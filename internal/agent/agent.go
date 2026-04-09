package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/imduchuyyy/opencm/internal/config"
	"github.com/imduchuyyy/opencm/internal/database"
	"github.com/imduchuyyy/opencm/internal/llm"
	"github.com/imduchuyyy/opencm/internal/tools"
)

// Agent represents a running community manager bot
type Agent struct {
	bot       *tgbotapi.BotAPI
	botID     int64
	db        *database.DB
	appConfig *config.Config
	cancel    context.CancelFunc
}

// NewAgent creates a new agent for a registered bot
func NewAgent(botToken string, botID int64, db *database.DB, appConfig *config.Config) (*Agent, error) {
	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		return nil, fmt.Errorf("create bot API: %w", err)
	}

	return &Agent{
		bot:       bot,
		botID:     botID,
		db:        db,
		appConfig: appConfig,
	}, nil
}

// Start begins the agent's message polling and processing loops
func (a *Agent) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel

	// Start polling for new messages
	go a.pollMessages(ctx)
	// Start the 5-second processing loop
	go a.processLoop(ctx)

	log.Printf("[Agent %d] Started (%s)", a.botID, a.bot.Self.UserName)
}

// Stop gracefully stops the agent
func (a *Agent) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
	a.bot.StopReceivingUpdates()
	log.Printf("[Agent %d] Stopped", a.botID)
}

// pollMessages receives Telegram updates and stores them in the database
func (a *Agent) pollMessages(ctx context.Context) {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30

	updates := a.bot.GetUpdatesChan(u)

	for {
		select {
		case <-ctx.Done():
			return
		case update := <-updates:
			a.handleUpdate(update)
		}
	}
}

// handleUpdate processes a single Telegram update
func (a *Agent) handleUpdate(update tgbotapi.Update) {
	// Handle being added/removed from groups
	if update.MyChatMember != nil {
		a.handleMyChatMember(update.MyChatMember)
		return
	}

	// Handle messages
	if update.Message != nil {
		msg := update.Message
		log.Printf("[Agent %d] Received message from %s (chat %d, type %s): %s",
			a.botID, msg.From.UserName, msg.Chat.ID, msg.Chat.Type, msg.Text)

		// Track group membership
		if msg.Chat.IsGroup() || msg.Chat.IsSuperGroup() {
			a.db.UpsertBotGroup(&database.BotGroup{
				BotID:     a.botID,
				ChatID:    msg.Chat.ID,
				ChatTitle: msg.Chat.Title,
				ChatType:  msg.Chat.Type,
			})
		}

		// Handle private messages (setup commands from owner)
		if msg.Chat.IsPrivate() {
			a.handlePrivateMessage(msg)
			return
		}

		// Handle /save command in groups (owner saves a message as knowledge)
		if msg.Text == "/save" || strings.HasPrefix(msg.Text, "/save@") {
			a.handleSaveCommand(msg)
			return
		}

		// Store ALL group messages in the database
		fromUser := msg.From
		if fromUser == nil {
			return
		}

		// Extract reply_to_message_id if this is a reply
		var replyToMsgID int
		if msg.ReplyToMessage != nil {
			replyToMsgID = msg.ReplyToMessage.MessageID
		}

		dbMsg := &database.Message{
			BotID:            a.botID,
			ChatID:           msg.Chat.ID,
			ChatType:         msg.Chat.Type,
			MessageID:        msg.MessageID,
			ReplyToMessageID: replyToMsgID,
			FromUserID:       fromUser.ID,
			FromUsername:     fromUser.UserName,
			FromFirstName:    fromUser.FirstName,
			Text:             msg.Text,
		}

		if err := a.db.SaveMessage(dbMsg); err != nil {
			log.Printf("[Agent %d] Error saving message: %v", a.botID, err)
		}
	}
}

// handleMyChatMember tracks when bot is added/removed from groups
func (a *Agent) handleMyChatMember(member *tgbotapi.ChatMemberUpdated) {
	chat := member.Chat
	newStatus := member.NewChatMember.Status

	if newStatus == "member" || newStatus == "administrator" {
		log.Printf("[Agent %d] Added to group: %s (%d)", a.botID, chat.Title, chat.ID)
		a.db.UpsertBotGroup(&database.BotGroup{
			BotID:     a.botID,
			ChatID:    chat.ID,
			ChatTitle: chat.Title,
			ChatType:  chat.Type,
		})
	} else if newStatus == "left" || newStatus == "kicked" {
		log.Printf("[Agent %d] Removed from group: %s (%d)", a.botID, chat.Title, chat.ID)
		a.db.DeactivateBotGroup(a.botID, chat.ID)
	}
}

// processLoop runs every 5 seconds to batch-process unread messages
func (a *Agent) processLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.processBatch(ctx)
		}
	}
}

// processBatch gets all unprocessed messages and sends them to the AI
func (a *Agent) processBatch(ctx context.Context) {
	msgs, err := a.db.GetUnprocessedMessages(a.botID)
	if err != nil {
		log.Printf("[Agent %d] Error getting unprocessed messages: %v", a.botID, err)
		return
	}
	if len(msgs) == 0 {
		return
	}

	// Get agent config
	agentCfg, err := a.db.GetAgentConfig(a.botID)
	if err != nil {
		log.Printf("[Agent %d] Error getting agent config: %v", a.botID, err)
		ids := make([]int64, len(msgs))
		for i, m := range msgs {
			ids[i] = m.ID
		}
		a.db.MarkMessagesProcessed(ids, "error: no agent config")
		return
	}

	// Group messages by chat
	chatMessages := make(map[int64][]*database.Message)
	for _, msg := range msgs {
		chatMessages[msg.ChatID] = append(chatMessages[msg.ChatID], msg)
	}

	// Process each chat's messages
	for chatID, chatMsgs := range chatMessages {
		a.processChatMessages(ctx, chatID, chatMsgs, agentCfg)
	}
}

// isMentioned checks if any message in the batch mentions the bot by @username, first name,
// or is a reply to one of the bot's own messages.
// Supports fuzzy matching: if the bot's first name is "Lucci Girl", mentioning just "Lucci" or "Girl" will match.
// Each word of the first name is checked individually (minimum 3 chars to avoid false positives).
func (a *Agent) isMentioned(msgs []*database.Message) bool {
	botUsername := strings.ToLower(a.bot.Self.UserName)
	botFirstName := strings.ToLower(a.bot.Self.FirstName)
	botUserID := a.bot.Self.ID

	// Build list of name words to match (full name + individual words)
	var nameWords []string
	if botFirstName != "" {
		nameWords = append(nameWords, botFirstName) // full first name
		for _, word := range strings.Fields(botFirstName) {
			word = strings.TrimSpace(word)
			if len(word) >= 3 && word != botFirstName { // skip duplicates and short words
				nameWords = append(nameWords, word)
			}
		}
	}

	for _, msg := range msgs {
		text := strings.ToLower(msg.Text)
		// Check @username mention
		if strings.Contains(text, "@"+botUsername) {
			return true
		}
		// Check first name mention (full name or any word of the name)
		for _, name := range nameWords {
			if strings.Contains(text, name) {
				return true
			}
		}
		// Check if this message is a reply to one of the bot's own messages
		if msg.ReplyToMessageID > 0 {
			repliedMsg, err := a.db.GetMessageByTelegramID(a.botID, msg.ChatID, msg.ReplyToMessageID)
			if err == nil && repliedMsg.FromUserID == botUserID {
				return true
			}
		}
	}
	return false
}

// fetchGroupContext fetches live group info from the Telegram API for the system prompt
func (a *Agent) fetchGroupContext(chatID int64) *GroupContext {
	gc := &GroupContext{}

	// Get chat info (title, description, pinned message)
	chatCfg := tgbotapi.ChatInfoConfig{
		ChatConfig: tgbotapi.ChatConfig{ChatID: chatID},
	}
	chat, err := a.bot.GetChat(chatCfg)
	if err != nil {
		log.Printf("[Agent %d] Error fetching chat info for %d: %v", a.botID, chatID, err)
		return gc
	}

	gc.GroupName = chat.Title
	gc.GroupDescription = chat.Description
	if chat.PinnedMessage != nil {
		gc.PinnedMessage = chat.PinnedMessage.Text
	}

	// Get admin info (the owner of this bot)
	botInfo, err := a.db.GetBotByBotID(a.botID)
	if err == nil {
		// Try to get the admin's chat member info for username/first name
		adminCfg := tgbotapi.GetChatMemberConfig{
			ChatConfigWithUser: tgbotapi.ChatConfigWithUser{
				ChatID: chatID,
				UserID: botInfo.OwnerID,
			},
		}
		adminMember, err := a.bot.GetChatMember(adminCfg)
		if err == nil {
			gc.AdminName = adminMember.User.FirstName
			gc.AdminUsername = adminMember.User.UserName
		}
	}

	return gc
}

// GroupContext holds info about a group for the system prompt
type GroupContext struct {
	GroupName        string
	GroupDescription string
	PinnedMessage    string
	AdminName        string
	AdminUsername    string
}

// processChatMessages handles messages from a single chat using the OpenAI Responses API
func (a *Agent) processChatMessages(ctx context.Context, chatID int64, msgs []*database.Message, agentCfg *database.AgentConfig) {
	// Check if the bot is mentioned in any of the new messages
	// If not mentioned, mark all as processed and skip the LLM call to save tokens
	if !a.isMentioned(msgs) {
		ids := make([]int64, len(msgs))
		for i, m := range msgs {
			ids[i] = m.ID
		}
		a.db.MarkMessagesProcessed(ids, "no mention - skipped")
		return
	}

	// Get last 10 messages for context
	recentMsgs, _ := a.db.GetRecentMessages(a.botID, chatID, 10)

	// Collect message IDs that are already in the recent context
	recentMsgIDs := make(map[int]bool)
	for _, m := range recentMsgs {
		recentMsgIDs[m.MessageID] = true
	}

	// Walk reply chains: for any message (in recent or new) that is a reply,
	// fetch the replied-to message if it's not already in context.
	// This gives the AI the full conversation thread even if messages are far apart.
	var replyContext []*database.Message
	seenReplyIDs := make(map[int]bool)
	var collectReplies func(messages []*database.Message)
	collectReplies = func(messages []*database.Message) {
		for _, m := range messages {
			if m.ReplyToMessageID > 0 && !recentMsgIDs[m.ReplyToMessageID] && !seenReplyIDs[m.ReplyToMessageID] {
				seenReplyIDs[m.ReplyToMessageID] = true
				repliedMsg, err := a.db.GetMessageByTelegramID(a.botID, chatID, m.ReplyToMessageID)
				if err == nil {
					replyContext = append(replyContext, repliedMsg)
					// Recursively fetch the reply's parent too (up to reasonable depth)
					if len(replyContext) < 10 {
						collectReplies([]*database.Message{repliedMsg})
					}
				}
			}
		}
	}
	collectReplies(recentMsgs)
	collectReplies(msgs)

	// Fetch group context (name, description, pinned message, admin)
	groupCtx := a.fetchGroupContext(chatID)

	// Get owner ID for config tools
	var ownerID int64
	botInfo, err := a.db.GetBotByBotID(a.botID)
	if err == nil {
		ownerID = botInfo.OwnerID
	}

	// Determine model
	model := agentCfg.Model
	if model == "" {
		model = a.appConfig.DefaultModel
	}

	apiKey := a.appConfig.OpenAIAPIKey
	if apiKey == "" {
		log.Printf("[Agent %d] No OpenAI API key configured", a.botID)
		return
	}

	client := llm.NewClient(apiKey, model)

	// Build system prompt and user messages
	systemPrompt := buildSystemPrompt(agentCfg, groupCtx, a.bot.Self.UserName, a.bot.Self.FirstName)
	userMessages := buildUserMessages(recentMsgs, msgs, replyContext)

	// Get available tools (include config tools)
	availableTools := tools.GetAvailableTools(agentCfg)

	// Call the AI
	response, err := client.Chat(ctx, systemPrompt, userMessages, availableTools, agentCfg.VectorStoreID)
	if err != nil {
		log.Printf("[Agent %d] LLM error: %v", a.botID, err)
		return
	}

	// Tool execution loop
	executor := tools.NewExecutor(a.bot, agentCfg, a.db, a.botID, ownerID)
	var allResults []string
	maxIterations := 5

	for i := 0; i < maxIterations; i++ {
		if response.Skip || len(response.ToolCalls) == 0 {
			break
		}

		if len(response.ToolCalls) == 1 && response.ToolCalls[0].Name == "skip" {
			allResults = append(allResults, "skipped")
			break
		}

		var toolResults []llm.ToolResult
		for _, tc := range response.ToolCalls {
			result, err := executor.Execute(tc)
			if err != nil {
				log.Printf("[Agent %d] Tool %s error: %v", a.botID, tc.Name, err)
				toolResults = append(toolResults, llm.ToolResult{
					CallID: tc.CallID,
					Output: fmt.Sprintf("error: %v", err),
				})
				allResults = append(allResults, fmt.Sprintf("%s: error - %v", tc.Name, err))
			} else {
				toolResults = append(toolResults, llm.ToolResult{
					CallID: tc.CallID,
					Output: result,
				})
				allResults = append(allResults, fmt.Sprintf("%s: %s", tc.Name, result))
			}
		}

		response, err = client.ContinueWithToolResults(ctx, response.ResponseID, toolResults, availableTools, agentCfg.VectorStoreID)
		if err != nil {
			log.Printf("[Agent %d] LLM continue error: %v", a.botID, err)
			break
		}
	}

	// Only log response text if it's meaningful (not "DONE" or empty)
	responseText := strings.TrimSpace(response.Text)
	if responseText != "" && !strings.EqualFold(responseText, "done") {
		allResults = append(allResults, "text: "+responseText)
	}

	aiResponseSummary := "skipped"
	if len(allResults) > 0 {
		aiResponseSummary = strings.Join(allResults, "; ")
	}

	// Mark messages as processed
	ids := make([]int64, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	if err := a.db.MarkMessagesProcessed(ids, aiResponseSummary); err != nil {
		log.Printf("[Agent %d] Error marking messages processed: %v", a.botID, err)
	}
}

// buildUserMessages constructs the user message strings for the LLM
// replyContext contains messages that are reply targets but fall outside the recent 10 messages
func buildUserMessages(history []*database.Message, newMsgs []*database.Message, replyContext []*database.Message) []string {
	var messages []string

	// Reply context: older messages referenced by replies in the conversation
	if len(replyContext) > 0 {
		var replyParts []string
		for _, msg := range replyContext {
			name := msg.FromFirstName
			if name == "" {
				name = msg.FromUsername
			}
			replyParts = append(replyParts, fmt.Sprintf("[%s, MsgID:%d] %s (@%s, ID:%d): %s",
				msg.CreatedAt.Format("15:04"), msg.MessageID, name, msg.FromUsername, msg.FromUserID, msg.Text))
		}
		messages = append(messages, "=== REFERENCED MESSAGES (older messages that are being replied to) ===\n"+strings.Join(replyParts, "\n"))
	}

	// Recent history for context (last 10 messages)
	if len(history) > 0 {
		var historyParts []string
		for _, msg := range history {
			name := msg.FromFirstName
			if name == "" {
				name = msg.FromUsername
			}
			line := fmt.Sprintf("[%s, MsgID:%d] %s (@%s, ID:%d): %s",
				msg.CreatedAt.Format("15:04"), msg.MessageID, name, msg.FromUsername, msg.FromUserID, msg.Text)
			if msg.ReplyToMessageID > 0 {
				line = fmt.Sprintf("[%s, MsgID:%d, ReplyTo:%d] %s (@%s, ID:%d): %s",
					msg.CreatedAt.Format("15:04"), msg.MessageID, msg.ReplyToMessageID, name, msg.FromUsername, msg.FromUserID, msg.Text)
			}
			historyParts = append(historyParts, line)
		}
		messages = append(messages, "=== RECENT CHAT HISTORY (last 10 messages for context) ===\n"+strings.Join(historyParts, "\n"))
	}

	// New unprocessed messages that triggered this call
	var newParts []string
	for _, msg := range newMsgs {
		name := msg.FromFirstName
		if name == "" {
			name = msg.FromUsername
		}
		line := fmt.Sprintf("[ChatID:%d, MsgID:%d] %s (@%s, UserID:%d): %s",
			msg.ChatID, msg.MessageID, name, msg.FromUsername, msg.FromUserID, msg.Text)
		if msg.ReplyToMessageID > 0 {
			line = fmt.Sprintf("[ChatID:%d, MsgID:%d, ReplyTo:%d] %s (@%s, UserID:%d): %s",
				msg.ChatID, msg.MessageID, msg.ReplyToMessageID, name, msg.FromUsername, msg.FromUserID, msg.Text)
		}
		newParts = append(newParts, line)
	}
	messages = append(messages, "=== NEW MESSAGES (you were mentioned or replied to) ===\n"+strings.Join(newParts, "\n")+
		"\n\nYou were mentioned or someone replied to your message. Decide what actions to take. You can call multiple tools. Use 'skip' only if the mention doesn't need a response.")

	return messages
}

// buildSystemPrompt creates the system prompt from agent config and group context
func buildSystemPrompt(cfg *database.AgentConfig, gc *GroupContext, botUsername, botFirstName string) string {
	var parts []string

	parts = append(parts, "You are an AI community manager bot for a Telegram group.")

	// Identity
	parts = append(parts, fmt.Sprintf("## Your Identity\nYour Telegram username: @%s\nYour name: %s", botUsername, botFirstName))

	// Group context
	if gc != nil {
		var groupParts []string
		if gc.GroupName != "" {
			groupParts = append(groupParts, "Group name: "+gc.GroupName)
		}
		if gc.GroupDescription != "" {
			groupParts = append(groupParts, "Group description: "+gc.GroupDescription)
		}
		if gc.PinnedMessage != "" {
			groupParts = append(groupParts, "Pinned message: "+gc.PinnedMessage)
		}
		if gc.AdminName != "" {
			admin := gc.AdminName
			if gc.AdminUsername != "" {
				admin += " (@" + gc.AdminUsername + ")"
			}
			groupParts = append(groupParts, "Group admin/owner: "+admin)
		}
		if len(groupParts) > 0 {
			parts = append(parts, "## Group Context\n"+strings.Join(groupParts, "\n"))
		}
	}

	if cfg.SystemPrompt != "" {
		parts = append(parts, "## Core Instructions\n"+cfg.SystemPrompt)
	}

	if cfg.Bio != "" {
		parts = append(parts, "## Your Bio/Identity\n"+cfg.Bio)
	}

	if cfg.Topics != "" {
		parts = append(parts, "## Topics You Cover\n"+cfg.Topics)
	}

	if cfg.ChatStyle != "" {
		parts = append(parts, "## Chat Style\n"+cfg.ChatStyle)
	}

	if cfg.MessageExamples != "" {
		parts = append(parts, "## Example Messages (match this style)\n"+cfg.MessageExamples)
	}

	parts = append(parts, fmt.Sprintf(`## Behavior Guidelines
- You respond when someone mentions you (by @%s or by name "%s") OR when someone replies to one of your messages.
- When a user replies to your message, treat it as a continuation of the conversation and respond accordingly.
- Messages with "ReplyTo:X" mean they are replying to message with MsgID X. Check the referenced messages section for older messages being replied to.
- When mentioned or replied to, respond helpfully and in character.
- Be natural and conversational, not robotic.
- When replying, always include the correct chat_id and reply_to_message_id to reply to the message that mentioned you or the latest message in the thread.
- For moderation actions (ban, mute, delete), only act on clear violations and only if asked by the admin.
- You can call multiple tools in one response (e.g., reply to a message AND react to another).
- You have access to get_config and set_config tools. Only the group admin can change your configuration. If a non-admin tries to change settings, politely decline.`, botUsername, botFirstName))

	parts = append(parts, `## Tool Usage Rules
- After you call tools (especially send_message), do NOT output any additional text. The tools already perform the actions (send_message sends the message to the chat). Just call the tools and output nothing, or output "DONE" to signal completion.
- You must ALWAYS use the send_message tool to communicate with users. Any text you output directly will NOT be sent to the chat - it is only logged internally.`)

	parts = append(parts, `## Knowledge Base (file_search)
- You have access to a file_search tool that searches uploaded knowledge documents.
- ONLY use file_search when someone asks a specific question that likely requires information from the knowledge base (e.g., project details, rules, FAQs, documentation).
- Do NOT use file_search for casual messages, greetings, general chat, or questions you can answer from context alone.
- When in doubt, answer from what you already know first. Only search if the question clearly needs specific facts from the documents.`)

	return strings.Join(parts, "\n\n")
}

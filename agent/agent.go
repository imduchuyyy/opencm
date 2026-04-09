package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/imduchuyyy/opencm/config"
	"github.com/imduchuyyy/opencm/database"
	"github.com/imduchuyyy/opencm/llm"
	"github.com/imduchuyyy/opencm/plan"
	"github.com/imduchuyyy/opencm/tools"
)

// Agent represents the single community manager bot instance
type Agent struct {
	bot       *tgbotapi.BotAPI
	db        *database.DB
	appConfig *config.Config
	cancel    context.CancelFunc
}

// New creates the agent bot
func New(cfg *config.Config, db *database.DB) (*Agent, error) {
	bot, err := tgbotapi.NewBotAPI(cfg.BotToken)
	if err != nil {
		return nil, fmt.Errorf("create bot API: %w", err)
	}
	log.Printf("[Agent] Bot started as @%s (ID: %d)", bot.Self.UserName, bot.Self.ID)

	return &Agent{
		bot:       bot,
		db:        db,
		appConfig: cfg,
	}, nil
}

// Start begins the bot's message polling and processing loops
func (a *Agent) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel

	go a.pollMessages(ctx)
	go a.processLoop(ctx)

	log.Printf("[Agent] Running as @%s", a.bot.Self.UserName)

	// Block until context is cancelled
	<-ctx.Done()
}

// Stop gracefully stops the agent
func (a *Agent) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
	a.bot.StopReceivingUpdates()
	log.Println("[Agent] Stopped")
}

// sendThinking sends a "Thinking..." message. Returns the message ID (0 on failure).
func (a *Agent) sendThinking(chatID int64) int {
	msg := tgbotapi.NewMessage(chatID, "Thinking...")
	sent, err := a.bot.Send(msg)
	if err != nil {
		log.Printf("[Agent] Failed to send thinking message: %v", err)
		return 0
	}
	return sent.MessageID
}

// deleteThinking deletes the "Thinking..." message. No-op if messageID is 0.
func (a *Agent) deleteThinking(chatID int64, messageID int) {
	if messageID == 0 {
		return
	}
	deleteMsg := tgbotapi.NewDeleteMessage(chatID, messageID)
	if _, err := a.bot.Request(deleteMsg); err != nil {
		log.Printf("[Agent] Failed to delete thinking message: %v", err)
	}
}

// isVisionMedia returns true if the media type can be understood by OpenAI vision
func isVisionMedia(mediaType string) bool {
	return mediaType == "photo" || mediaType == "animation"
}

// resolveMediaURLs resolves Telegram file IDs to download URLs for vision-compatible media.
func (a *Agent) resolveMediaURLs(msgs ...[]*database.Message) map[int]string {
	result := make(map[int]string)
	for _, group := range msgs {
		for _, msg := range group {
			if msg.MediaFileID == "" || !isVisionMedia(msg.MediaType) {
				continue
			}
			if _, exists := result[msg.MessageID]; exists {
				continue
			}
			fileConfig := tgbotapi.FileConfig{FileID: msg.MediaFileID}
			tgFile, err := a.bot.GetFile(fileConfig)
			if err != nil {
				log.Printf("[Agent] Error getting file for msg %d: %v", msg.MessageID, err)
				continue
			}
			result[msg.MessageID] = tgFile.Link(a.bot.Token)
		}
	}
	return result
}

// isAdmin checks if a user is an admin/creator of a group via Telegram API
func (a *Agent) isAdmin(chatID, userID int64) bool {
	admins, err := a.bot.GetChatAdministrators(tgbotapi.ChatAdministratorsConfig{
		ChatConfig: tgbotapi.ChatConfig{ChatID: chatID},
	})
	if err != nil {
		log.Printf("[Agent] Error getting admins for chat %d: %v", chatID, err)
		return false
	}
	for _, admin := range admins {
		if admin.User.ID == userID {
			return true
		}
	}
	return false
}

// getAdminGroups returns a list of active groups where the given user is an admin.
// Only checks groups where the user has been seen (tracked via group_members table),
// avoiding O(N) Telegram API calls across all groups.
func (a *Agent) getAdminGroups(userID int64) []*database.Group {
	groups, err := a.db.GetUserGroups(userID)
	if err != nil {
		log.Printf("[Agent] Error getting user groups: %v", err)
		return nil
	}

	var result []*database.Group
	for _, g := range groups {
		if a.isAdmin(g.ChatID, userID) {
			result = append(result, g)
		}
	}
	return result
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

	if update.Message == nil {
		return
	}

	msg := update.Message

	// Log received message
	logText := msg.Text
	if logText == "" && msg.Caption != "" {
		logText = "[caption] " + msg.Caption
	}
	if logText == "" {
		if msg.Photo != nil {
			logText = "[photo]"
		} else if msg.Video != nil {
			logText = "[video]"
		} else if msg.Document != nil {
			logText = "[document]"
		} else if msg.Sticker != nil {
			logText = "[sticker]"
		} else if msg.Voice != nil {
			logText = "[voice]"
		} else if msg.Animation != nil {
			logText = "[animation]"
		}
	}
	log.Printf("[Agent] Message from %s (chat %d, type %s): %s",
		msg.From.UserName, msg.Chat.ID, msg.Chat.Type, logText)

	// Handle private messages (setup commands from admins)
	if msg.Chat.IsPrivate() {
		a.handlePrivateMessage(msg)
		return
	}

	// Track group membership
	if msg.Chat.IsGroup() || msg.Chat.IsSuperGroup() {
		a.db.UpsertGroup(&database.Group{
			ChatID:    msg.Chat.ID,
			ChatTitle: msg.Chat.Title,
			ChatType:  msg.Chat.Type,
		})
		// Track that this user is in this group
		if msg.From != nil {
			a.db.UpsertGroupMember(msg.Chat.ID, msg.From.ID)
		}
	}

	// Handle /save command in groups
	if msg.Text == "/save" || strings.HasPrefix(msg.Text, "/save@") {
		a.handleSaveCommand(msg)
		return
	}

	// Store ALL group messages in the database
	fromUser := msg.From
	if fromUser == nil {
		return
	}

	var replyToMsgID int
	if msg.ReplyToMessage != nil {
		replyToMsgID = msg.ReplyToMessage.MessageID
	}

	text := msg.Text
	if text == "" && msg.Caption != "" {
		text = msg.Caption
	}

	var mediaType, mediaFileID string
	if msg.Photo != nil && len(msg.Photo) > 0 {
		mediaType = "photo"
		mediaFileID = msg.Photo[len(msg.Photo)-1].FileID
	} else if msg.Video != nil {
		mediaType = "video"
		mediaFileID = msg.Video.FileID
	} else if msg.Document != nil {
		mediaType = "document"
		mediaFileID = msg.Document.FileID
	} else if msg.Animation != nil {
		mediaType = "animation"
		mediaFileID = msg.Animation.FileID
	} else if msg.Voice != nil {
		mediaType = "voice"
		mediaFileID = msg.Voice.FileID
	} else if msg.Sticker != nil {
		mediaType = "sticker"
		mediaFileID = msg.Sticker.FileID
	} else if msg.VideoNote != nil {
		mediaType = "video_note"
		mediaFileID = msg.VideoNote.FileID
	}

	dbMsg := &database.Message{
		ChatID:           msg.Chat.ID,
		ChatType:         msg.Chat.Type,
		MessageID:        msg.MessageID,
		ReplyToMessageID: replyToMsgID,
		FromUserID:       fromUser.ID,
		FromUsername:     fromUser.UserName,
		FromFirstName:    fromUser.FirstName,
		Text:             text,
		MediaType:        mediaType,
		MediaFileID:      mediaFileID,
	}

	if err := a.db.SaveMessage(dbMsg); err != nil {
		log.Printf("[Agent] Error saving message: %v", err)
	}
}

// handleMyChatMember tracks when bot is added/removed from groups
func (a *Agent) handleMyChatMember(member *tgbotapi.ChatMemberUpdated) {
	chat := member.Chat
	newStatus := member.NewChatMember.Status

	if newStatus == "member" || newStatus == "administrator" {
		log.Printf("[Agent] Added to group: %s (%d)", chat.Title, chat.ID)
		a.db.UpsertGroup(&database.Group{
			ChatID:    chat.ID,
			ChatTitle: chat.Title,
			ChatType:  chat.Type,
		})
	} else if newStatus == "left" || newStatus == "kicked" {
		log.Printf("[Agent] Removed from group: %s (%d)", chat.Title, chat.ID)
		a.db.DeactivateGroup(chat.ID)
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
	msgs, err := a.db.GetUnprocessedMessages()
	if err != nil {
		log.Printf("[Agent] Error getting unprocessed messages: %v", err)
		return
	}
	if len(msgs) == 0 {
		return
	}

	// Group messages by chat
	chatMessages := make(map[int64][]*database.Message)
	for _, msg := range msgs {
		chatMessages[msg.ChatID] = append(chatMessages[msg.ChatID], msg)
	}

	// Process each chat's messages
	for chatID, chatMsgs := range chatMessages {
		a.processChatMessages(ctx, chatID, chatMsgs)
	}
}

// isMentioned checks if any message in the batch mentions the bot
func (a *Agent) isMentioned(chatID int64, msgs []*database.Message) bool {
	botUsername := strings.ToLower(a.bot.Self.UserName)
	botFirstName := strings.ToLower(a.bot.Self.FirstName)
	botUserID := a.bot.Self.ID

	var nameWords []string
	if botFirstName != "" {
		nameWords = append(nameWords, botFirstName)
		for _, word := range strings.Fields(botFirstName) {
			word = strings.TrimSpace(word)
			if len(word) >= 3 && word != botFirstName {
				nameWords = append(nameWords, word)
			}
		}
	}

	for _, msg := range msgs {
		text := strings.ToLower(msg.Text)
		if strings.Contains(text, "@"+botUsername) {
			return true
		}
		for _, name := range nameWords {
			if strings.Contains(text, name) {
				return true
			}
		}
		if msg.ReplyToMessageID > 0 {
			repliedMsg, err := a.db.GetMessageByTelegramID(chatID, msg.ReplyToMessageID)
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

	chatCfg := tgbotapi.ChatInfoConfig{
		ChatConfig: tgbotapi.ChatConfig{ChatID: chatID},
	}
	chat, err := a.bot.GetChat(chatCfg)
	if err != nil {
		log.Printf("[Agent] Error fetching chat info for %d: %v", chatID, err)
		return gc
	}

	gc.GroupName = chat.Title
	gc.GroupDescription = chat.Description
	if chat.PinnedMessage != nil {
		gc.PinnedMessage = chat.PinnedMessage.Text
	}

	// Get admin list for context
	admins, err := a.bot.GetChatAdministrators(tgbotapi.ChatAdministratorsConfig{
		ChatConfig: tgbotapi.ChatConfig{ChatID: chatID},
	})
	if err == nil {
		for _, admin := range admins {
			if admin.IsCreator() {
				gc.AdminName = admin.User.FirstName
				gc.AdminUsername = admin.User.UserName
				break
			}
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

// getOrCreateGroupConfig returns the group config, creating a default if none exists
func (a *Agent) getOrCreateGroupConfig(chatID int64) *database.GroupConfig {
	cfg, err := a.db.GetGroupConfig(chatID)
	if err == nil {
		return cfg
	}
	// Create default config
	cfg = &database.GroupConfig{
		ChatID:    chatID,
		Plan:      plan.Free,
		Model:     a.appConfig.DefaultModel,
		ChatStyle: "friendly and helpful",
		CanReply:  true,
	}
	a.db.UpsertGroupConfig(cfg)
	return cfg
}

// processChatMessages handles messages from a single chat
func (a *Agent) processChatMessages(ctx context.Context, chatID int64, msgs []*database.Message) {
	// Check if the bot is mentioned
	if !a.isMentioned(chatID, msgs) {
		ids := make([]int64, len(msgs))
		for i, m := range msgs {
			ids[i] = m.ID
		}
		a.db.MarkMessagesProcessed(ids, "no mention - skipped")
		return
	}

	// Load per-group config
	groupCfg := a.getOrCreateGroupConfig(chatID)

	// Enforce plan limits before calling the LLM
	limits := plan.GetLimits(groupCfg.Plan)

	monthlyUsage, err := a.db.GetMonthlyUsage(chatID)
	if err != nil {
		log.Printf("[Agent] Error getting monthly usage for chat %d: %v", chatID, err)
	} else if monthlyUsage >= limits.MonthlyMessages {
		log.Printf("[Agent] Chat %d hit monthly limit (%d/%d, plan: %s)", chatID, monthlyUsage, limits.MonthlyMessages, groupCfg.Plan)
		ids := make([]int64, len(msgs))
		for i, m := range msgs {
			ids[i] = m.ID
		}
		a.db.MarkMessagesProcessed(ids, "monthly limit reached")
		return
	}

	minuteUsage, err := a.db.GetMinuteUsage(chatID)
	if err != nil {
		log.Printf("[Agent] Error getting minute usage for chat %d: %v", chatID, err)
	} else if minuteUsage >= limits.PerMinute {
		log.Printf("[Agent] Chat %d hit rate limit (%d/%d per min, plan: %s)", chatID, minuteUsage, limits.PerMinute, groupCfg.Plan)
		ids := make([]int64, len(msgs))
		for i, m := range msgs {
			ids[i] = m.ID
		}
		a.db.MarkMessagesProcessed(ids, "rate limit reached")
		return
	}

	// Get last 10 messages for context
	recentMsgs, _ := a.db.GetRecentMessages(chatID, 10)

	// Collect recent message IDs
	recentMsgIDs := make(map[int]bool)
	for _, m := range recentMsgs {
		recentMsgIDs[m.MessageID] = true
	}

	// Walk reply chains
	var replyContext []*database.Message
	seenReplyIDs := make(map[int]bool)
	var collectReplies func(messages []*database.Message)
	collectReplies = func(messages []*database.Message) {
		for _, m := range messages {
			if m.ReplyToMessageID > 0 && !recentMsgIDs[m.ReplyToMessageID] && !seenReplyIDs[m.ReplyToMessageID] {
				seenReplyIDs[m.ReplyToMessageID] = true
				repliedMsg, err := a.db.GetMessageByTelegramID(chatID, m.ReplyToMessageID)
				if err == nil {
					replyContext = append(replyContext, repliedMsg)
					if len(replyContext) < 10 {
						collectReplies([]*database.Message{repliedMsg})
					}
				}
			}
		}
	}
	collectReplies(recentMsgs)
	collectReplies(msgs)

	// Fetch group context
	groupCtx := a.fetchGroupContext(chatID)

	// Determine model
	model := groupCfg.Model
	if model == "" {
		model = a.appConfig.DefaultModel
	}

	apiKey := a.appConfig.OpenAIAPIKey
	if apiKey == "" {
		log.Printf("[Agent] No OpenAI API key configured")
		return
	}

	client := llm.NewClient(apiKey, model)

	// Build system prompt and user messages
	systemPrompt := buildSystemPrompt(groupCfg, groupCtx, a.bot.Self.UserName, a.bot.Self.FirstName)

	// Resolve image URLs from Telegram for vision-compatible media
	mediaURLs := a.resolveMediaURLs(recentMsgs, msgs, replyContext)

	userMessages := buildUserMessages(recentMsgs, msgs, replyContext, mediaURLs)

	// Get available tools
	availableTools := tools.GetAvailableTools(groupCfg)

	// Send "Thinking..." indicator
	thinkingMsgID := a.sendThinking(chatID)

	// Call the AI
	response, err := client.Chat(ctx, systemPrompt, userMessages, availableTools, groupCfg.VectorStoreID)
	if err != nil {
		log.Printf("[Agent] LLM error for chat %d: %v", chatID, err)
		a.deleteThinking(chatID, thinkingMsgID)
		return
	}

	// Tool execution loop
	executor := tools.NewExecutor(a.bot, groupCfg, a.db, chatID, a.appConfig.LangSearchAPIKey)
	var allResults []string
	maxIterations := 5

	for i := 0; i < maxIterations; i++ {
		if len(response.ToolCalls) == 0 {
			break
		}

		var toolResults []llm.ToolResult
		for _, tc := range response.ToolCalls {
			result, err := executor.Execute(tc)
			if err != nil {
				log.Printf("[Agent] Tool %s error: %v", tc.Name, err)
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

		response, err = client.ContinueWithToolResults(ctx, response.ResponseID, toolResults, availableTools, groupCfg.VectorStoreID)
		if err != nil {
			log.Printf("[Agent] LLM continue error: %v", err)
			break
		}
	}

	// Delete the "Thinking..." message
	a.deleteThinking(chatID, thinkingMsgID)

	// Log usage for this AI response
	if err := a.db.LogUsage(chatID); err != nil {
		log.Printf("[Agent] Error logging usage for chat %d: %v", chatID, err)
	}

	responseText := strings.TrimSpace(response.Text)
	if responseText != "" && !strings.EqualFold(responseText, "done") {
		allResults = append(allResults, "text: "+responseText)
	}

	aiResponseSummary := "skipped"
	if len(allResults) > 0 {
		aiResponseSummary = strings.Join(allResults, "; ")
	}

	ids := make([]int64, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	if err := a.db.MarkMessagesProcessed(ids, aiResponseSummary); err != nil {
		log.Printf("[Agent] Error marking messages processed: %v", err)
	}
}

// formatMessageLine formats a single database message into a text line for the LLM
func formatMessageLine(msg *database.Message, includeChat bool) string {
	name := msg.FromFirstName
	if name == "" {
		name = msg.FromUsername
	}
	mediaTag := ""
	if msg.MediaType != "" {
		mediaTag = fmt.Sprintf(" [%s]", msg.MediaType)
	}
	text := msg.Text
	if text == "" && msg.MediaType != "" {
		text = fmt.Sprintf("(%s with no caption)", msg.MediaType)
	}

	if includeChat {
		if msg.ReplyToMessageID > 0 {
			return fmt.Sprintf("[ChatID:%d, MsgID:%d, ReplyTo:%d]%s %s (@%s, UserID:%d): %s",
				msg.ChatID, msg.MessageID, msg.ReplyToMessageID, mediaTag, name, msg.FromUsername, msg.FromUserID, text)
		}
		return fmt.Sprintf("[ChatID:%d, MsgID:%d]%s %s (@%s, UserID:%d): %s",
			msg.ChatID, msg.MessageID, mediaTag, name, msg.FromUsername, msg.FromUserID, text)
	}

	if msg.ReplyToMessageID > 0 {
		return fmt.Sprintf("[%s, MsgID:%d, ReplyTo:%d]%s %s (@%s, ID:%d): %s",
			msg.CreatedAt.Format("15:04"), msg.MessageID, msg.ReplyToMessageID, mediaTag, name, msg.FromUsername, msg.FromUserID, text)
	}
	return fmt.Sprintf("[%s, MsgID:%d]%s %s (@%s, ID:%d): %s",
		msg.CreatedAt.Format("15:04"), msg.MessageID, mediaTag, name, msg.FromUsername, msg.FromUserID, text)
}

// buildSectionMessages converts a group of messages into InputMessages.
func buildSectionMessages(msgs []*database.Message, mediaURLs map[int]string, header string, includeChat bool) []llm.InputMessage {
	var result []llm.InputMessage
	var textBatch []string

	flush := func() {
		if len(textBatch) == 0 {
			return
		}
		text := strings.Join(textBatch, "\n")
		if header != "" {
			text = header + "\n" + text
			header = ""
		}
		result = append(result, llm.InputMessage{Text: text})
		textBatch = nil
	}

	for _, msg := range msgs {
		line := formatMessageLine(msg, includeChat)
		imageURL, hasImage := mediaURLs[msg.MessageID]

		if hasImage {
			flush()
			text := line
			if header != "" {
				text = header + "\n" + text
				header = ""
			}
			result = append(result, llm.InputMessage{
				Text:      text,
				ImageURLs: []string{imageURL},
			})
		} else {
			textBatch = append(textBatch, line)
		}
	}

	flush()
	return result
}

// buildUserMessages constructs the user messages for the LLM.
func buildUserMessages(history []*database.Message, newMsgs []*database.Message, replyContext []*database.Message, mediaURLs map[int]string) []llm.InputMessage {
	var messages []llm.InputMessage

	if len(replyContext) > 0 {
		messages = append(messages,
			buildSectionMessages(replyContext, mediaURLs, "=== REFERENCED MESSAGES (older messages that are being replied to) ===", false)...)
	}

	if len(history) > 0 {
		messages = append(messages,
			buildSectionMessages(history, mediaURLs, "=== RECENT CHAT HISTORY (last 10 messages for context) ===", false)...)
	}

	newSectionMsgs := buildSectionMessages(newMsgs, mediaURLs, "=== NEW MESSAGES (you were mentioned or replied to) ===", true)
	messages = append(messages, newSectionMsgs...)

	if len(messages) > 0 {
		last := &messages[len(messages)-1]
		last.Text += "\n\nYou were mentioned or someone replied to your message. Decide what actions to take. You can call multiple tools."
	}

	return messages
}

// buildSystemPrompt creates the system prompt from group config and group context
func buildSystemPrompt(cfg *database.GroupConfig, gc *GroupContext, botUsername, botFirstName string) string {
	var parts []string

	parts = append(parts, "You are an AI community manager bot for a Telegram group.")

	parts = append(parts, fmt.Sprintf("## Your Identity\nYour Telegram username: @%s\nYour name: %s", botUsername, botFirstName))

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
- For moderation actions (ban, mute, delete), only act on clear violations and only if asked by an admin.
- You can call multiple tools in one response.
- You have access to get_config and set_config tools. Only group admins can change your configuration. If a non-admin tries to change settings, politely decline.`, botUsername, botFirstName))

	parts = append(parts, `## Tool Usage Rules
- After you call tools (especially send_message), do NOT output any additional text. The tools already perform the actions. Just call the tools and output nothing, or output "DONE" to signal completion.
- You must ALWAYS use the send_message tool to communicate with users. Any text you output directly will NOT be sent to the chat - it is only logged internally.`)

	parts = append(parts, `## Knowledge Base (file_search)
- You have access to a file_search tool that searches uploaded knowledge documents.
- ONLY use file_search when someone asks a specific question that likely requires information from the knowledge base.
- Do NOT use file_search for casual messages, greetings, general chat, or questions you can answer from context alone.`)

	parts = append(parts, `## Image Understanding
- You can see images and photos shared in the chat. When images are attached to messages, they are included as image content alongside the text.
- You can describe, analyze, and discuss images that users share.
- If a user sends or references an image and asks about it, use your vision capabilities to understand and respond about the image content.`)

	return strings.Join(parts, "\n\n")
}

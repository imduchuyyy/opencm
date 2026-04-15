package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
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
	llmClient *llm.Client
	cancel    context.CancelFunc
	processMu sync.Mutex // guards processBatch to prevent double-processing
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
		llmClient: llm.NewClient(cfg.OpenAIAPIKey, cfg.DefaultModel),
	}, nil
}

// Start begins the bot's message polling and processing loops
func (a *Agent) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel

	go a.pollMessages(ctx)
	go a.processLoop(ctx)
	go a.scheduledPostLoop(ctx)
	go a.engagementLoop(ctx)

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

// updateThinking edits the thinking message to show current activity. No-op if messageID is 0.
func (a *Agent) updateThinking(chatID int64, messageID int, text string) {
	if messageID == 0 {
		return
	}
	edit := tgbotapi.NewEditMessageText(chatID, messageID, text)
	if _, err := a.bot.Send(edit); err != nil {
		log.Printf("[Agent] Failed to update thinking message: %v", err)
	}
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

// sendReply sends the agent's text response as a reply to a specific message.
// Tries Markdown first, falls back to plain text. Saves the bot's message to DB.
func (a *Agent) sendReply(chatID int64, replyToMsgID int, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyToMessageID = replyToMsgID
	msg.ParseMode = "Markdown"

	sent, err := a.bot.Send(msg)
	if err != nil {
		// Markdown failed, retry without parse mode
		msg.ParseMode = ""
		sent, err = a.bot.Send(msg)
		if err != nil {
			log.Printf("[Agent] Failed to send reply to chat %d: %v", chatID, err)
			return
		}
	}

	// Save bot's own message to DB for context history
	botUser := a.bot.Self
	dbMsg := &database.Message{
		ChatID:           chatID,
		ChatType:         "group",
		MessageID:        sent.MessageID,
		ReplyToMessageID: replyToMsgID,
		FromUserID:       botUser.ID,
		FromUsername:     botUser.UserName,
		FromFirstName:    botUser.FirstName,
		Text:             text,
		IsProcessed:      true,
	}
	if err := a.db.SaveMessage(dbMsg); err != nil {
		log.Printf("[Agent] Failed to save bot reply to DB: %v", err)
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

// isSuperAdmin checks if a username matches the configured super admin
func (a *Agent) isSuperAdmin(username string) bool {
	sa := a.appConfig.SuperAdminUsername
	if sa == "" || username == "" {
		return false
	}
	return strings.EqualFold(username, sa)
}

// isAdminOrSuperAdmin checks if a user is a group admin or the super admin.
// Used for config/knowledge operations where super admin should bypass group admin checks.
func (a *Agent) isAdminOrSuperAdmin(chatID, userID int64, username string) bool {
	if a.isSuperAdmin(username) {
		return true
	}
	return a.isAdmin(chatID, userID)
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
	// Handle pre-checkout queries for Telegram Stars payments (must respond within 10s)
	if update.PreCheckoutQuery != nil {
		a.handlePreCheckoutQuery(update.PreCheckoutQuery)
		return
	}

	// Handle being added/removed from groups
	if update.MyChatMember != nil {
		a.handleMyChatMember(update.MyChatMember)
		return
	}

	if update.Message == nil {
		return
	}

	msg := update.Message

	// Handle successful payment messages (Telegram Stars)
	if msg.SuccessfulPayment != nil {
		a.handleSuccessfulPayment(msg)
		return
	}

	// Handle new chat members (welcome/onboarding)
	if msg.NewChatMembers != nil && len(msg.NewChatMembers) > 0 {
		a.handleNewChatMembers(msg)
		return
	}

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
			a.db.UpsertGroupMember(msg.Chat.ID, msg.From.ID, msg.From.UserName, msg.From.FirstName)
		}
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

// scheduledPostLoop runs every 60 seconds to check for due scheduled posts and execute them.
func (a *Agent) scheduledPostLoop(ctx context.Context) {
	// Initial delay to let the bot fully start up
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.processScheduledPosts(ctx)
		}
	}
}

// processScheduledPosts finds all due scheduled posts and generates/sends them.
func (a *Agent) processScheduledPosts(ctx context.Context) {
	duePosts, err := a.db.GetDueScheduledPosts()
	if err != nil {
		log.Printf("[Posts] Error getting due scheduled posts: %v", err)
		return
	}
	if len(duePosts) == 0 {
		return
	}

	for _, sp := range duePosts {
		// Verify the group's plan still allows scheduled posting
		effectivePlan := a.db.GetEffectivePlan(sp.ChatID)
		limits := plan.GetLimits(effectivePlan)
		if !limits.SchedulePost {
			log.Printf("[Posts] Group %d no longer on a plan that allows scheduled posts, deactivating", sp.ChatID)
			a.db.DeactivateScheduledPost(sp.ChatID)
			continue
		}

		log.Printf("[Posts] Generating scheduled post for group %d", sp.ChatID)

		if err := a.generateScheduledPost(ctx, sp.ChatID); err != nil {
			log.Printf("[Posts] Error generating scheduled post for group %d: %v", sp.ChatID, err)
			// Still advance the schedule so we don't retry immediately
		}

		// Advance to next scheduled time
		if err := a.db.AdvanceScheduledPost(sp.ChatID); err != nil {
			log.Printf("[Posts] Error advancing schedule for group %d: %v", sp.ChatID, err)
		}
	}
}

// engagementLoop runs every 5 minutes to check for inactive groups and post engagement content.
func (a *Agent) engagementLoop(ctx context.Context) {
	// Initial delay
	select {
	case <-ctx.Done():
		return
	case <-time.After(60 * time.Second):
	}

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.checkInactivityAndEngage(ctx)
		}
	}
}

// processBatch gets all unprocessed messages and sends them to the AI
func (a *Agent) processBatch(ctx context.Context) {
	// Prevent overlapping batch runs. If the previous batch is still processing,
	// skip this tick rather than risk double-processing the same messages.
	if !a.processMu.TryLock() {
		return
	}
	defer a.processMu.Unlock()

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

	// Process each chat's messages concurrently (max 5 concurrent chats)
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	for chatID, chatMsgs := range chatMessages {
		wg.Add(1)
		sem <- struct{}{}
		go func(cid int64, cmsgs []*database.Message) {
			defer wg.Done()
			defer func() { <-sem }()
			a.processChatMessages(ctx, cid, cmsgs)
		}(chatID, chatMsgs)
	}
	wg.Wait()
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
			if containsWholeWord(text, name) {
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

// containsWholeWord checks if text contains the word as a whole word (not as a substring of another word).
// Uses simple boundary check: the character before and after the match must not be a letter or digit.
func containsWholeWord(text, word string) bool {
	start := 0
	for {
		idx := strings.Index(text[start:], word)
		if idx == -1 {
			return false
		}
		absIdx := start + idx
		endIdx := absIdx + len(word)

		// Check left boundary: beginning of string or non-alphanumeric character
		leftOK := absIdx == 0 || !isAlphaNum(rune(text[absIdx-1]))
		// Check right boundary: end of string or non-alphanumeric character
		rightOK := endIdx == len(text) || !isAlphaNum(rune(text[endIdx]))

		if leftOK && rightOK {
			return true
		}
		start = absIdx + 1
	}
}

func isAlphaNum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
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
		ChatStyle: "friendly and helpful",
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

	// Derive effective plan from active subscription (overrides stored plan)
	effectivePlan := a.db.GetEffectivePlan(chatID)
	if effectivePlan != groupCfg.Plan {
		groupCfg.Plan = effectivePlan
		// Sync the derived plan back to the stored config
		if err := a.db.UpsertGroupConfig(groupCfg); err != nil {
			log.Printf("[Agent] Error syncing plan for chat %d: %v", chatID, err)
		}
	}

	// Enforce plan limits before calling the LLM
	limits := plan.GetLimits(groupCfg.Plan)

	monthlyUsage, err := a.db.GetMonthlyUsage(chatID)
	if err != nil {
		log.Printf("[Agent] Error getting monthly usage for chat %d, blocking as precaution: %v", chatID, err)
		ids := make([]int64, len(msgs))
		for i, m := range msgs {
			ids[i] = m.ID
		}
		a.db.MarkMessagesProcessed(ids, "usage check error - skipped")
		return
	}
	if monthlyUsage >= limits.MonthlyMessages {
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
		log.Printf("[Agent] Error getting minute usage for chat %d, blocking as precaution: %v", chatID, err)
		ids := make([]int64, len(msgs))
		for i, m := range msgs {
			ids[i] = m.ID
		}
		a.db.MarkMessagesProcessed(ids, "rate check error - skipped")
		return
	}
	if minuteUsage >= limits.PerMinute {
		log.Printf("[Agent] Chat %d hit rate limit (%d/%d per min, plan: %s)", chatID, minuteUsage, limits.PerMinute, groupCfg.Plan)
		ids := make([]int64, len(msgs))
		for i, m := range msgs {
			ids[i] = m.ID
		}
		a.db.MarkMessagesProcessed(ids, "rate limit reached")
		return
	}

	// Get the direct reply-to message for immediate context (if any new message is a reply)
	var replyContext []*database.Message
	seenReplyIDs := make(map[int]bool)
	for _, m := range msgs {
		if m.ReplyToMessageID > 0 && !seenReplyIDs[m.ReplyToMessageID] {
			seenReplyIDs[m.ReplyToMessageID] = true
			repliedMsg, err := a.db.GetMessageByTelegramID(chatID, m.ReplyToMessageID)
			if err == nil {
				replyContext = append(replyContext, repliedMsg)
			}
		}
	}

	// Fetch recent media messages (photos/animations) so the agent can "see" images
	// shared in the last few messages even if they aren't in the current batch.
	var recentMedia []*database.Message
	allMediaMsgs, _ := a.db.GetRecentMediaMessages(chatID, 5)
	// Deduplicate: exclude media messages already in the new batch or reply context
	newMsgIDs := make(map[int]bool)
	for _, m := range msgs {
		newMsgIDs[m.MessageID] = true
	}
	for _, m := range replyContext {
		newMsgIDs[m.MessageID] = true
	}
	for _, m := range allMediaMsgs {
		if !newMsgIDs[m.MessageID] {
			recentMedia = append(recentMedia, m)
		}
	}

	// Fetch group context
	groupCtx := a.fetchGroupContext(chatID)

	// Use the global default model
	apiKey := a.appConfig.OpenAIAPIKey
	if apiKey == "" {
		log.Printf("[Agent] No OpenAI API key configured")
		return
	}

	client := a.llmClient

	// Build system prompt and user messages
	systemPrompt := buildSystemPrompt(groupCfg, groupCtx, a.bot.Self.UserName, a.bot.Self.FirstName)

	// Resolve image URLs from Telegram for vision-compatible media
	mediaURLs := a.resolveMediaURLs(msgs, replyContext, recentMedia)

	userMessages := buildUserMessages(msgs, replyContext, recentMedia, mediaURLs)

	// Get available tools based on plan
	availableTools := tools.GetAvailableTools(limits)

	// Determine which message to reply to (last message in the triggering batch)
	replyToMsgID := msgs[len(msgs)-1].MessageID

	// Send "Thinking..." indicator
	thinkingMsgID := a.sendThinking(chatID)
	defer a.deleteThinking(chatID, thinkingMsgID)

	// Call the AI
	response, err := client.Chat(ctx, systemPrompt, userMessages, availableTools, groupCfg.VectorStoreID)
	if err != nil {
		log.Printf("[Agent] LLM error for chat %d: %v", chatID, err)
		return
	}

	// Tool execution loop
	executor := tools.NewExecutor(a.bot, a.db, chatID, a.appConfig.LangSearchAPIKey, a.appConfig.OpenAIAPIKey, limits)
	var allResults []string
	maxIterations := 5

	for i := 0; i < maxIterations; i++ {
		if len(response.ToolCalls) == 0 {
			break
		}

		// Update thinking message to show what the agent is doing
		if status := toolStatusText(response.ToolCalls); status != "" {
			a.updateThinking(chatID, thinkingMsgID, status)
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

	// Send the LLM's final text output as the bot's reply
	responseText := strings.TrimSpace(response.Text)
	if responseText != "" {
		a.sendReply(chatID, replyToMsgID, responseText)
		allResults = append(allResults, "reply: "+responseText)
	}

	// Log usage for this AI response
	if err := a.db.LogUsage(chatID); err != nil {
		log.Printf("[Agent] Error logging usage for chat %d: %v", chatID, err)
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

// toolStatusText returns a user-friendly status string for the current tool calls
func toolStatusText(toolCalls []llm.ToolCall) string {
	displayNames := map[string]string{
		"web_search":          "Searching the web...",
		"web_fetch":           "Reading a webpage...",
		"search_chat_history": "Searching chat history...",
		"send_poll":           "Creating poll...",
		"get_config":          "Reading configuration...",
		"set_config":          "Updating configuration...",
		"delete_message":      "Moderating...",
		"warn_user":           "Issuing warning...",
		"ban_user":            "Banning user...",
	}

	// If there's a single tool call, show its specific status
	if len(toolCalls) == 1 {
		if name, ok := displayNames[toolCalls[0].Name]; ok {
			return name
		}
		return "Working..."
	}

	// Multiple tool calls - show the most interesting one
	// Priority: web_search > web_fetch > others
	priority := []string{"web_search", "web_fetch", "search_chat_history", "delete_message", "ban_user", "send_poll", "set_config"}
	for _, p := range priority {
		for _, tc := range toolCalls {
			if tc.Name == p {
				return displayNames[p]
			}
		}
	}

	return "Working..."
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
func buildUserMessages(newMsgs []*database.Message, replyContext []*database.Message, recentMedia []*database.Message, mediaURLs map[int]string) []llm.InputMessage {
	var messages []llm.InputMessage

	if len(replyContext) > 0 {
		messages = append(messages,
			buildSectionMessages(replyContext, mediaURLs, "=== REFERENCED MESSAGES (messages being replied to) ===", false)...)
	}

	if len(recentMedia) > 0 {
		messages = append(messages,
			buildSectionMessages(recentMedia, mediaURLs, "=== RECENT IMAGES (recently shared photos/animations) ===", false)...)
	}

	newSectionMsgs := buildSectionMessages(newMsgs, mediaURLs, "=== NEW MESSAGES (you were mentioned or replied to) ===", true)
	messages = append(messages, newSectionMsgs...)

	if len(messages) > 0 {
		last := &messages[len(messages)-1]
		last.Text += "\n\nYou were mentioned or someone replied to your message. Decide what actions to take. You can call multiple tools. If you need context from earlier conversation, use the search_chat_history tool."
	}

	return messages
}

// buildSystemPrompt creates the system prompt from group config and group context
func buildSystemPrompt(cfg *database.GroupConfig, gc *GroupContext, botUsername, botFirstName string) string {
	var parts []string

	parts = append(parts, "You are an AI community manager bot for a Telegram group.")

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

	parts = append(parts, `## Behavior Guidelines
- When a user replies to your message, treat it as a continuation of the conversation and respond accordingly.
- You can call multiple tools in one response.
- You have access to get_config and set_config tools. Only group admins can change your configuration. If a non-admin tries to change settings, politely decline.`)

	parts = append(parts, `## Core Response Style
- Keep it SHORT. No walls of text.
- Talk like a sharp teammate, not a customer service bot. Be direct, skip filler.
- If you don't know, say so in one line. Don't pad ignorance with fluff.
- Match the group's energy. Casual group = casual tone. Technical group = precise answers.
- NEVER ask the user a question back. Give your best answer. If info is missing, state what you'd need but still give what you can.
- Drop the "Let me help you with that" template speak. Just help.
`)

	parts = append(parts, `## Tool Usage Rules
- Your text output IS your reply. It will be sent directly to the chat as a message. Just write your response naturally.
- You can also call tools (web_search, web_fetch, search_chat_history, send_poll, get_config, set_config) when needed. After tools run, you'll get their results and can write your final response.
- If you use tools, your final text output after all tool calls is what gets sent to the chat.
- You can use Markdown formatting in your responses.`)

	parts = append(parts, `## Knowledge Base (file_search)
- You have access to a file_search tool that searches uploaded knowledge documents.
- ONLY use file_search when someone asks a specific question that likely requires information from the knowledge base.
- Do NOT use file_search for casual messages, greetings, general chat, or questions you can answer from context alone.`)

	parts = append(parts, `## Moderation
- You have access to delete_message, warn_user, and ban_user tools for moderation.
- Use these to enforce group rules when you see clear violations.
- Always explain WHY you're taking moderation action.
- Prefer warning before banning unless the violation is severe (spam/scam/phishing).`)

	return strings.Join(parts, "\n\n")
}

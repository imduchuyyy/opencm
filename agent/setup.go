package agent

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/imduchuyyy/opencm/database"
	"github.com/imduchuyyy/opencm/llm"
)

// Setup steps
const (
	StepIdle            = "idle"
	StepSelectGroup     = "select_group"
	StepSystemPrompt    = "system_prompt"
	StepBio             = "bio"
	StepTopics          = "topics"
	StepMessageExamples = "message_examples"
	StepChatStyle       = "chat_style"
	StepModel           = "model"
	StepPermissions     = "permissions"
	StepKnowledgeFile   = "knowledge_file"
	StepKnowledgeURL    = "knowledge_url"
)

// Allowed file extensions for knowledge uploads
var allowedKnowledgeExts = map[string]bool{
	".pdf": true,
	".md":  true,
	".txt": true,
}

// send sends a message with Markdown, falling back to plain text on failure
func (a *Agent) send(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	if _, err := a.bot.Send(msg); err != nil {
		log.Printf("[Agent] Markdown send failed: %v, retrying plain", err)
		msg.ParseMode = ""
		if _, err := a.bot.Send(msg); err != nil {
			log.Printf("[Agent] Send failed: %v", err)
		}
	}
}

// getLLMClient returns an LLM client for vector store operations
func (a *Agent) getLLMClient() *llm.Client {
	return llm.NewClient(a.appConfig.OpenAIAPIKey, a.appConfig.DefaultModel)
}

// ensureVectorStore creates a vector store for a group if one doesn't exist yet
func (a *Agent) ensureVectorStore(ctx context.Context, chatID int64) (string, error) {
	cfg, err := a.db.GetGroupConfig(chatID)
	if err != nil {
		return "", fmt.Errorf("get group config: %w", err)
	}

	if cfg.VectorStoreID != "" {
		return cfg.VectorStoreID, nil
	}

	client := a.getLLMClient()
	name := fmt.Sprintf("opencm-group-%d", chatID)
	vsID, err := client.CreateVectorStore(ctx, name)
	if err != nil {
		return "", fmt.Errorf("create vector store: %w", err)
	}

	if err := a.db.UpdateGroupConfigField(chatID, "vector_store_id", vsID); err != nil {
		return "", fmt.Errorf("save vector store ID: %w", err)
	}

	log.Printf("[Agent] Created vector store for group %d: %s", chatID, vsID)
	return vsID, nil
}

// handlePrivateMessage processes DMs to the bot (for setup by group admins)
func (a *Agent) handlePrivateMessage(msg *tgbotapi.Message) {
	userID := msg.From.ID

	// Check if owner sent a document while in the knowledge file step
	if msg.Document != nil {
		state, err := a.db.GetSetupState(userID)
		if err == nil && state.Step == StepKnowledgeFile && state.ChatID != 0 {
			a.handleKnowledgeFile(msg, state.ChatID)
			return
		}
		a.send(msg.Chat.ID, "To upload a file as knowledge, first use /add_knowledge then send the file.")
		return
	}

	text := strings.TrimSpace(msg.Text)

	// Strip @botname suffix from commands
	if strings.HasPrefix(text, "/") {
		if idx := strings.Index(text, "@"); idx != -1 {
			parts := strings.SplitN(text, " ", 2)
			cmd := parts[0]
			cmd = cmd[:strings.Index(cmd, "@")]
			if len(parts) > 1 {
				text = cmd + " " + parts[1]
			} else {
				text = cmd
			}
		}
		a.handleSetupCommand(msg, text)
		return
	}

	// Handle setup flow input
	a.handleSetupInput(msg, text)
}

// handleSetupCommand handles /commands from a user in DM
func (a *Agent) handleSetupCommand(msg *tgbotapi.Message, text string) {
	chatID := msg.Chat.ID
	userID := msg.From.ID

	switch {
	case text == "/start":
		a.sendSetupWelcome(chatID)

	case text == "/setup":
		a.startGroupSelection(chatID, userID)

	case text == "/config":
		a.sendCurrentConfig(chatID, userID)

	case text == "/set_system_prompt":
		a.startConfigStep(chatID, userID, StepSystemPrompt,
			"Send me the system prompt for the bot in this group.\n\nThis is the core instruction that tells the AI how to behave. Example:\n\n\"You are a helpful community manager for a crypto trading group. Keep discussions on topic, help newcomers, and moderate spam.\"")

	case text == "/set_bio":
		a.startConfigStep(chatID, userID, StepBio,
			"Send me the bot's bio/description.\n\nThis helps the AI understand its identity. Example:\n\n\"CryptoBot - Your friendly crypto community assistant, helping since 2024\"")

	case text == "/set_topics":
		a.startConfigStep(chatID, userID, StepTopics,
			"Send me the topics the bot should cover (comma-separated).\n\nExample:\n\"cryptocurrency, trading, DeFi, market analysis, technical analysis\"")

	case text == "/set_examples":
		a.startConfigStep(chatID, userID, StepMessageExamples,
			"Send me example messages that show the bot's style.\n\nPut each example on a new line. Example:\n\n\"Welcome aboard! Feel free to ask anything about trading.\"\n\"Great question! Here's what I think about BTC...\"\n\"Please keep the discussion civil, folks.\"")

	case text == "/set_style":
		a.startConfigStep(chatID, userID, StepChatStyle,
			"Describe the chat style for your bot.\n\nExample:\n\"Friendly and casual, uses occasional emojis, speaks like a knowledgeable friend rather than a formal assistant\"")

	case text == "/set_model":
		a.startConfigStep(chatID, userID, StepModel,
			"Send me the OpenAI model name to use.\n\nExamples: gpt-4o, gpt-4o-mini, gpt-4.1, gpt-4.1-mini")

	case text == "/set_permissions":
		a.sendPermissionsMenu(chatID, userID)

	case strings.HasPrefix(text, "/perm_"):
		a.handlePermissionToggle(msg, text)

	case text == "/add_knowledge":
		a.startConfigStep(chatID, userID, StepKnowledgeFile,
			"Send me a file to add to the knowledge base.\n\nSupported formats: PDF, Markdown (.md), Text (.txt)\n\nThe file will be uploaded to the AI knowledge store so the bot can reference it when answering questions.\n\nSend /cancel to abort.")

	case text == "/add_url":
		a.startConfigStep(chatID, userID, StepKnowledgeURL,
			"Send me a URL to fetch and store as knowledge.\n\nI'll download the page content and upload it to the knowledge base.")

	case text == "/list_knowledge":
		a.sendKnowledgeList(chatID, userID)

	case strings.HasPrefix(text, "/delete_knowledge"):
		a.handleDeleteKnowledge(chatID, userID, text)

	case text == "/groups":
		a.sendGroupsList(chatID, userID)

	case text == "/help":
		a.sendHelp(chatID)

	case text == "/cancel":
		a.db.SetSetupState(userID, 0, StepIdle)
		a.send(chatID, "Cancelled. Use /setup to select a group and configure.")

	default:
		a.send(chatID, "Unknown command. Use /help to see available commands.")
	}
}

// startGroupSelection presents the user with a list of groups they admin
func (a *Agent) startGroupSelection(chatID, userID int64) {
	adminGroups := a.getAdminGroups(userID)
	if len(adminGroups) == 0 {
		a.send(chatID, "You are not an admin of any groups I'm in.\n\nPlease add me to a group first and make sure you are an admin there.")
		return
	}

	var lines []string
	for i, g := range adminGroups {
		lines = append(lines, fmt.Sprintf("%d. %s (ID: %d)", i+1, g.ChatTitle, g.ChatID))
	}

	a.db.SetSetupState(userID, 0, StepSelectGroup)
	a.send(chatID, "Select a group to configure:\n\n"+strings.Join(lines, "\n")+
		"\n\nSend the number of the group you want to configure:")
}

// startConfigStep verifies the user has a group selected and sets the step
func (a *Agent) startConfigStep(chatID, userID int64, step, prompt string) {
	state, err := a.db.GetSetupState(userID)
	if err != nil || state.ChatID == 0 {
		// No group selected - start selection first
		a.send(chatID, "Please select a group first. Use /setup to choose a group.")
		return
	}

	// Verify user is still admin of this group
	if !a.isAdmin(state.ChatID, userID) {
		a.db.SetSetupState(userID, 0, StepIdle)
		a.send(chatID, "You are no longer an admin of the selected group. Use /setup to select a new group.")
		return
	}

	a.db.SetSetupState(userID, state.ChatID, step)
	a.send(chatID, prompt)
}

// handleSetupInput handles non-command text during setup flow
func (a *Agent) handleSetupInput(msg *tgbotapi.Message, text string) {
	chatID := msg.Chat.ID
	userID := msg.From.ID

	state, err := a.db.GetSetupState(userID)
	if err != nil {
		log.Printf("[Agent] Error getting setup state: %v", err)
		return
	}

	if state.Step == StepIdle {
		a.send(chatID, "Use /setup to configure bot settings for your group, or /help for available commands.")
		return
	}

	// Handle group selection
	if state.Step == StepSelectGroup {
		a.handleGroupSelection(chatID, userID, text)
		return
	}

	// Handle knowledge steps
	switch state.Step {
	case StepKnowledgeFile:
		a.send(chatID, "Please send a file (PDF, .md, or .txt).\n\nSend /cancel to abort.")
		return
	case StepKnowledgeURL:
		a.handleKnowledgeURL(chatID, userID, state.ChatID, text)
		return
	}

	// Map step to database field
	fieldMap := map[string]string{
		StepSystemPrompt:    "system_prompt",
		StepBio:             "bio",
		StepTopics:          "topics",
		StepMessageExamples: "message_examples",
		StepChatStyle:       "chat_style",
		StepModel:           "model",
	}

	field, ok := fieldMap[state.Step]
	if !ok {
		a.db.SetSetupState(userID, 0, StepIdle)
		return
	}

	groupChatID := state.ChatID
	if groupChatID == 0 {
		a.send(chatID, "No group selected. Use /setup to select a group first.")
		return
	}

	// Ensure group config exists
	a.getOrCreateGroupConfig(groupChatID)

	// Update the field
	if err := a.db.UpdateGroupConfigField(groupChatID, field, text); err != nil {
		log.Printf("[Agent] Error updating config: %v", err)
		a.send(chatID, "Error saving configuration. Please try again.")
		return
	}

	a.db.SetSetupState(userID, groupChatID, StepIdle)
	a.send(chatID, fmt.Sprintf("%s updated successfully!\n\nUse /config to see current configuration or /setup to continue configuring.", stepDisplayName(state.Step)))
}

// handleGroupSelection processes the user's group choice
func (a *Agent) handleGroupSelection(chatID, userID int64, text string) {
	adminGroups := a.getAdminGroups(userID)
	if len(adminGroups) == 0 {
		a.send(chatID, "No groups found. Add me to a group first.")
		a.db.SetSetupState(userID, 0, StepIdle)
		return
	}

	num, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || num < 1 || num > len(adminGroups) {
		a.send(chatID, fmt.Sprintf("Please send a number between 1 and %d.", len(adminGroups)))
		return
	}

	selectedGroup := adminGroups[num-1]

	// Ensure group config exists
	a.getOrCreateGroupConfig(selectedGroup.ChatID)

	a.db.SetSetupState(userID, selectedGroup.ChatID, StepIdle)
	a.send(chatID, fmt.Sprintf("Selected group: %s\n\nYou can now configure the bot for this group. Use /setup to see the menu, or use any /set_* command directly.", selectedGroup.ChatTitle))
	a.sendSetupMenu(chatID)
}

func (a *Agent) sendSetupWelcome(chatID int64) {
	text := fmt.Sprintf("Welcome! I'm @%s, your AI community manager bot.\n\nTo get started:\n1. Add me to your Telegram group\n2. Make me an admin\n3. Use /setup here to configure my behavior for your group\n\nI manage each group independently with its own personality, knowledge, and permissions.\n\nUse /help to see all available commands.", a.bot.Self.UserName)
	a.send(chatID, text)
}

func (a *Agent) sendSetupMenu(chatID int64) {
	text := "Bot Setup Menu\n\nConfigure your bot's personality and behavior:\n\n" +
		"/set_system_prompt - Core AI instructions\n" +
		"/set_bio - Bot identity/description\n" +
		"/set_topics - Topics to cover\n" +
		"/set_examples - Example messages for style\n" +
		"/set_style - Chat tone and style\n" +
		"/set_model - OpenAI model (gpt-4o, gpt-4o-mini, etc.)\n" +
		"/set_permissions - Toggle bot permissions\n\n" +
		"Knowledge Base:\n" +
		"/add_knowledge - Upload a file (PDF, .md, .txt)\n" +
		"/add_url - Add knowledge from a URL\n" +
		"/list_knowledge - View all knowledge entries\n\n" +
		"Tip: Reply /save to any message in a group to save it as knowledge.\n\n" +
		"/config - View current configuration\n" +
		"/groups - View groups I'm in where you're admin\n" +
		"/setup - Switch to a different group"
	a.send(chatID, text)
}

// getSelectedGroupChatID returns the group chat ID from setup state, or 0
func (a *Agent) getSelectedGroupChatID(userID int64) int64 {
	state, err := a.db.GetSetupState(userID)
	if err != nil || state.ChatID == 0 {
		return 0
	}
	return state.ChatID
}

func (a *Agent) sendCurrentConfig(chatID, userID int64) {
	groupChatID := a.getSelectedGroupChatID(userID)
	if groupChatID == 0 {
		a.send(chatID, "No group selected. Use /setup to select a group first.")
		return
	}

	cfg, err := a.db.GetGroupConfig(groupChatID)
	if err != nil {
		a.send(chatID, "No configuration yet. Use /setup to get started.")
		return
	}

	truncate := func(s string, max int) string {
		if len(s) > max {
			return s[:max] + "..."
		}
		if s == "" {
			return "(not set)"
		}
		return s
	}

	vectorStatus := "(not created)"
	if cfg.VectorStoreID != "" {
		vectorStatus = cfg.VectorStoreID
	}

	// Get group title for display
	groupTitle := fmt.Sprintf("Group %d", groupChatID)
	groups, _ := a.db.GetActiveGroups()
	for _, g := range groups {
		if g.ChatID == groupChatID {
			groupTitle = g.ChatTitle
			break
		}
	}

	text := fmt.Sprintf("Configuration for: %s\n\n"+
		"System Prompt: %s\n\n"+
		"Bio: %s\n\n"+
		"Topics: %s\n\n"+
		"Chat Style: %s\n\n"+
		"Message Examples: %s\n\n"+
		"Model: %s\n"+
		"Vector Store: %s\n\n"+
		"Permissions:\n"+
		"  Reply: %s\n"+
		"  Ban: %s\n"+
		"  Pin: %s\n"+
		"  Poll: %s\n"+
		"  Delete: %s",
		groupTitle,
		truncate(cfg.SystemPrompt, 200),
		truncate(cfg.Bio, 200),
		truncate(cfg.Topics, 200),
		truncate(cfg.ChatStyle, 200),
		truncate(cfg.MessageExamples, 200),
		cfg.Model,
		vectorStatus,
		boolEmoji(cfg.CanReply),
		boolEmoji(cfg.CanBan),
		boolEmoji(cfg.CanPin),
		boolEmoji(cfg.CanPoll),
		boolEmoji(cfg.CanDelete),
	)
	a.send(chatID, text)
}

func (a *Agent) sendPermissionsMenu(chatID, userID int64) {
	groupChatID := a.getSelectedGroupChatID(userID)
	if groupChatID == 0 {
		a.send(chatID, "No group selected. Use /setup to select a group first.")
		return
	}

	cfg, err := a.db.GetGroupConfig(groupChatID)
	if err != nil {
		a.send(chatID, "Please set up the group first with /setup")
		return
	}

	text := fmt.Sprintf("Bot Permissions\n\n"+
		"Toggle permissions by sending the command:\n\n"+
		"/perm_reply - Reply to messages [%s]\n"+
		"/perm_ban - Ban/mute members [%s]\n"+
		"/perm_pin - Pin messages [%s]\n"+
		"/perm_poll - Create polls [%s]\n"+
		"/perm_delete - Delete messages [%s]\n\n"+
		"Note: The bot also needs the corresponding Telegram admin permissions in the group.",
		boolEmoji(cfg.CanReply),
		boolEmoji(cfg.CanBan),
		boolEmoji(cfg.CanPin),
		boolEmoji(cfg.CanPoll),
		boolEmoji(cfg.CanDelete),
	)
	a.send(chatID, text)
}

func (a *Agent) handlePermissionToggle(msg *tgbotapi.Message, text string) {
	chatID := msg.Chat.ID
	userID := msg.From.ID

	groupChatID := a.getSelectedGroupChatID(userID)
	if groupChatID == 0 {
		a.send(chatID, "No group selected. Use /setup to select a group first.")
		return
	}

	if !a.isAdmin(groupChatID, userID) {
		a.send(chatID, "You are no longer an admin of the selected group.")
		return
	}

	cfg, err := a.db.GetGroupConfig(groupChatID)
	if err != nil {
		a.send(chatID, "Please set up the group first with /setup")
		return
	}

	permMap := map[string]struct {
		field string
		value bool
	}{
		"/perm_reply":  {"can_reply", !cfg.CanReply},
		"/perm_ban":    {"can_ban", !cfg.CanBan},
		"/perm_pin":    {"can_pin", !cfg.CanPin},
		"/perm_poll":   {"can_poll", !cfg.CanPoll},
		"/perm_delete": {"can_delete", !cfg.CanDelete},
	}

	perm, ok := permMap[text]
	if !ok {
		a.send(chatID, "Unknown permission.")
		return
	}

	if err := a.db.UpdateGroupConfigBool(groupChatID, perm.field, perm.value); err != nil {
		a.send(chatID, "Error updating permission.")
		return
	}

	status := "disabled"
	if perm.value {
		status = "enabled"
	}

	a.send(chatID, fmt.Sprintf("Permission %s %s.", perm.field, status))
	a.sendPermissionsMenu(chatID, userID)
}

func (a *Agent) sendGroupsList(chatID, userID int64) {
	adminGroups := a.getAdminGroups(userID)
	if len(adminGroups) == 0 {
		a.send(chatID, "I'm not in any groups where you're an admin.\n\nAdd me to a group and make sure you're an admin there!")
		return
	}

	var lines []string
	for _, g := range adminGroups {
		lines = append(lines, fmt.Sprintf("- %s (ID: %d, Type: %s)", g.ChatTitle, g.ChatID, g.ChatType))
	}

	// Show which group is currently selected
	selectedID := a.getSelectedGroupChatID(userID)
	selectedNote := ""
	if selectedID != 0 {
		for _, g := range adminGroups {
			if g.ChatID == selectedID {
				selectedNote = fmt.Sprintf("\n\nCurrently configuring: %s", g.ChatTitle)
				break
			}
		}
	}

	a.send(chatID, "Groups where you're admin:\n\n"+strings.Join(lines, "\n")+selectedNote)
}

func (a *Agent) sendHelp(chatID int64) {
	text := "Available Commands\n\n" +
		"Setup:\n" +
		"/setup - Select a group and configure\n" +
		"/config - View current config\n\n" +
		"Configuration (for selected group):\n" +
		"/set_system_prompt - Core AI instructions\n" +
		"/set_bio - Bot identity\n" +
		"/set_topics - Topics to cover\n" +
		"/set_examples - Example messages\n" +
		"/set_style - Chat style\n" +
		"/set_model - OpenAI model\n" +
		"/set_permissions - Toggle permissions\n\n" +
		"Knowledge Base:\n" +
		"/add_knowledge - Upload a file (PDF, .md, .txt)\n" +
		"/add_url - Add knowledge from URL\n" +
		"/list_knowledge - List all knowledge\n" +
		"/delete_knowledge <id> - Delete a knowledge entry\n\n" +
		"In groups (admin only):\n" +
		"Reply /save to any message to save it as knowledge\n\n" +
		"Info:\n" +
		"/groups - View your admin groups\n" +
		"/help - Show this message"
	a.send(chatID, text)
}

func boolEmoji(b bool) string {
	if b {
		return "ON"
	}
	return "OFF"
}

func stepDisplayName(step string) string {
	names := map[string]string{
		StepSystemPrompt:    "System Prompt",
		StepBio:             "Bio",
		StepTopics:          "Topics",
		StepMessageExamples: "Message Examples",
		StepChatStyle:       "Chat Style",
		StepModel:           "Model",
	}
	if name, ok := names[step]; ok {
		return name
	}
	return step
}

// ----- Knowledge handlers -----

// handleKnowledgeFile handles a document upload during the /add_knowledge flow
func (a *Agent) handleKnowledgeFile(msg *tgbotapi.Message, groupChatID int64) {
	chatID := msg.Chat.ID
	userID := msg.From.ID
	doc := msg.Document

	ext := strings.ToLower(filepath.Ext(doc.FileName))
	if !allowedKnowledgeExts[ext] {
		a.send(chatID, fmt.Sprintf("Unsupported file type: %s\n\nPlease send a PDF, Markdown (.md), or Text (.txt) file.", ext))
		return
	}

	if doc.FileSize > 20*1024*1024 {
		a.send(chatID, "File is too large (max 20MB). Please send a smaller file.")
		return
	}

	a.send(chatID, fmt.Sprintf("Downloading %s...", doc.FileName))

	fileConfig := tgbotapi.FileConfig{FileID: doc.FileID}
	tgFile, err := a.bot.GetFile(fileConfig)
	if err != nil {
		log.Printf("[Agent] Error getting file from Telegram: %v", err)
		a.send(chatID, "Error downloading file from Telegram. Please try again.")
		return
	}

	fileURL := tgFile.Link(a.bot.Token)

	resp, err := http.Get(fileURL)
	if err != nil {
		log.Printf("[Agent] Error downloading file: %v", err)
		a.send(chatID, "Error downloading file. Please try again.")
		return
	}
	defer resp.Body.Close()

	fileBytes, err := io.ReadAll(io.LimitReader(resp.Body, 20*1024*1024))
	if err != nil {
		log.Printf("[Agent] Error reading file content: %v", err)
		a.send(chatID, "Error reading file content. Please try again.")
		return
	}

	a.send(chatID, "Uploading to knowledge base...")

	ctx := context.Background()
	vsID, err := a.ensureVectorStore(ctx, groupChatID)
	if err != nil {
		log.Printf("[Agent] Error ensuring vector store: %v", err)
		a.send(chatID, "Error creating knowledge store. Please try again.")
		a.db.SetSetupState(userID, groupChatID, StepIdle)
		return
	}

	client := a.getLLMClient()
	reader := strings.NewReader(string(fileBytes))
	fileID, err := client.UploadFileToVectorStore(ctx, vsID, doc.FileName, reader)
	if err != nil {
		log.Printf("[Agent] Error uploading file to OpenAI: %v", err)
		a.send(chatID, "Error uploading file to knowledge base. Please try again.")
		a.db.SetSetupState(userID, groupChatID, StepIdle)
		return
	}

	preview := buildFilePreview(fileBytes, ext)

	k := &database.Knowledge{
		ChatID:       groupChatID,
		SourceType:   "file",
		Title:        doc.FileName,
		Content:      preview,
		OpenAIFileID: fileID,
		AddedBy:      userID,
	}
	if err := a.db.AddKnowledge(k); err != nil {
		log.Printf("[Agent] Error adding knowledge to DB: %v", err)
		a.send(chatID, "File uploaded to OpenAI but failed to save local record.")
		a.db.SetSetupState(userID, groupChatID, StepIdle)
		return
	}

	a.db.SetSetupState(userID, groupChatID, StepIdle)
	a.send(chatID, fmt.Sprintf("File uploaded to knowledge base! (ID: %d)\n\nFilename: %s\nSize: %s\n\nUse /list_knowledge to see all entries or /add_knowledge to upload more.",
		k.ID, doc.FileName, formatFileSize(int64(len(fileBytes)))))
}

func buildFilePreview(data []byte, ext string) string {
	switch ext {
	case ".txt", ".md":
		content := string(data)
		return truncateStr(content, 500)
	case ".pdf":
		return "(PDF file - content indexed in OpenAI)"
	default:
		return "(file content indexed in OpenAI)"
	}
}

func formatFileSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
}

func (a *Agent) handleKnowledgeURL(chatID, userID, groupChatID int64, url string) {
	if !strings.HasPrefix(url, "http") {
		url = "https://" + url
	}

	a.send(chatID, "Fetching content from URL...")

	httpClient := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		a.send(chatID, "Invalid URL. Please try again with /add_url")
		a.db.SetSetupState(userID, groupChatID, StepIdle)
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; OpenCM Bot/1.0)")

	resp, err := httpClient.Do(req)
	if err != nil {
		a.send(chatID, fmt.Sprintf("Failed to fetch URL: %v\n\nTry again with /add_url", err))
		a.db.SetSetupState(userID, groupChatID, StepIdle)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024))
	if err != nil {
		a.send(chatID, "Failed to read URL content. Try again with /add_url")
		a.db.SetSetupState(userID, groupChatID, StepIdle)
		return
	}

	content := stripHTMLTags(string(body))
	content = strings.Join(strings.Fields(content), " ")
	if len(content) > 50000 {
		content = content[:50000]
	}

	if strings.TrimSpace(content) == "" {
		a.send(chatID, "Could not extract text from that URL. Try a different URL or use /add_knowledge to upload a file instead.")
		a.db.SetSetupState(userID, groupChatID, StepIdle)
		return
	}

	a.send(chatID, "Uploading to knowledge base...")

	ctx := context.Background()
	vsID, err := a.ensureVectorStore(ctx, groupChatID)
	if err != nil {
		log.Printf("[Agent] Error ensuring vector store: %v", err)
		a.send(chatID, "Error creating knowledge store. Please try again.")
		a.db.SetSetupState(userID, groupChatID, StepIdle)
		return
	}

	client := a.getLLMClient()
	fileID, err := client.UploadTextAsFile(ctx, vsID, url, content)
	if err != nil {
		log.Printf("[Agent] Error uploading URL knowledge: %v", err)
		a.send(chatID, "Error uploading knowledge. Please try again.")
		a.db.SetSetupState(userID, groupChatID, StepIdle)
		return
	}

	k := &database.Knowledge{
		ChatID:       groupChatID,
		SourceType:   "url",
		Title:        url,
		Content:      truncateStr(content, 500),
		OpenAIFileID: fileID,
		AddedBy:      userID,
	}
	if err := a.db.AddKnowledge(k); err != nil {
		log.Printf("[Agent] Error adding URL knowledge to DB: %v", err)
		a.send(chatID, "Error saving knowledge record.")
		a.db.SetSetupState(userID, groupChatID, StepIdle)
		return
	}

	a.db.SetSetupState(userID, groupChatID, StepIdle)
	a.send(chatID, fmt.Sprintf("Knowledge from URL added! (ID: %d)\n\nSource: %s\nContent preview: %s\n\nUse /list_knowledge to see all entries.",
		k.ID, url, truncateStr(content, 200)))
}

func (a *Agent) sendKnowledgeList(chatID, userID int64) {
	groupChatID := a.getSelectedGroupChatID(userID)
	if groupChatID == 0 {
		a.send(chatID, "No group selected. Use /setup to select a group first.")
		return
	}

	items, err := a.db.ListKnowledge(groupChatID)
	if err != nil || len(items) == 0 {
		a.send(chatID, "No knowledge entries yet.\n\nUse /add_knowledge to upload a file or /add_url to add from a URL.")
		return
	}

	var lines []string
	for _, k := range items {
		lines = append(lines, fmt.Sprintf("[%d] [%s] %s\n    %s",
			k.ID, k.SourceType, k.Title, truncateStr(k.Content, 80)))
	}

	text := fmt.Sprintf("Knowledge Base (%d entries)\n\n%s\n\nTo delete: /delete_knowledge <id>",
		len(items), strings.Join(lines, "\n\n"))
	a.send(chatID, text)
}

func (a *Agent) handleDeleteKnowledge(chatID, userID int64, text string) {
	groupChatID := a.getSelectedGroupChatID(userID)
	if groupChatID == 0 {
		a.send(chatID, "No group selected. Use /setup to select a group first.")
		return
	}

	parts := strings.Fields(text)
	if len(parts) < 2 {
		a.send(chatID, "Usage: /delete_knowledge <id>\n\nUse /list_knowledge to see IDs.")
		return
	}

	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		a.send(chatID, "Invalid ID. Use /list_knowledge to see valid IDs.")
		return
	}

	k, err := a.db.GetKnowledge(id)
	if err != nil || k.ChatID != groupChatID {
		a.send(chatID, "Knowledge entry not found.")
		return
	}

	// Delete from OpenAI vector store if file ID exists
	if k.OpenAIFileID != "" {
		cfg, err := a.db.GetGroupConfig(groupChatID)
		if err == nil && cfg.VectorStoreID != "" {
			ctx := context.Background()
			client := a.getLLMClient()
			if err := client.DeleteVectorStoreFile(ctx, cfg.VectorStoreID, k.OpenAIFileID); err != nil {
				log.Printf("[Agent] Error deleting file from vector store: %v", err)
			}
		}
	}

	if err := a.db.DeleteKnowledge(id, groupChatID); err != nil {
		a.send(chatID, "Error deleting knowledge entry.")
		return
	}

	a.send(chatID, fmt.Sprintf("Knowledge entry %d deleted (\"%s\").", id, truncateStr(k.Title, 50)))
}

// handleSaveCommand handles /save in group chats (reply to a message to save as knowledge)
func (a *Agent) handleSaveCommand(msg *tgbotapi.Message) {
	if msg.ReplyToMessage == nil || msg.ReplyToMessage.Text == "" {
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Reply to a message with /save to save it as knowledge.")
		reply.ReplyToMessageID = msg.MessageID
		a.bot.Send(reply)
		return
	}

	// Check if sender is an admin of this group
	if !a.isAdmin(msg.Chat.ID, msg.From.ID) {
		return // Silently ignore non-admin
	}

	savedMsg := msg.ReplyToMessage
	title := fmt.Sprintf("Chat message from %s", savedMsg.From.FirstName)
	content := savedMsg.Text

	ctx := context.Background()
	vsID, err := a.ensureVectorStore(ctx, msg.Chat.ID)
	if err != nil {
		log.Printf("[Agent] Error ensuring vector store for /save: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Error saving to knowledge base.")
		reply.ReplyToMessageID = msg.MessageID
		a.bot.Send(reply)
		return
	}

	client := a.getLLMClient()
	fileID, err := client.UploadTextAsFile(ctx, vsID, title, content)
	if err != nil {
		log.Printf("[Agent] Error uploading saved message: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Error uploading to knowledge base.")
		reply.ReplyToMessageID = msg.MessageID
		a.bot.Send(reply)
		return
	}

	k := &database.Knowledge{
		ChatID:       msg.Chat.ID,
		SourceType:   "chat",
		Title:        title,
		Content:      truncateStr(content, 500),
		OpenAIFileID: fileID,
		AddedBy:      msg.From.ID,
	}
	if err := a.db.AddKnowledge(k); err != nil {
		log.Printf("[Agent] Error saving chat knowledge: %v", err)
		return
	}

	reply := tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("Saved to knowledge base (ID: %d)", k.ID))
	reply.ReplyToMessageID = msg.MessageID
	a.bot.Send(reply)
}

// ----- Helpers -----

func stripHTMLTags(s string) string {
	var result strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			result.WriteRune(' ')
			continue
		}
		if !inTag {
			result.WriteRune(r)
		}
	}
	return result.String()
}

func truncateStr(s string, max int) string {
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

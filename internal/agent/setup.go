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
	"github.com/imduchuyyy/opencm/internal/database"
	"github.com/imduchuyyy/opencm/internal/llm"
)

// Setup steps
const (
	StepIdle            = "idle"
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
		log.Printf("[Agent %d] Markdown send failed: %v, retrying plain", a.botID, err)
		msg.ParseMode = ""
		if _, err := a.bot.Send(msg); err != nil {
			log.Printf("[Agent %d] Send failed: %v", a.botID, err)
		}
	}
}

// getLLMClient returns an LLM client for vector store operations
func (a *Agent) getLLMClient() *llm.Client {
	cfg, err := a.db.GetAgentConfig(a.botID)
	if err != nil {
		return llm.NewClient(a.appConfig.OpenAIAPIKey, a.appConfig.DefaultModel)
	}
	model := cfg.Model
	if model == "" {
		model = a.appConfig.DefaultModel
	}
	return llm.NewClient(a.appConfig.OpenAIAPIKey, model)
}

// ensureVectorStore creates a vector store for this agent if one doesn't exist yet
func (a *Agent) ensureVectorStore(ctx context.Context) (string, error) {
	cfg, err := a.db.GetAgentConfig(a.botID)
	if err != nil {
		return "", fmt.Errorf("get agent config: %w", err)
	}

	if cfg.VectorStoreID != "" {
		return cfg.VectorStoreID, nil
	}

	// Create a new vector store
	client := a.getLLMClient()
	name := fmt.Sprintf("opencm-agent-%d", a.botID)
	vsID, err := client.CreateVectorStore(ctx, name)
	if err != nil {
		return "", fmt.Errorf("create vector store: %w", err)
	}

	// Save the vector store ID
	if err := a.db.UpdateAgentConfigField(a.botID, "vector_store_id", vsID); err != nil {
		return "", fmt.Errorf("save vector store ID: %w", err)
	}

	log.Printf("[Agent %d] Created vector store: %s", a.botID, vsID)
	return vsID, nil
}

// handlePrivateMessage processes DMs to the agent bot (for setup by the owner)
func (a *Agent) handlePrivateMessage(msg *tgbotapi.Message) {
	// Check if this is the bot owner
	botInfo, err := a.db.GetBotByBotID(a.botID)
	if err != nil {
		log.Printf("[Agent %d] Error getting bot info: %v", a.botID, err)
		return
	}

	log.Printf("[Agent %d] Private message from user %d, owner is %d", a.botID, msg.From.ID, botInfo.OwnerID)

	if msg.From.ID != botInfo.OwnerID {
		// Non-owner messaging the bot directly
		a.send(msg.Chat.ID, "I'm a community manager bot. Add me to your group to get started!")
		return
	}

	// Check if owner sent a document while in the knowledge file step
	if msg.Document != nil {
		step, err := a.db.GetSetupState(a.botID, msg.From.ID)
		if err == nil && step == StepKnowledgeFile {
			a.handleKnowledgeFile(msg)
			return
		}
		// Document sent outside of knowledge flow - ignore or hint
		a.send(msg.Chat.ID, "To upload a file as knowledge, first use /add_knowledge then send the file.")
		return
	}

	// Owner is messaging - handle setup commands
	text := strings.TrimSpace(msg.Text)

	// Strip @botname suffix from commands (e.g. /setup@mybotname -> /setup)
	if strings.HasPrefix(text, "/") {
		if idx := strings.Index(text, "@"); idx != -1 {
			// Keep only the command part before @ and anything after a space
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

// handleSetupCommand handles /commands from the owner
func (a *Agent) handleSetupCommand(msg *tgbotapi.Message, text string) {
	chatID := msg.Chat.ID
	log.Printf("[Agent %d] Handling setup command: %q", a.botID, text)

	switch {
	case text == "/start":
		a.sendSetupWelcome(chatID)

	case text == "/setup":
		a.sendSetupMenu(chatID)

	case text == "/config":
		a.sendCurrentConfig(chatID)

	case text == "/set_system_prompt":
		a.db.SetSetupState(a.botID, msg.From.ID, StepSystemPrompt)
		a.send(chatID, "Send me the system prompt for your bot.\n\nThis is the core instruction that tells the AI how to behave. Example:\n\n\"You are a helpful community manager for a crypto trading group. Keep discussions on topic, help newcomers, and moderate spam.\"")

	case text == "/set_bio":
		a.db.SetSetupState(a.botID, msg.From.ID, StepBio)
		a.send(chatID, "Send me the bot's bio/description.\n\nThis helps the AI understand its identity. Example:\n\n\"CryptoBot - Your friendly crypto community assistant, helping since 2024\"")

	case text == "/set_topics":
		a.db.SetSetupState(a.botID, msg.From.ID, StepTopics)
		a.send(chatID, "Send me the topics the bot should cover (comma-separated).\n\nExample:\n\"cryptocurrency, trading, DeFi, market analysis, technical analysis\"")

	case text == "/set_examples":
		a.db.SetSetupState(a.botID, msg.From.ID, StepMessageExamples)
		a.send(chatID, "Send me example messages that show the bot's style.\n\nPut each example on a new line. Example:\n\n\"Welcome aboard! Feel free to ask anything about trading.\"\n\"Great question! Here's what I think about BTC...\"\n\"Please keep the discussion civil, folks.\"")

	case text == "/set_style":
		a.db.SetSetupState(a.botID, msg.From.ID, StepChatStyle)
		a.send(chatID, "Describe the chat style for your bot.\n\nExample:\n\"Friendly and casual, uses occasional emojis, speaks like a knowledgeable friend rather than a formal assistant\"")

	case text == "/set_model":
		a.db.SetSetupState(a.botID, msg.From.ID, StepModel)
		a.send(chatID, "Send me the OpenAI model name to use.\n\nExamples: gpt-4o, gpt-4o-mini, gpt-4.1, gpt-4.1-mini")

	case text == "/set_permissions":
		a.sendPermissionsMenu(chatID)

	case strings.HasPrefix(text, "/perm_"):
		a.handlePermissionToggle(msg, text)

	case text == "/add_knowledge":
		a.db.SetSetupState(a.botID, msg.From.ID, StepKnowledgeFile)
		a.send(chatID, "Send me a file to add to the knowledge base.\n\nSupported formats: PDF, Markdown (.md), Text (.txt)\n\nThe file will be uploaded to the AI knowledge store so the bot can reference it when answering questions.\n\nSend /cancel to abort.")

	case text == "/add_url":
		a.db.SetSetupState(a.botID, msg.From.ID, StepKnowledgeURL)
		a.send(chatID, "Send me a URL to fetch and store as knowledge.\n\nI'll download the page content and upload it to the knowledge base.")

	case text == "/list_knowledge":
		a.sendKnowledgeList(chatID)

	case strings.HasPrefix(text, "/delete_knowledge"):
		a.handleDeleteKnowledge(chatID, text)

	case text == "/groups":
		a.sendGroupsList(chatID)

	case text == "/help":
		a.sendHelp(chatID)

	case text == "/cancel":
		a.db.SetSetupState(a.botID, msg.From.ID, StepIdle)
		a.send(chatID, "Cancelled. Use /setup to see available commands.")

	default:
		a.send(chatID, "Unknown command. Use /help to see available commands.")
	}
}

// handleSetupInput handles non-command text during setup flow
func (a *Agent) handleSetupInput(msg *tgbotapi.Message, text string) {
	chatID := msg.Chat.ID
	ownerID := msg.From.ID

	step, err := a.db.GetSetupState(a.botID, ownerID)
	if err != nil {
		log.Printf("[Agent %d] Error getting setup state: %v", a.botID, err)
		return
	}

	if step == StepIdle {
		a.send(chatID, "Use /setup to configure your bot, or /help for available commands.")
		return
	}

	// Handle knowledge steps separately
	switch step {
	case StepKnowledgeFile:
		// User sent text instead of a file
		a.send(chatID, "Please send a file (PDF, .md, or .txt).\n\nSend /cancel to abort.")
		return
	case StepKnowledgeURL:
		a.handleKnowledgeURL(chatID, ownerID, text)
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

	field, ok := fieldMap[step]
	if !ok {
		a.db.SetSetupState(a.botID, ownerID, StepIdle)
		return
	}

	// Ensure agent config exists
	_, err = a.db.GetAgentConfig(a.botID)
	if err != nil {
		// Create default config
		a.db.UpsertAgentConfig(&database.AgentConfig{
			BotID:     a.botID,
			Model:     a.appConfig.DefaultModel,
			ChatStyle: "friendly and helpful",
			CanReply:  true,
			CanReact:  true,
		})
	}

	// Update the field
	if err := a.db.UpdateAgentConfigField(a.botID, field, text); err != nil {
		log.Printf("[Agent %d] Error updating config: %v", a.botID, err)
		a.send(chatID, "Error saving configuration. Please try again.")
		return
	}

	// Reset setup state
	a.db.SetSetupState(a.botID, ownerID, StepIdle)

	a.send(chatID, fmt.Sprintf("%s updated successfully!\n\nUse /config to see current configuration or /setup to continue configuring.", stepDisplayName(step)))
}

func (a *Agent) sendSetupWelcome(chatID int64) {
	text := fmt.Sprintf("Welcome! I'm @%s, your AI community manager bot.\n\nTo get started:\n1. Use /setup to configure my behavior\n2. Add me to your Telegram group\n3. Make me an admin (so I can moderate)\n\nI'll then start managing your community based on your configuration!\n\nUse /help to see all available commands.", a.bot.Self.UserName)
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
		"/groups - View groups I'm in"
	a.send(chatID, text)
}

func (a *Agent) sendCurrentConfig(chatID int64) {
	cfg, err := a.db.GetAgentConfig(a.botID)
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

	text := fmt.Sprintf("Current Configuration\n\n"+
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
		"  React: %s\n"+
		"  Delete: %s",
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
		boolEmoji(cfg.CanReact),
		boolEmoji(cfg.CanDelete),
	)
	a.send(chatID, text)
}

func (a *Agent) sendPermissionsMenu(chatID int64) {
	cfg, err := a.db.GetAgentConfig(a.botID)
	if err != nil {
		a.send(chatID, "Please set up your bot first with /setup")
		return
	}

	text := fmt.Sprintf("Bot Permissions\n\n"+
		"Toggle permissions by sending the command:\n\n"+
		"/perm_reply - Reply to messages [%s]\n"+
		"/perm_ban - Ban/mute members [%s]\n"+
		"/perm_pin - Pin messages [%s]\n"+
		"/perm_poll - Create polls [%s]\n"+
		"/perm_react - React to messages [%s]\n"+
		"/perm_delete - Delete messages [%s]\n\n"+
		"Note: The bot also needs the corresponding Telegram admin permissions in the group.",
		boolEmoji(cfg.CanReply),
		boolEmoji(cfg.CanBan),
		boolEmoji(cfg.CanPin),
		boolEmoji(cfg.CanPoll),
		boolEmoji(cfg.CanReact),
		boolEmoji(cfg.CanDelete),
	)
	a.send(chatID, text)
}

func (a *Agent) handlePermissionToggle(msg *tgbotapi.Message, text string) {
	chatID := msg.Chat.ID

	cfg, err := a.db.GetAgentConfig(a.botID)
	if err != nil {
		a.send(chatID, "Please set up your bot first with /setup")
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
		"/perm_react":  {"can_react", !cfg.CanReact},
		"/perm_delete": {"can_delete", !cfg.CanDelete},
	}

	perm, ok := permMap[text]
	if !ok {
		a.send(chatID, "Unknown permission.")
		return
	}

	if err := a.db.UpdateAgentConfigBool(a.botID, perm.field, perm.value); err != nil {
		a.send(chatID, "Error updating permission.")
		return
	}

	status := "disabled"
	if perm.value {
		status = "enabled"
	}

	a.send(chatID, fmt.Sprintf("Permission %s %s.", perm.field, status))
	a.sendPermissionsMenu(chatID)
}

func (a *Agent) sendGroupsList(chatID int64) {
	groups, err := a.db.GetBotGroups(a.botID)
	if err != nil || len(groups) == 0 {
		a.send(chatID, "I'm not in any groups yet. Add me to a group to get started!")
		return
	}

	var lines []string
	for _, g := range groups {
		lines = append(lines, fmt.Sprintf("- %s (ID: %d, Type: %s)", g.ChatTitle, g.ChatID, g.ChatType))
	}

	a.send(chatID, "Groups I'm in:\n\n"+strings.Join(lines, "\n"))
}

func (a *Agent) sendHelp(chatID int64) {
	text := "Available Commands\n\n" +
		"Setup:\n" +
		"/setup - Configuration menu\n" +
		"/config - View current config\n\n" +
		"Configuration:\n" +
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
		"/groups - View groups I'm in\n" +
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
func (a *Agent) handleKnowledgeFile(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	ownerID := msg.From.ID
	doc := msg.Document

	// Validate file extension
	ext := strings.ToLower(filepath.Ext(doc.FileName))
	if !allowedKnowledgeExts[ext] {
		a.send(chatID, fmt.Sprintf("Unsupported file type: %s\n\nPlease send a PDF, Markdown (.md), or Text (.txt) file.", ext))
		return
	}

	// Check file size (OpenAI limit is 512MB, but let's be reasonable for knowledge)
	if doc.FileSize > 20*1024*1024 {
		a.send(chatID, "File is too large (max 20MB). Please send a smaller file.")
		return
	}

	a.send(chatID, fmt.Sprintf("Downloading %s...", doc.FileName))

	// Get file URL from Telegram
	fileConfig := tgbotapi.FileConfig{FileID: doc.FileID}
	tgFile, err := a.bot.GetFile(fileConfig)
	if err != nil {
		log.Printf("[Agent %d] Error getting file from Telegram: %v", a.botID, err)
		a.send(chatID, "Error downloading file from Telegram. Please try again.")
		return
	}

	fileURL := tgFile.Link(a.bot.Token)

	// Download the file content
	resp, err := http.Get(fileURL)
	if err != nil {
		log.Printf("[Agent %d] Error downloading file: %v", a.botID, err)
		a.send(chatID, "Error downloading file. Please try again.")
		return
	}
	defer resp.Body.Close()

	// Read entire file into memory (we need it for both OpenAI upload and preview)
	fileBytes, err := io.ReadAll(io.LimitReader(resp.Body, 20*1024*1024))
	if err != nil {
		log.Printf("[Agent %d] Error reading file content: %v", a.botID, err)
		a.send(chatID, "Error reading file content. Please try again.")
		return
	}

	a.send(chatID, "Uploading to knowledge base...")

	// Ensure vector store exists
	ctx := context.Background()
	vsID, err := a.ensureVectorStore(ctx)
	if err != nil {
		log.Printf("[Agent %d] Error ensuring vector store: %v", a.botID, err)
		a.send(chatID, "Error creating knowledge store. Please try again.")
		a.db.SetSetupState(a.botID, ownerID, StepIdle)
		return
	}

	// Upload to OpenAI vector store
	client := a.getLLMClient()
	reader := strings.NewReader(string(fileBytes))
	fileID, err := client.UploadFileToVectorStore(ctx, vsID, doc.FileName, reader)
	if err != nil {
		log.Printf("[Agent %d] Error uploading file to OpenAI: %v", a.botID, err)
		a.send(chatID, "Error uploading file to knowledge base. Please try again.")
		a.db.SetSetupState(a.botID, ownerID, StepIdle)
		return
	}

	// Build a content preview for local DB storage
	preview := buildFilePreview(fileBytes, ext)

	// Save to local DB
	k := &database.Knowledge{
		BotID:        a.botID,
		SourceType:   "file",
		Title:        doc.FileName,
		Content:      preview,
		OpenAIFileID: fileID,
		AddedBy:      ownerID,
	}
	if err := a.db.AddKnowledge(k); err != nil {
		log.Printf("[Agent %d] Error adding knowledge to DB: %v", a.botID, err)
		a.send(chatID, "File uploaded to OpenAI but failed to save local record.")
		a.db.SetSetupState(a.botID, ownerID, StepIdle)
		return
	}

	a.db.SetSetupState(a.botID, ownerID, StepIdle)
	a.send(chatID, fmt.Sprintf("File uploaded to knowledge base! (ID: %d)\n\nFilename: %s\nSize: %s\n\nUse /list_knowledge to see all entries or /add_knowledge to upload more.",
		k.ID, doc.FileName, formatFileSize(int64(len(fileBytes)))))
}

// buildFilePreview creates a short text preview from file bytes for local DB storage
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

// formatFileSize returns a human-readable file size string
func formatFileSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
}

func (a *Agent) handleKnowledgeURL(chatID, ownerID int64, url string) {
	if !strings.HasPrefix(url, "http") {
		url = "https://" + url
	}

	a.send(chatID, "Fetching content from URL...")

	// Fetch the URL
	httpClient := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		a.send(chatID, "Invalid URL. Please try again with /add_url")
		a.db.SetSetupState(a.botID, ownerID, StepIdle)
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; OpenCM Bot/1.0)")

	resp, err := httpClient.Do(req)
	if err != nil {
		a.send(chatID, fmt.Sprintf("Failed to fetch URL: %v\n\nTry again with /add_url", err))
		a.db.SetSetupState(a.botID, ownerID, StepIdle)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024))
	if err != nil {
		a.send(chatID, "Failed to read URL content. Try again with /add_url")
		a.db.SetSetupState(a.botID, ownerID, StepIdle)
		return
	}

	content := stripHTMLTags(string(body))
	// Collapse whitespace
	content = strings.Join(strings.Fields(content), " ")
	if len(content) > 50000 {
		content = content[:50000]
	}

	if strings.TrimSpace(content) == "" {
		a.send(chatID, "Could not extract text from that URL. Try a different URL or use /add_knowledge to upload a file instead.")
		a.db.SetSetupState(a.botID, ownerID, StepIdle)
		return
	}

	a.send(chatID, "Uploading to knowledge base...")

	// Ensure vector store exists
	ctx := context.Background()
	vsID, err := a.ensureVectorStore(ctx)
	if err != nil {
		log.Printf("[Agent %d] Error ensuring vector store: %v", a.botID, err)
		a.send(chatID, "Error creating knowledge store. Please try again.")
		a.db.SetSetupState(a.botID, ownerID, StepIdle)
		return
	}

	// Upload to OpenAI vector store
	client := a.getLLMClient()
	fileID, err := client.UploadTextAsFile(ctx, vsID, url, content)
	if err != nil {
		log.Printf("[Agent %d] Error uploading URL knowledge: %v", a.botID, err)
		a.send(chatID, "Error uploading knowledge. Please try again.")
		a.db.SetSetupState(a.botID, ownerID, StepIdle)
		return
	}

	k := &database.Knowledge{
		BotID:        a.botID,
		SourceType:   "url",
		Title:        url,
		Content:      truncateStr(content, 500),
		OpenAIFileID: fileID,
		AddedBy:      ownerID,
	}
	if err := a.db.AddKnowledge(k); err != nil {
		log.Printf("[Agent %d] Error adding URL knowledge to DB: %v", a.botID, err)
		a.send(chatID, "Error saving knowledge record.")
		a.db.SetSetupState(a.botID, ownerID, StepIdle)
		return
	}

	a.db.SetSetupState(a.botID, ownerID, StepIdle)
	a.send(chatID, fmt.Sprintf("Knowledge from URL added! (ID: %d)\n\nSource: %s\nContent preview: %s\n\nUse /list_knowledge to see all entries.",
		k.ID, url, truncateStr(content, 200)))
}

func (a *Agent) sendKnowledgeList(chatID int64) {
	items, err := a.db.ListKnowledge(a.botID)
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

func (a *Agent) handleDeleteKnowledge(chatID int64, text string) {
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

	// Verify it belongs to this bot
	k, err := a.db.GetKnowledge(id)
	if err != nil || k.BotID != a.botID {
		a.send(chatID, "Knowledge entry not found.")
		return
	}

	// Delete from OpenAI vector store if file ID exists
	if k.OpenAIFileID != "" {
		cfg, err := a.db.GetAgentConfig(a.botID)
		if err == nil && cfg.VectorStoreID != "" {
			ctx := context.Background()
			client := a.getLLMClient()
			if err := client.DeleteVectorStoreFile(ctx, cfg.VectorStoreID, k.OpenAIFileID); err != nil {
				log.Printf("[Agent %d] Error deleting file from vector store: %v", a.botID, err)
				// Continue with local deletion anyway
			}
		}
	}

	if err := a.db.DeleteKnowledge(id, a.botID); err != nil {
		a.send(chatID, "Error deleting knowledge entry.")
		return
	}

	a.send(chatID, fmt.Sprintf("Knowledge entry %d deleted (\"%s\").", id, truncateStr(k.Title, 50)))
}

// handleSaveCommand handles /save in group chats (reply to a message to save as knowledge)
func (a *Agent) handleSaveCommand(msg *tgbotapi.Message) {
	// Must be a reply
	if msg.ReplyToMessage == nil || msg.ReplyToMessage.Text == "" {
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Reply to a message with /save to save it as knowledge.")
		reply.ReplyToMessageID = msg.MessageID
		a.bot.Send(reply)
		return
	}

	// Check if sender is the owner
	botInfo, err := a.db.GetBotByBotID(a.botID)
	if err != nil {
		return
	}
	if msg.From.ID != botInfo.OwnerID {
		return // Silently ignore non-owner
	}

	savedMsg := msg.ReplyToMessage
	title := fmt.Sprintf("Chat message from %s", savedMsg.From.FirstName)
	content := savedMsg.Text

	// Upload to vector store
	ctx := context.Background()
	vsID, err := a.ensureVectorStore(ctx)
	if err != nil {
		log.Printf("[Agent %d] Error ensuring vector store for /save: %v", a.botID, err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Error saving to knowledge base.")
		reply.ReplyToMessageID = msg.MessageID
		a.bot.Send(reply)
		return
	}

	client := a.getLLMClient()
	fileID, err := client.UploadTextAsFile(ctx, vsID, title, content)
	if err != nil {
		log.Printf("[Agent %d] Error uploading saved message: %v", a.botID, err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Error uploading to knowledge base.")
		reply.ReplyToMessageID = msg.MessageID
		a.bot.Send(reply)
		return
	}

	k := &database.Knowledge{
		BotID:        a.botID,
		SourceType:   "chat",
		Title:        title,
		Content:      truncateStr(content, 500),
		OpenAIFileID: fileID,
		AddedBy:      msg.From.ID,
	}
	if err := a.db.AddKnowledge(k); err != nil {
		log.Printf("[Agent %d] Error saving chat knowledge: %v", a.botID, err)
		return
	}

	reply := tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("Saved to knowledge base (ID: %d)", k.ID))
	reply.ReplyToMessageID = msg.MessageID
	a.bot.Send(reply)
}

// stripHTMLTags removes HTML tags from a string
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

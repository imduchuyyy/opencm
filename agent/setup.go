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
	"github.com/imduchuyyy/opencm/plan"
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
	StepKnowledgeFile   = "knowledge_file"
	StepKnowledgeURL    = "knowledge_url"
)

// Allowed file extensions for knowledge uploads
var allowedKnowledgeExts = map[string]bool{
	".pdf": true,
	".md":  true,
	".txt": true,
}

// send sends a plain text message to a chat
func (a *Agent) send(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	if _, err := a.bot.Send(msg); err != nil {
		log.Printf("[Agent] Send failed: %v", err)
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
		a.send(msg.Chat.ID, MsgDocNeedStep)
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
	case text == CmdStart:
		a.sendSetupWelcome(chatID)

	case text == CmdSetup:
		a.startGroupSelection(chatID, userID)

	case text == CmdConfig:
		a.sendCurrentConfig(chatID, userID)

	case text == CmdSetSystemPrompt:
		a.startConfigStep(chatID, userID, StepSystemPrompt, MsgPromptSystemPrompt)

	case text == CmdSetBio:
		a.startConfigStep(chatID, userID, StepBio, MsgPromptBio)

	case text == CmdSetTopics:
		a.startConfigStep(chatID, userID, StepTopics, MsgPromptTopics)

	case text == CmdSetExamples:
		a.startConfigStep(chatID, userID, StepMessageExamples, MsgPromptExamples)

	case text == CmdSetStyle:
		a.startConfigStep(chatID, userID, StepChatStyle, MsgPromptStyle)

	case text == CmdAddKnowledge:
		a.startKnowledgeStep(chatID, userID, StepKnowledgeFile, MsgPromptKnowledgeFile)

	case text == CmdAddURL:
		a.startKnowledgeStep(chatID, userID, StepKnowledgeURL, MsgPromptKnowledgeURL)

	case text == CmdListKnowledge:
		a.sendKnowledgeList(chatID, userID)

	case strings.HasPrefix(text, CmdDeleteKnowledge):
		a.handleDeleteKnowledge(chatID, userID, text)

	case text == CmdGroups:
		a.sendGroupsList(chatID, userID)

	case text == CmdPlan:
		a.sendPlanInfo(chatID, userID)

	case text == CmdHelp:
		a.sendHelp(chatID)

	case text == CmdCancel:
		a.db.SetSetupState(userID, 0, StepIdle)
		a.send(chatID, MsgCancelled)

	default:
		a.send(chatID, MsgUnknownCommand)
	}
}

// startGroupSelection presents the user with a list of groups they admin
func (a *Agent) startGroupSelection(chatID, userID int64) {
	adminGroups := a.getAdminGroups(userID)
	if len(adminGroups) == 0 {
		a.send(chatID, MsgNotAdminAnyGroup)
		return
	}

	var lines []string
	for i, g := range adminGroups {
		lines = append(lines, fmt.Sprintf("%d. %s (ID: %d)", i+1, g.ChatTitle, g.ChatID))
	}

	a.db.SetSetupState(userID, 0, StepSelectGroup)
	a.send(chatID, fmt.Sprintf(MsgSelectGroupPrompt, strings.Join(lines, "\n")))
}

// startConfigStep verifies the user has a group selected and sets the step
func (a *Agent) startConfigStep(chatID, userID int64, step, prompt string) {
	state, err := a.db.GetSetupState(userID)
	if err != nil || state.ChatID == 0 {
		// No group selected - start selection first
		a.send(chatID, MsgSelectGroupFirst)
		return
	}

	// Verify user is still admin of this group
	if !a.isAdmin(state.ChatID, userID) {
		a.db.SetSetupState(userID, 0, StepIdle)
		a.send(chatID, MsgNoLongerAdmin)
		return
	}

	a.db.SetSetupState(userID, state.ChatID, step)
	a.send(chatID, prompt)
}

// startKnowledgeStep is like startConfigStep but also checks that the group's plan allows knowledge uploads
func (a *Agent) startKnowledgeStep(chatID, userID int64, step, prompt string) {
	state, err := a.db.GetSetupState(userID)
	if err != nil || state.ChatID == 0 {
		a.send(chatID, MsgSelectGroupFirst)
		return
	}

	if !a.isAdmin(state.ChatID, userID) {
		a.db.SetSetupState(userID, 0, StepIdle)
		a.send(chatID, MsgNoLongerAdmin)
		return
	}

	groupCfg := a.getOrCreateGroupConfig(state.ChatID)
	limits := plan.GetLimits(groupCfg.Plan)
	if !limits.KnowledgeUpload {
		a.send(chatID, fmt.Sprintf(MsgKnowledgeNoUpload, groupCfg.Plan.DisplayName()))
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
		a.send(chatID, MsgUseSetupOrHelp)
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
		a.send(chatID, MsgSendFilePrompt)
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
	}

	field, ok := fieldMap[state.Step]
	if !ok {
		a.db.SetSetupState(userID, 0, StepIdle)
		return
	}

	groupChatID := state.ChatID
	if groupChatID == 0 {
		a.send(chatID, MsgNoGroupSelected)
		return
	}

	// Ensure group config exists
	a.getOrCreateGroupConfig(groupChatID)

	// Update the field
	if err := a.db.UpdateGroupConfigField(groupChatID, field, text); err != nil {
		log.Printf("[Agent] Error updating config: %v", err)
		a.send(chatID, MsgErrorSaveConfig)
		return
	}

	a.db.SetSetupState(userID, groupChatID, StepIdle)
	a.send(chatID, fmt.Sprintf(MsgFieldUpdated, stepDisplayName(state.Step)))
}

// handleGroupSelection processes the user's group choice
func (a *Agent) handleGroupSelection(chatID, userID int64, text string) {
	adminGroups := a.getAdminGroups(userID)
	if len(adminGroups) == 0 {
		a.send(chatID, MsgNoGroupsFound)
		a.db.SetSetupState(userID, 0, StepIdle)
		return
	}

	num, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || num < 1 || num > len(adminGroups) {
		a.send(chatID, fmt.Sprintf(MsgInvalidGroupNum, len(adminGroups)))
		return
	}

	selectedGroup := adminGroups[num-1]

	// Ensure group config exists
	a.getOrCreateGroupConfig(selectedGroup.ChatID)

	a.db.SetSetupState(userID, selectedGroup.ChatID, StepIdle)
	a.send(chatID, fmt.Sprintf(MsgGroupSelected, selectedGroup.ChatTitle))
	a.sendSetupMenu(chatID)
}

func (a *Agent) sendSetupWelcome(chatID int64) {
	a.send(chatID, fmt.Sprintf(MsgWelcome, a.bot.Self.UserName))
}

func (a *Agent) sendSetupMenu(chatID int64) {
	a.send(chatID, MsgSetupMenu)
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
		a.send(chatID, MsgNoGroupSelected)
		return
	}

	cfg, err := a.db.GetGroupConfig(groupChatID)
	if err != nil {
		a.send(chatID, MsgNoConfigYet)
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
	if g, err := a.db.GetGroup(groupChatID); err == nil {
		groupTitle = g.ChatTitle
	}

	text := fmt.Sprintf("Configuration for: %s\n\n"+
		"System Prompt: %s\n\n"+
		"Bio: %s\n\n"+
		"Topics: %s\n\n"+
		"Chat Style: %s\n\n"+
		"Message Examples: %s\n\n"+
		"Vector Store: %s",
		groupTitle,
		truncate(cfg.SystemPrompt, 200),
		truncate(cfg.Bio, 200),
		truncate(cfg.Topics, 200),
		truncate(cfg.ChatStyle, 200),
		truncate(cfg.MessageExamples, 200),
		vectorStatus,
	)
	a.send(chatID, text)
}

func (a *Agent) sendGroupsList(chatID, userID int64) {
	adminGroups := a.getAdminGroups(userID)
	if len(adminGroups) == 0 {
		a.send(chatID, MsgNoGroupsAdmin)
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
				selectedNote = fmt.Sprintf(MsgCurrentlyConfiguring, g.ChatTitle)
				break
			}
		}
	}

	a.send(chatID, fmt.Sprintf(MsgGroupsListHeader, strings.Join(lines, "\n"))+selectedNote)
}

func (a *Agent) sendPlanInfo(chatID, userID int64) {
	groupChatID := a.getSelectedGroupChatID(userID)
	if groupChatID == 0 {
		a.send(chatID, MsgNoGroupSelected)
		return
	}

	groupCfg := a.getOrCreateGroupConfig(groupChatID)
	limits := plan.GetLimits(groupCfg.Plan)

	monthlyUsage, _ := a.db.GetMonthlyUsage(groupChatID)

	// Get group title
	groupTitle := fmt.Sprintf("Group %d", groupChatID)
	if g, err := a.db.GetGroup(groupChatID); err == nil {
		groupTitle = g.ChatTitle
	}

	knowledgeStatus := "Not available"
	if limits.KnowledgeUpload {
		knowledgeStatus = fmt.Sprintf("Enabled (max %s per file)", formatFileSize(limits.MaxFileSize))
	}

	text := fmt.Sprintf("Plan for: %s\n\n"+
		"Current Plan: %s\n\n"+
		"Usage This Month: %d / %d messages\n"+
		"Rate Limit: %d messages/min\n"+
		"Knowledge Upload: %s\n\n"+
		"Plans:\n"+
		"  Free - 1,000 msgs/mo, 10/min, no knowledge\n"+
		"  Pro ($49/mo) - 10,000 msgs/mo, 100/min, knowledge (5MB)\n"+
		"  Max ($99/mo) - 50,000 msgs/mo, 100/min, knowledge (20MB)",
		groupTitle,
		groupCfg.Plan.DisplayName(),
		monthlyUsage, limits.MonthlyMessages,
		limits.PerMinute,
		knowledgeStatus,
	)
	a.send(chatID, text)
}

func (a *Agent) sendHelp(chatID int64) {
	a.send(chatID, MsgHelp)
}

func stepDisplayName(step string) string {
	names := map[string]string{
		StepSystemPrompt:    "System Prompt",
		StepBio:             "Bio",
		StepTopics:          "Topics",
		StepMessageExamples: "Message Examples",
		StepChatStyle:       "Chat Style",
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
		a.send(chatID, fmt.Sprintf(MsgUnsupportedFileType, ext))
		return
	}

	// Enforce file size limit based on plan
	groupCfg := a.getOrCreateGroupConfig(groupChatID)
	limits := plan.GetLimits(groupCfg.Plan)
	if int64(doc.FileSize) > limits.MaxFileSize {
		a.send(chatID, fmt.Sprintf(MsgFileTooLarge,
			formatFileSize(limits.MaxFileSize), groupCfg.Plan.DisplayName()))
		return
	}

	a.send(chatID, fmt.Sprintf(MsgDownloading, doc.FileName))

	fileConfig := tgbotapi.FileConfig{FileID: doc.FileID}
	tgFile, err := a.bot.GetFile(fileConfig)
	if err != nil {
		log.Printf("[Agent] Error getting file from Telegram: %v", err)
		a.send(chatID, MsgErrorDownloadTG)
		return
	}

	fileURL := tgFile.Link(a.bot.Token)

	resp, err := http.Get(fileURL)
	if err != nil {
		log.Printf("[Agent] Error downloading file: %v", err)
		a.send(chatID, MsgErrorDownload)
		return
	}
	defer resp.Body.Close()

	fileBytes, err := io.ReadAll(io.LimitReader(resp.Body, limits.MaxFileSize))
	if err != nil {
		log.Printf("[Agent] Error reading file content: %v", err)
		a.send(chatID, MsgErrorReadContent)
		return
	}

	a.send(chatID, MsgUploadingKB)

	ctx := context.Background()
	vsID, err := a.ensureVectorStore(ctx, groupChatID)
	if err != nil {
		log.Printf("[Agent] Error ensuring vector store: %v", err)
		a.send(chatID, MsgErrorKnowledgeStore)
		a.db.SetSetupState(userID, groupChatID, StepIdle)
		return
	}

	client := a.getLLMClient()
	reader := strings.NewReader(string(fileBytes))
	fileID, err := client.UploadFileToVectorStore(ctx, vsID, doc.FileName, reader)
	if err != nil {
		log.Printf("[Agent] Error uploading file to OpenAI: %v", err)
		a.send(chatID, MsgErrorUploadKB)
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
		a.send(chatID, MsgErrorUploadLocal)
		a.db.SetSetupState(userID, groupChatID, StepIdle)
		return
	}

	a.db.SetSetupState(userID, groupChatID, StepIdle)
	a.send(chatID, fmt.Sprintf(MsgFileUploaded,
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

	a.send(chatID, MsgFetchingURL)

	httpClient := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		a.send(chatID, MsgInvalidURL)
		a.db.SetSetupState(userID, groupChatID, StepIdle)
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; OpenCM Bot/1.0)")

	resp, err := httpClient.Do(req)
	if err != nil {
		a.send(chatID, fmt.Sprintf(MsgFailedFetchURL, err))
		a.db.SetSetupState(userID, groupChatID, StepIdle)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024))
	if err != nil {
		a.send(chatID, MsgFailedReadURL)
		a.db.SetSetupState(userID, groupChatID, StepIdle)
		return
	}

	content := stripHTMLTags(string(body))
	content = strings.Join(strings.Fields(content), " ")
	if len(content) > 50000 {
		content = content[:50000]
	}

	if strings.TrimSpace(content) == "" {
		a.send(chatID, MsgNoTextFromURL)
		a.db.SetSetupState(userID, groupChatID, StepIdle)
		return
	}

	a.send(chatID, MsgUploadingKB)

	ctx := context.Background()
	vsID, err := a.ensureVectorStore(ctx, groupChatID)
	if err != nil {
		log.Printf("[Agent] Error ensuring vector store: %v", err)
		a.send(chatID, MsgErrorKnowledgeStore)
		a.db.SetSetupState(userID, groupChatID, StepIdle)
		return
	}

	client := a.getLLMClient()
	fileID, err := client.UploadTextAsFile(ctx, vsID, url, content)
	if err != nil {
		log.Printf("[Agent] Error uploading URL knowledge: %v", err)
		a.send(chatID, MsgErrorUploadURL)
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
		a.send(chatID, MsgErrorSaveRecord)
		a.db.SetSetupState(userID, groupChatID, StepIdle)
		return
	}

	a.db.SetSetupState(userID, groupChatID, StepIdle)
	a.send(chatID, fmt.Sprintf(MsgURLKnowledgeAdded,
		k.ID, url, truncateStr(content, 200)))
}

func (a *Agent) sendKnowledgeList(chatID, userID int64) {
	groupChatID := a.getSelectedGroupChatID(userID)
	if groupChatID == 0 {
		a.send(chatID, MsgNoGroupSelected)
		return
	}

	items, err := a.db.ListKnowledge(groupChatID)
	if err != nil || len(items) == 0 {
		a.send(chatID, MsgErrorKnowledgeNone)
		return
	}

	var lines []string
	for _, k := range items {
		lines = append(lines, fmt.Sprintf("[%d] [%s] %s\n    %s",
			k.ID, k.SourceType, k.Title, truncateStr(k.Content, 80)))
	}

	text := fmt.Sprintf(MsgKnowledgeListFmt,
		len(items), strings.Join(lines, "\n\n"))
	a.send(chatID, text)
}

func (a *Agent) handleDeleteKnowledge(chatID, userID int64, text string) {
	groupChatID := a.getSelectedGroupChatID(userID)
	if groupChatID == 0 {
		a.send(chatID, MsgNoGroupSelected)
		return
	}

	parts := strings.Fields(text)
	if len(parts) < 2 {
		a.send(chatID, MsgDeleteUsage)
		return
	}

	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		a.send(chatID, MsgInvalidID)
		return
	}

	k, err := a.db.GetKnowledge(id)
	if err != nil || k.ChatID != groupChatID {
		a.send(chatID, MsgKnowledgeNotFound)
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
		a.send(chatID, MsgErrorDeleteEntry)
		return
	}

	a.send(chatID, fmt.Sprintf(MsgKnowledgeDeleted, id, truncateStr(k.Title, 50)))
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

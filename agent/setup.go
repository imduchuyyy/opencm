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
	"time"

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

// getLLMClient returns the shared LLM client for vector store operations
func (a *Agent) getLLMClient() *llm.Client {
	return a.llmClient
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
		parts := strings.SplitN(text, " ", 2)
		cmd := parts[0]
		if atIdx := strings.Index(cmd, "@"); atIdx != -1 {
			cmd = cmd[:atIdx]
		}
		if len(parts) > 1 {
			text = cmd + " " + parts[1]
		} else {
			text = cmd
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
		a.startGroupSelection(chatID, userID, msg.From.UserName)

	case text == CmdConfig:
		a.sendCurrentConfig(chatID, userID)

	case text == CmdSetSystemPrompt:
		a.startConfigStep(chatID, userID, msg.From.UserName, StepSystemPrompt, MsgPromptSystemPrompt)

	case text == CmdSetBio:
		a.startConfigStep(chatID, userID, msg.From.UserName, StepBio, MsgPromptBio)

	case text == CmdSetTopics:
		a.startConfigStep(chatID, userID, msg.From.UserName, StepTopics, MsgPromptTopics)

	case text == CmdSetExamples:
		a.startConfigStep(chatID, userID, msg.From.UserName, StepMessageExamples, MsgPromptExamples)

	case text == CmdSetStyle:
		a.startConfigStep(chatID, userID, msg.From.UserName, StepChatStyle, MsgPromptStyle)

	case text == CmdAddKnowledge:
		a.startKnowledgeStep(chatID, userID, msg.From.UserName, StepKnowledgeFile, MsgPromptKnowledgeFile)

	case text == CmdAddURL:
		a.startKnowledgeStep(chatID, userID, msg.From.UserName, StepKnowledgeURL, MsgPromptKnowledgeURL)

	case text == CmdListKnowledge:
		a.sendKnowledgeList(chatID, userID)

	case strings.HasPrefix(text, CmdDeleteKnowledge):
		a.handleDeleteKnowledge(chatID, userID, msg.From.UserName, text)

	case text == CmdGroups:
		a.sendGroupsList(chatID, userID, msg.From.UserName)

	case text == CmdPlan:
		a.sendPlanInfo(chatID, userID)

	case strings.HasPrefix(text, CmdSubscribePro):
		a.handleSubscribeCommand(chatID, userID, plan.Pro, text)

	case strings.HasPrefix(text, CmdSubscribeMax):
		a.handleSubscribeCommand(chatID, userID, plan.Max, text)

	case text == CmdHelp:
		a.sendHelp(chatID)

	case text == CmdCancel:
		a.db.SetSetupState(userID, 0, StepIdle)
		a.send(chatID, MsgCancelled)

	// Super admin commands
	case strings.HasPrefix(text, CmdAdminSearch):
		a.handleAdminSearch(chatID, msg.From.UserName, text)

	case strings.HasPrefix(text, CmdAdminSetPlan):
		a.handleAdminSetPlan(chatID, msg.From.UserName, text)

	case strings.HasPrefix(text, CmdAdminSelect):
		a.handleAdminSelect(chatID, userID, msg.From.UserName, text)

	case text == CmdAdminHelp:
		a.handleAdminHelp(chatID, msg.From.UserName)

	default:
		a.send(chatID, MsgUnknownCommand)
	}
}

// startGroupSelection presents the user with a list of groups they admin.
// Super admin sees all active groups.
func (a *Agent) startGroupSelection(chatID, userID int64, username string) {
	var groups []*database.Group

	if a.isSuperAdmin(username) {
		// Super admin sees all active groups
		var err error
		groups, err = a.db.GetActiveGroups()
		if err != nil {
			log.Printf("[Agent] Error getting active groups: %v", err)
		}
	} else {
		groups = a.getAdminGroups(userID)
	}

	if len(groups) == 0 {
		a.send(chatID, MsgNotAdminAnyGroup)
		return
	}

	var lines []string
	for i, g := range groups {
		lines = append(lines, fmt.Sprintf("%d. %s (ID: %d)", i+1, g.ChatTitle, g.ChatID))
	}

	a.db.SetSetupState(userID, 0, StepSelectGroup)
	a.send(chatID, fmt.Sprintf(MsgSelectGroupPrompt, strings.Join(lines, "\n")))
}

// startConfigStep verifies the user has a group selected and sets the step
func (a *Agent) startConfigStep(chatID, userID int64, username, step, prompt string) {
	state, err := a.db.GetSetupState(userID)
	if err != nil || state.ChatID == 0 {
		// No group selected - start selection first
		a.send(chatID, MsgSelectGroupFirst)
		return
	}

	// Verify user is still admin of this group (super admin bypasses)
	if !a.isAdminOrSuperAdmin(state.ChatID, userID, username) {
		a.db.SetSetupState(userID, 0, StepIdle)
		a.send(chatID, MsgNoLongerAdmin)
		return
	}

	a.db.SetSetupState(userID, state.ChatID, step)
	a.send(chatID, prompt)
}

// startKnowledgeStep is like startConfigStep but also checks that the group's plan allows knowledge uploads
func (a *Agent) startKnowledgeStep(chatID, userID int64, username, step, prompt string) {
	state, err := a.db.GetSetupState(userID)
	if err != nil || state.ChatID == 0 {
		a.send(chatID, MsgSelectGroupFirst)
		return
	}

	if !a.isAdminOrSuperAdmin(state.ChatID, userID, username) {
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
		a.handleGroupSelection(chatID, userID, msg.From.UserName, text)
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
func (a *Agent) handleGroupSelection(chatID, userID int64, username, text string) {
	var groups []*database.Group

	if a.isSuperAdmin(username) {
		var err error
		groups, err = a.db.GetActiveGroups()
		if err != nil {
			log.Printf("[Agent] Error getting active groups: %v", err)
		}
	} else {
		groups = a.getAdminGroups(userID)
	}

	if len(groups) == 0 {
		a.send(chatID, MsgNoGroupsFound)
		a.db.SetSetupState(userID, 0, StepIdle)
		return
	}

	num, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || num < 1 || num > len(groups) {
		a.send(chatID, fmt.Sprintf(MsgInvalidGroupNum, len(groups)))
		return
	}

	selectedGroup := groups[num-1]

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

func (a *Agent) sendGroupsList(chatID, userID int64, username string) {
	var groups []*database.Group

	if a.isSuperAdmin(username) {
		var err error
		groups, err = a.db.GetActiveGroups()
		if err != nil {
			log.Printf("[Agent] Error getting active groups: %v", err)
		}
	} else {
		groups = a.getAdminGroups(userID)
	}

	if len(groups) == 0 {
		a.send(chatID, MsgNoGroupsAdmin)
		return
	}

	var lines []string
	for _, g := range groups {
		line := fmt.Sprintf("- %s (ID: %d, Type: %s)", g.ChatTitle, g.ChatID, g.ChatType)
		if a.isSuperAdmin(username) {
			effectivePlan := a.db.GetEffectivePlan(g.ChatID)
			line += fmt.Sprintf(" [%s]", effectivePlan.ShortName())
		}
		lines = append(lines, line)
	}

	// Show which group is currently selected
	selectedID := a.getSelectedGroupChatID(userID)
	selectedNote := ""
	if selectedID != 0 {
		for _, g := range groups {
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

	// Derive effective plan from subscription
	effectivePlan := a.db.GetEffectivePlan(groupChatID)
	limits := plan.GetLimits(effectivePlan)

	monthlyUsage, _ := a.db.GetMonthlyUsage(groupChatID)

	// Get group title
	groupTitle := fmt.Sprintf("Group %d", groupChatID)
	if g, err := a.db.GetGroup(groupChatID); err == nil {
		groupTitle = g.ChatTitle
	}

	// Determine subscription status
	status := "Free (no subscription)"
	sub, err := a.db.GetActiveSubscription(groupChatID)
	if err == nil && sub != nil {
		status = fmt.Sprintf("Active (expires %s)", sub.ExpiresAt.Format("Jan 2, 2006"))
	} else if effectivePlan == plan.Custom {
		status = "Custom plan (managed by admin)"
	}

	// Feature flags as readable strings
	boolStr := func(b bool) string {
		if b {
			return "Enabled"
		}
		return "Not available"
	}

	knowledgeStatus := "Not available"
	if limits.KnowledgeUpload {
		knowledgeStatus = fmt.Sprintf("Enabled (max %s per file)", formatFileSize(limits.MaxFileSize))
	}

	_ = groupCfg // already used above via getOrCreateGroupConfig

	text := fmt.Sprintf(MsgPlanInfo,
		groupTitle,
		effectivePlan.DisplayName(),
		status,
		monthlyUsage, limits.MonthlyMessages,
		limits.PerMinute,
		boolStr(limits.WebSearch),
		boolStr(limits.WebFetch),
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

// ----- Super Admin handlers -----

// handleAdminSearch searches all groups by name (super admin only)
func (a *Agent) handleAdminSearch(chatID int64, username, text string) {
	if !a.isSuperAdmin(username) {
		a.send(chatID, MsgNotSuperAdmin)
		return
	}

	parts := strings.SplitN(text, " ", 2)
	if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
		a.send(chatID, MsgAdminSearchUsage)
		return
	}

	query := strings.TrimSpace(parts[1])
	groups, err := a.db.SearchGroupsByName(query)
	if err != nil {
		log.Printf("[Admin] Error searching groups: %v", err)
		a.send(chatID, fmt.Sprintf(MsgAdminSearchNoResult, query))
		return
	}

	if len(groups) == 0 {
		a.send(chatID, fmt.Sprintf(MsgAdminSearchNoResult, query))
		return
	}

	var lines []string
	for _, g := range groups {
		effectivePlan := a.db.GetEffectivePlan(g.ChatID)
		lines = append(lines, fmt.Sprintf("- %s\n  ID: %d | Type: %s | Plan: %s",
			g.ChatTitle, g.ChatID, g.ChatType, effectivePlan.ShortName()))
	}

	a.send(chatID, fmt.Sprintf(MsgAdminSearchResult, query, strings.Join(lines, "\n\n")))
}

// handleAdminSetPlan sets a group's plan without payment (super admin only)
func (a *Agent) handleAdminSetPlan(chatID int64, username, text string) {
	if !a.isSuperAdmin(username) {
		a.send(chatID, MsgNotSuperAdmin)
		return
	}

	parts := strings.Fields(text)
	if len(parts) < 3 {
		a.send(chatID, MsgAdminSetPlanUsage)
		return
	}

	targetChatID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		a.send(chatID, MsgAdminSetPlanUsage)
		return
	}

	targetPlan := plan.Plan(strings.ToLower(parts[2]))
	if !targetPlan.Valid() {
		a.send(chatID, MsgAdminSetPlanUsage)
		return
	}

	// Verify group exists
	group, err := a.db.GetGroup(targetChatID)
	if err != nil {
		a.send(chatID, MsgAdminGroupNotFound)
		return
	}

	// Ensure group config exists
	groupCfg := a.getOrCreateGroupConfig(targetChatID)
	groupCfg.Plan = targetPlan
	if err := a.db.UpsertGroupConfig(groupCfg); err != nil {
		a.send(chatID, fmt.Sprintf(MsgAdminSetPlanError, err))
		return
	}

	// Expire any existing active subscriptions first
	if err := a.db.ExpireActiveSubscriptions(targetChatID); err != nil {
		log.Printf("[Admin] Error expiring subscriptions for %d: %v", targetChatID, err)
	}

	// For paid/custom plans, create a subscription record (10 years = effectively permanent)
	if targetPlan != plan.Free {
		now := time.Now().UTC()
		expiresAt := now.AddDate(10, 0, 0) // 10-year subscription

		sub := &database.Subscription{
			ChatID:                  targetChatID,
			Plan:                    string(targetPlan),
			BillingPeriod:           "admin_grant",
			StarAmount:              0,
			TelegramPaymentChargeID: fmt.Sprintf("admin_grant_%d", now.Unix()),
			StartedAt:               now,
			ExpiresAt:               expiresAt,
		}
		if err := a.db.CreateSubscription(sub); err != nil {
			a.send(chatID, fmt.Sprintf(MsgAdminSetPlanError, err))
			return
		}

		a.send(chatID, fmt.Sprintf(MsgAdminSetPlanDone,
			group.ChatTitle, targetChatID, targetPlan.ShortName(),
			expiresAt.Format("Jan 2, 2006")))
	} else {
		// Setting to Free - just update config, no subscription needed
		a.send(chatID, fmt.Sprintf(MsgAdminSetPlanDone,
			group.ChatTitle, targetChatID, "Free", "N/A (free plan)"))
	}
}

// handleAdminSelect selects any group for configuration (super admin only, bypasses admin check)
func (a *Agent) handleAdminSelect(chatID, userID int64, username, text string) {
	if !a.isSuperAdmin(username) {
		a.send(chatID, MsgNotSuperAdmin)
		return
	}

	parts := strings.Fields(text)
	if len(parts) < 2 {
		a.send(chatID, MsgAdminSelectUsage)
		return
	}

	targetChatID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		a.send(chatID, MsgAdminSelectUsage)
		return
	}

	group, err := a.db.GetGroup(targetChatID)
	if err != nil {
		a.send(chatID, MsgAdminGroupNotFound)
		return
	}

	a.getOrCreateGroupConfig(targetChatID)
	a.db.SetSetupState(userID, targetChatID, StepIdle)
	a.send(chatID, fmt.Sprintf(MsgAdminSelectDone, group.ChatTitle, group.ChatID))
	a.sendSetupMenu(chatID)
}

// handleAdminHelp shows super admin commands
func (a *Agent) handleAdminHelp(chatID int64, username string) {
	if !a.isSuperAdmin(username) {
		a.send(chatID, MsgNotSuperAdmin)
		return
	}
	a.send(chatID, MsgAdminHelp)
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

func (a *Agent) handleDeleteKnowledge(chatID, userID int64, username, text string) {
	groupChatID := a.getSelectedGroupChatID(userID)
	if groupChatID == 0 {
		a.send(chatID, MsgNoGroupSelected)
		return
	}

	// Verify user is still admin of this group (or super admin)
	if !a.isAdminOrSuperAdmin(groupChatID, userID, username) {
		a.db.SetSetupState(userID, 0, StepIdle)
		a.send(chatID, MsgNoLongerAdmin)
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
	if s == "" {
		return s
	}
	runes := []rune(s)
	if len(runes) > max {
		return string(runes[:max]) + "..."
	}
	return s
}

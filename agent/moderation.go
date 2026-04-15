package agent

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/imduchuyyy/opencm/database"
	"github.com/imduchuyyy/opencm/llm"
	"github.com/imduchuyyy/opencm/plan"
)

// ----- Welcome / Onboarding -----

// handleNewChatMembers sends LLM-generated welcome messages when users join a group.
// Uses gpt-4o-mini to write a personalized welcome based on group config (bio, topics, style, rules)
// and the admin's welcome guidance (set via /set_welcome).
func (a *Agent) handleNewChatMembers(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID

	// Get group config
	cfg, err := a.db.GetGroupConfig(chatID)
	if err != nil {
		// No config = not set up yet, skip
		return
	}

	for _, newUser := range msg.NewChatMembers {
		// Don't welcome the bot itself
		if newUser.ID == a.bot.Self.ID {
			continue
		}

		// Track the new member
		a.db.UpsertGroupMember(chatID, newUser.ID, newUser.UserName, newUser.FirstName)

		// Determine the new member's display name
		name := newUser.FirstName
		if name == "" {
			name = newUser.UserName
		}
		if name == "" {
			name = "there"
		}

		// Generate welcome message using LLM
		welcomeText := a.generateLLMWelcome(chatID, cfg, name)

		// Send as a reply to the join message
		reply := tgbotapi.NewMessage(chatID, welcomeText)
		reply.ReplyToMessageID = msg.MessageID
		if _, err := a.bot.Send(reply); err != nil {
			log.Printf("[Welcome] Failed to send welcome message: %v", err)
		}
	}
}

// generateLLMWelcome uses gpt-4o-mini to write a personalized welcome message for a new member.
// Falls back to a simple default if the LLM call fails.
func (a *Agent) generateLLMWelcome(chatID int64, cfg *database.GroupConfig, memberName string) string {
	subClient := llm.NewClient(a.appConfig.OpenAIAPIKey, "gpt-4o-mini")

	// Build context for the LLM from group config
	var contextParts []string
	if cfg.Bio != "" {
		contextParts = append(contextParts, "Bot identity: "+cfg.Bio)
	}
	if cfg.Topics != "" {
		contextParts = append(contextParts, "Group topics: "+cfg.Topics)
	}
	if cfg.ChatStyle != "" {
		contextParts = append(contextParts, "Chat style: "+cfg.ChatStyle)
	}
	if cfg.RulesText != "" {
		contextParts = append(contextParts, "Group rules:\n"+cfg.RulesText)
	}

	// Admin guidance from /set_welcome (if any)
	adminGuidance := ""
	if cfg.WelcomeMessage != "" {
		adminGuidance = fmt.Sprintf("\nAdmin guidance for welcome messages: %s", cfg.WelcomeMessage)
	}

	groupContext := strings.Join(contextParts, "\n")

	systemPrompt := `You are writing a welcome message for a new member joining a Telegram group.

Rules:
- Keep it SHORT: 1-3 sentences max
- Sound like a real person, not a bot. Match the group's vibe.
- Mention the new member's name naturally
- If the group has rules, briefly mention they should check the rules (don't list them all)
- Do NOT use excessive emojis
- Do NOT say "feel free to" or "don't hesitate to"
- Do NOT ask questions back
- Output ONLY the welcome message text, nothing else`

	userMsg := fmt.Sprintf("New member name: %s\n\nGroup info:\n%s%s",
		memberName, groupContext, adminGuidance)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := subClient.Chat(ctx, systemPrompt, []llm.InputMessage{{Text: userMsg}}, nil, "")
	if err != nil {
		log.Printf("[Welcome] LLM welcome generation failed for chat %d: %v, using fallback", chatID, err)
		return fmt.Sprintf("Welcome %s!", memberName)
	}

	text := strings.TrimSpace(resp.Text)
	if text == "" {
		return fmt.Sprintf("Welcome %s!", memberName)
	}
	return text
}

// ----- Report / Analytics (with chat summary) -----

func (a *Agent) handleReport(chatID, userID int64) {
	groupChatID := a.getSelectedGroupChatID(userID)
	if groupChatID == 0 {
		a.send(chatID, MsgNoGroupSelected)
		return
	}

	// Get group title
	groupTitle := fmt.Sprintf("Group %d", groupChatID)
	if g, err := a.db.GetGroup(groupChatID); err == nil {
		groupTitle = g.ChatTitle
	}

	days := 7

	// Total tracked members
	totalMembers, _ := a.db.GetTotalMembersCount(groupChatID)
	activeMembers, _ := a.db.GetActiveMembersCount(groupChatID, 7)
	newMembers, _ := a.db.GetNewMembersCount(groupChatID, days)

	// Message stats
	dailyCounts, _ := a.db.GetDailyMessageCount(groupChatID, days)
	var msgLines []string
	totalMsgs := 0

	// Sort dates
	var dates []string
	for d := range dailyCounts {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	for _, d := range dates {
		count := dailyCounts[d]
		totalMsgs += count
		msgLines = append(msgLines, fmt.Sprintf("  %s: %d msgs", d, count))
	}
	if len(msgLines) == 0 {
		msgLines = append(msgLines, "  (no messages)")
	}
	msgLines = append([]string{fmt.Sprintf("  Total (%dd): %d", days, totalMsgs)}, msgLines...)

	// Top posters
	topMembers, _ := a.db.GetTopMembers(groupChatID, 5)
	var topLines []string
	for i, m := range topMembers {
		name := m.FirstName
		if name == "" {
			name = m.Username
		}
		if name == "" {
			name = fmt.Sprintf("User %d", m.UserID)
		}
		topLines = append(topLines, fmt.Sprintf("  %d. %s - %d msgs", i+1, name, m.MessageCount))
	}
	if len(topLines) == 0 {
		topLines = append(topLines, "  (no data)")
	}

	// Mod actions
	modCount, _ := a.db.GetModActionCount(groupChatID, time.Now().AddDate(0, 0, -days))

	// Plan info
	effectivePlan := a.db.GetEffectivePlan(groupChatID)
	limits := plan.GetLimits(effectivePlan)
	monthlyUsage, _ := a.db.GetMonthlyUsage(groupChatID)

	// Generate LLM chat summary from recent messages
	summaryText := a.generateChatSummaryForReport(groupChatID, days)

	text := fmt.Sprintf(MsgReportHeader,
		groupTitle, days,
		totalMembers, activeMembers,
		days, newMembers,
		strings.Join(msgLines, "\n"),
		strings.Join(topLines, "\n"),
		days, modCount,
		effectivePlan.ShortName(), monthlyUsage, limits.MonthlyMessages,
		summaryText,
	)

	a.send(chatID, text)
}

// generateChatSummaryForReport uses gpt-4o-mini to generate a chat summary for the report.
// Returns a summary string, or a fallback message if generation fails.
func (a *Agent) generateChatSummaryForReport(chatID int64, days int) string {
	since := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)

	msgs, err := a.db.GetMessagesSince(chatID, since, 500)
	if err != nil || len(msgs) < 5 {
		return "(Not enough messages to generate a summary)"
	}

	// Format messages for LLM
	var lines []string
	for _, msg := range msgs {
		name := msg.FromFirstName
		if name == "" {
			name = msg.FromUsername
		}
		lines = append(lines, fmt.Sprintf("[%s] %s: %s",
			msg.CreatedAt.Format("Jan 2 15:04"), name, msg.Text))
	}
	chatLog := strings.Join(lines, "\n")
	if len(chatLog) > 15000 {
		chatLog = chatLog[:15000] + "\n... (truncated)"
	}

	subClient := llm.NewClient(a.appConfig.OpenAIAPIKey, "gpt-4o-mini")

	systemPrompt := `You are a chat summarizer for a Telegram group. Generate a concise summary of the conversation.

Rules:
- Highlight key topics discussed, important decisions, and notable interactions
- Mention active participants by name
- Keep it to 3-8 bullet points
- Use simple, clear language
- If there was a heated discussion, note the topic but stay neutral
- Include any links or resources that were shared
- Do NOT use emojis excessively
- Format as plain text bullet points (use - for each point)`

	userMsg := fmt.Sprintf("Summarize this chat activity from the last %d days (%d messages):\n\n%s",
		days, len(msgs), chatLog)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	resp, err := subClient.Chat(ctx, systemPrompt, []llm.InputMessage{{Text: userMsg}}, nil, "")
	if err != nil {
		log.Printf("[Report] LLM summary generation failed for chat %d: %v", chatID, err)
		return "(Summary generation failed)"
	}

	text := strings.TrimSpace(resp.Text)
	if text == "" {
		return "(Empty summary generated)"
	}
	return text
}

// ----- Inactivity Engagement -----

// checkInactivityAndEngage checks all active groups for inactivity and posts engagement content.
func (a *Agent) checkInactivityAndEngage(ctx context.Context) {
	groups, err := a.db.GetActiveGroups()
	if err != nil {
		log.Printf("[Engage] Error getting active groups: %v", err)
		return
	}

	for _, g := range groups {
		cfg, err := a.db.GetGroupConfig(g.ChatID)
		if err != nil {
			continue
		}

		// Only engage groups that have topics configured (meaning they're set up)
		if strings.TrimSpace(cfg.Topics) == "" {
			continue
		}

		lastMsgTime, err := a.db.GetLastMessageTime(g.ChatID)
		if err != nil {
			continue
		}

		// If no messages in the last 12 hours, try to engage
		silenceThreshold := 12 * time.Hour
		if time.Since(lastMsgTime) < silenceThreshold {
			continue
		}

		// Don't engage more than once per silence period - check if we already posted recently
		recentPosts, _ := a.db.GetRecentGeneratedPosts(g.ChatID, 1)
		if len(recentPosts) > 0 && time.Since(recentPosts[0].CreatedAt) < silenceThreshold {
			continue
		}

		log.Printf("[Engage] Group %d (%s) has been quiet for %v, generating engagement",
			g.ChatID, g.ChatTitle, time.Since(lastMsgTime).Round(time.Hour))

		a.generateEngagementPost(ctx, g.ChatID, cfg)
	}
}

// generateEngagementPost creates an engagement prompt for a quiet group.
func (a *Agent) generateEngagementPost(ctx context.Context, chatID int64, cfg *database.GroupConfig) {
	subClient := llm.NewClient(a.appConfig.OpenAIAPIKey, "gpt-4o-mini")

	systemPrompt := `You are generating a conversation starter for a quiet Telegram group. The group has been inactive and needs a nudge.

Rules:
- Generate ONE of: a thought-provoking question, a fun poll idea, an interesting fact, or a discussion topic
- Make it relevant to the group's topics
- Keep it SHORT (1-3 sentences max)
- Be casual, not corporate
- Do NOT say "Hey everyone" or "It's been quiet" - just jump into the content
- Output ONLY the message text, nothing else`

	userMsg := fmt.Sprintf("Group topics: %s", cfg.Topics)
	if cfg.Bio != "" {
		userMsg += "\nBot identity: " + cfg.Bio
	}

	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	resp, err := subClient.Chat(callCtx, systemPrompt, []llm.InputMessage{{Text: userMsg}}, nil, "")
	if err != nil {
		log.Printf("[Engage] Error generating engagement for chat %d: %v", chatID, err)
		return
	}

	text := strings.TrimSpace(resp.Text)
	if text == "" {
		return
	}

	// Determine where to post
	targetChatID := chatID

	msgToSend := tgbotapi.NewMessage(targetChatID, text)
	sent, err := a.bot.Send(msgToSend)
	if err != nil {
		log.Printf("[Engage] Failed to send engagement to chat %d: %v", chatID, err)
		return
	}

	// Save as a generated post for tracking
	a.db.SaveGeneratedPost(&database.GeneratedPost{
		ChatID:    chatID,
		ChannelID: targetChatID,
		Source:    "engagement",
		Query:     "inactivity nudge",
		Content:   text,
		MessageID: sent.MessageID,
	})

	log.Printf("[Engage] Sent engagement message to chat %d", chatID)
}

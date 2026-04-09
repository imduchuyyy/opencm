package master

import (
	"context"
	"fmt"
	"log"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/imduchuyyy/opencm/internal/agent"
	"github.com/imduchuyyy/opencm/internal/config"
	"github.com/imduchuyyy/opencm/internal/database"
)

// Bot is the master bot that handles user onboarding and agent creation
type Bot struct {
	api          *tgbotapi.BotAPI
	db           *database.DB
	appConfig    *config.Config
	agentManager *agent.Manager
	// Track users who are in the process of adding a bot token
	pendingTokens map[int64]bool // userID -> waiting for token
}

func New(cfg *config.Config, db *database.DB, agentManager *agent.Manager) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(cfg.MasterBotToken)
	if err != nil {
		return nil, fmt.Errorf("create master bot: %w", err)
	}

	log.Printf("[Master] Bot started as @%s", api.Self.UserName)

	return &Bot{
		api:           api,
		db:            db,
		appConfig:     cfg,
		agentManager:  agentManager,
		pendingTokens: make(map[int64]bool),
	}, nil
}

// Start begins polling for updates
func (b *Bot) Start(ctx context.Context) {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30

	updates := b.api.GetUpdatesChan(u)

	for {
		select {
		case <-ctx.Done():
			return
		case update := <-updates:
			if update.Message == nil {
				continue
			}
			b.handleMessage(update.Message)
		}
	}
}

func (b *Bot) Stop() {
	b.api.StopReceivingUpdates()
}

func (b *Bot) handleMessage(msg *tgbotapi.Message) {
	// Only handle private messages
	if !msg.Chat.IsPrivate() {
		return
	}

	text := strings.TrimSpace(msg.Text)
	userID := msg.From.ID
	chatID := msg.Chat.ID

	// Handle commands
	if strings.HasPrefix(text, "/") {
		b.handleCommand(chatID, userID, text)
		return
	}

	// Handle pending token input
	if b.pendingTokens[userID] {
		b.handleBotToken(chatID, userID, text)
		return
	}

	b.send(chatID, "Use /help to see available commands.")
}

func (b *Bot) handleCommand(chatID, userID int64, text string) {
	switch {
	case text == "/start":
		b.send(chatID, `*Welcome to OpenCM!*

I help you create AI-powered community managers for your Telegram groups.

*How it works:*
1. Create a bot via @BotFather
2. Send me the bot token
3. Configure your bot's personality & permissions
4. Add your bot to your group
5. Your AI community manager is live!

Use /newbot to get started.`)

	case text == "/newbot":
		b.pendingTokens[userID] = true
		b.send(chatID, `*Create a New AI Agent*

To create a new AI community manager, I need a Telegram bot token.

*Steps:*
1. Open @BotFather
2. Send /newbot and follow the instructions
3. Copy the bot token
4. Paste it here

Send me the bot token now:`)

	case text == "/mybots":
		b.listBots(chatID, userID)

	case text == "/help":
		b.send(chatID, `*OpenCM - AI Community Manager*

*Commands:*
/newbot - Create a new AI agent bot
/mybots - List your bots
/help - Show this message

*How to use:*
1. /newbot to create and register a new bot
2. Chat with your bot directly to configure it
3. Add the bot to your group and make it admin
4. The AI will start managing your community!`)

	default:
		b.send(chatID, "Unknown command. Use /help to see available commands.")
	}
}

func (b *Bot) handleBotToken(chatID, userID int64, token string) {
	delete(b.pendingTokens, userID)

	// Validate the token by trying to create a bot API instance
	testBot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		b.send(chatID, "Invalid bot token. Please check and try again.\n\nUse /newbot to retry.")
		return
	}

	botUser := testBot.Self
	log.Printf("[Master] User %d registering bot @%s (ID: %d)", userID, botUser.UserName, botUser.ID)

	// Check if this bot is already registered
	existing, err := b.db.GetBotByBotID(int64(botUser.ID))
	if err == nil && existing != nil {
		if existing.OwnerID == userID {
			b.send(chatID, fmt.Sprintf("Bot @%s is already registered! Chat with @%s directly to configure it.", botUser.UserName, botUser.UserName))
		} else {
			b.send(chatID, "This bot is already registered by another user.")
		}
		return
	}

	// Register the bot
	bot := &database.Bot{
		OwnerID:  userID,
		BotToken: token,
		BotID:    int64(botUser.ID),
		BotName:  botUser.UserName,
	}
	if err := b.db.CreateBot(bot); err != nil {
		log.Printf("[Master] Error creating bot: %v", err)
		b.send(chatID, "Error registering bot. Please try again.")
		return
	}

	// Create default agent config
	defaultCfg := &database.AgentConfig{
		BotID:     int64(botUser.ID),
		Model:     b.appConfig.DefaultModel,
		ChatStyle: "friendly and helpful",
		CanReply:  true,
		CanReact:  true,
	}
	if err := b.db.UpsertAgentConfig(defaultCfg); err != nil {
		log.Printf("[Master] Error creating agent config: %v", err)
	}

	// Start the agent
	ctx := context.Background()
	if err := b.agentManager.StartAgent(ctx, token, int64(botUser.ID)); err != nil {
		log.Printf("[Master] Error starting agent: %v", err)
		b.send(chatID, fmt.Sprintf("Bot @%s registered but failed to start. Please try again later.", botUser.UserName))
		return
	}

	b.send(chatID, fmt.Sprintf(`*Bot @%s registered successfully!*

*Next steps:*
1. Chat with @%s directly to configure its personality and behavior
2. Add @%s to your Telegram group
3. Make @%s an admin in the group
4. Your AI community manager will start working!

Send /start to @%s to begin configuration.`,
		botUser.UserName, botUser.UserName, botUser.UserName, botUser.UserName, botUser.UserName))
}

func (b *Bot) listBots(chatID, userID int64) {
	bots, err := b.db.GetBotsByOwner(userID)
	if err != nil || len(bots) == 0 {
		b.send(chatID, "You don't have any bots yet. Use /newbot to create one!")
		return
	}

	var lines []string
	for _, bot := range bots {
		status := "Active"
		if !bot.IsActive {
			status = "Inactive"
		}
		running := ""
		if b.agentManager.IsRunning(bot.BotID) {
			running = " (running)"
		}
		lines = append(lines, fmt.Sprintf("• @%s - %s%s", bot.BotName, status, running))
	}

	b.send(chatID, "*Your Bots:*\n\n"+strings.Join(lines, "\n")+"\n\nChat with each bot directly to configure it.")
}

func (b *Bot) send(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	if _, err := b.api.Send(msg); err != nil {
		// Retry without markdown
		msg.ParseMode = ""
		b.api.Send(msg)
	}
}

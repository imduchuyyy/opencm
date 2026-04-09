package database

import "time"

// Bot represents a registered agent bot
type Bot struct {
	ID        int64     `json:"id"`
	OwnerID   int64     `json:"owner_id"`  // Telegram user ID of the owner
	BotToken  string    `json:"bot_token"` // Telegram bot API token
	BotID     int64     `json:"bot_id"`    // Telegram bot user ID
	BotName   string    `json:"bot_name"`  // Bot username
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AgentConfig holds the AI agent configuration for a bot
type AgentConfig struct {
	ID              int64  `json:"id"`
	BotID           int64  `json:"bot_id"`
	SystemPrompt    string `json:"system_prompt"`
	Bio             string `json:"bio"`
	Topics          string `json:"topics"`
	MessageExamples string `json:"message_examples"`
	ChatStyle       string `json:"chat_style"`
	Model           string `json:"model"`           // OpenAI model (gpt-4o, etc.)
	VectorStoreID   string `json:"vector_store_id"` // OpenAI vector store ID for file search
	// Permissions
	CanReply  bool      `json:"can_reply"`
	CanBan    bool      `json:"can_ban"`
	CanPin    bool      `json:"can_pin"`
	CanPoll   bool      `json:"can_poll"`
	CanReact  bool      `json:"can_react"`
	CanDelete bool      `json:"can_delete"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Message stores every message received by an agent bot
type Message struct {
	ID               int64     `json:"id"`
	BotID            int64     `json:"bot_id"`
	ChatID           int64     `json:"chat_id"`
	ChatType         string    `json:"chat_type"`
	MessageID        int       `json:"message_id"`
	ReplyToMessageID int       `json:"reply_to_message_id"` // Telegram message ID this is replying to (0 if not a reply)
	FromUserID       int64     `json:"from_user_id"`
	FromUsername     string    `json:"from_username"`
	FromFirstName    string    `json:"from_first_name"`
	Text             string    `json:"text"`
	IsProcessed      bool      `json:"is_processed"`
	AIResponse       string    `json:"ai_response"`
	CreatedAt        time.Time `json:"created_at"`
}

// BotGroup tracks which groups a bot has been added to
type BotGroup struct {
	ID        int64     `json:"id"`
	BotID     int64     `json:"bot_id"`
	ChatID    int64     `json:"chat_id"`
	ChatTitle string    `json:"chat_title"`
	ChatType  string    `json:"chat_type"`
	IsActive  bool      `json:"is_active"`
	JoinedAt  time.Time `json:"joined_at"`
}

// SetupState tracks the configuration conversation state
type SetupState struct {
	ID      int64  `json:"id"`
	BotID   int64  `json:"bot_id"`
	OwnerID int64  `json:"owner_id"`
	Step    string `json:"step"`
}

// Knowledge stores a piece of knowledge for an agent
type Knowledge struct {
	ID           int64     `json:"id"`
	BotID        int64     `json:"bot_id"`
	SourceType   string    `json:"source_type"` // text, file, url, chat
	Title        string    `json:"title"`
	Content      string    `json:"content"`        // Local preview/summary
	OpenAIFileID string    `json:"openai_file_id"` // OpenAI file ID in vector store
	AddedBy      int64     `json:"added_by"`
	CreatedAt    time.Time `json:"created_at"`
}

package database

import (
	"time"

	"github.com/imduchuyyy/opencm/plan"
)

// GroupConfig holds the AI agent configuration for a specific group
type GroupConfig struct {
	ID              int64     `json:"id"`
	ChatID          int64     `json:"chat_id"` // Telegram chat ID (group/supergroup)
	Plan            plan.Plan `json:"plan"`    // Subscription tier: free, pro, max
	SystemPrompt    string    `json:"system_prompt"`
	Bio             string    `json:"bio"`
	Topics          string    `json:"topics"`
	MessageExamples string    `json:"message_examples"`
	ChatStyle       string    `json:"chat_style"`
	VectorStoreID   string    `json:"vector_store_id"` // OpenAI vector store ID for file search
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Group tracks which groups the bot has been added to
type Group struct {
	ID        int64     `json:"id"`
	ChatID    int64     `json:"chat_id"`
	ChatTitle string    `json:"chat_title"`
	ChatType  string    `json:"chat_type"` // group, supergroup
	IsActive  bool      `json:"is_active"`
	JoinedAt  time.Time `json:"joined_at"`
}

// Message stores every message received by the bot
type Message struct {
	ID               int64     `json:"id"`
	ChatID           int64     `json:"chat_id"`
	ChatType         string    `json:"chat_type"`
	MessageID        int       `json:"message_id"`
	ReplyToMessageID int       `json:"reply_to_message_id"` // Telegram message ID this is replying to (0 if not a reply)
	FromUserID       int64     `json:"from_user_id"`
	FromUsername     string    `json:"from_username"`
	FromFirstName    string    `json:"from_first_name"`
	Text             string    `json:"text"`
	MediaType        string    `json:"media_type"`    // "", "photo", "video", "document", "voice", "sticker", "animation"
	MediaFileID      string    `json:"media_file_id"` // Telegram file_id for the media
	IsProcessed      bool      `json:"is_processed"`
	AIResponse       string    `json:"ai_response"`
	CreatedAt        time.Time `json:"created_at"`
}

// SetupState tracks the configuration conversation state for a user configuring a specific group
type SetupState struct {
	ID     int64  `json:"id"`
	UserID int64  `json:"user_id"` // Telegram user ID of the admin
	ChatID int64  `json:"chat_id"` // Group being configured (0 = selecting a group)
	Step   string `json:"step"`
}

// Knowledge stores a piece of knowledge for a specific group
type Knowledge struct {
	ID           int64     `json:"id"`
	ChatID       int64     `json:"chat_id"`     // Group this knowledge belongs to
	SourceType   string    `json:"source_type"` // text, file, url, chat
	Title        string    `json:"title"`
	Content      string    `json:"content"`        // Local preview/summary
	OpenAIFileID string    `json:"openai_file_id"` // OpenAI file ID in vector store
	AddedBy      int64     `json:"added_by"`
	CreatedAt    time.Time `json:"created_at"`
}

// Subscription tracks a paid plan subscription for a group
type Subscription struct {
	ID                      int64     `json:"id"`
	ChatID                  int64     `json:"chat_id"`                    // Group this subscription is for
	Plan                    string    `json:"plan"`                       // pro, max, custom
	BillingPeriod           string    `json:"billing_period"`             // monthly, yearly
	StarAmount              int       `json:"star_amount"`                // Number of Telegram Stars paid
	TelegramPaymentChargeID string    `json:"telegram_payment_charge_id"` // For refunds
	StartedAt               time.Time `json:"started_at"`
	ExpiresAt               time.Time `json:"expires_at"`
	CreatedAt               time.Time `json:"created_at"`
}

// PostChannel stores an optional channel where the bot posts content for a group.
// If no channel is set, posts go to the group chat itself.
type PostChannel struct {
	ID        int64     `json:"id"`
	ChatID    int64     `json:"chat_id"`    // The group config this belongs to
	ChannelID int64     `json:"channel_id"` // Telegram channel/chat to post to (can be the group itself)
	Title     string    `json:"title"`      // Display name
	CreatedAt time.Time `json:"created_at"`
}

// ScheduledPost tracks scheduled automatic posting configurations for a group.
type ScheduledPost struct {
	ID            int64     `json:"id"`
	ChatID        int64     `json:"chat_id"`        // The group config this belongs to
	IntervalHours int       `json:"interval_hours"` // How often to post (in hours, e.g. 24 = daily)
	IsActive      bool      `json:"is_active"`      // Whether this schedule is active
	LastPostedAt  time.Time `json:"last_posted_at"` // When the last scheduled post was sent
	NextPostAt    time.Time `json:"next_post_at"`   // When the next post should be sent
	CreatedAt     time.Time `json:"created_at"`
}

// GeneratedPost stores a record of every post the bot has generated and sent.
type GeneratedPost struct {
	ID        int64     `json:"id"`
	ChatID    int64     `json:"chat_id"`    // Group config it belongs to
	ChannelID int64     `json:"channel_id"` // Where it was posted
	Source    string    `json:"source"`     // "manual" or "scheduled"
	Query     string    `json:"query"`      // The link/keyword/topic that triggered the post
	Content   string    `json:"content"`    // The generated post text
	MessageID int       `json:"message_id"` // Telegram message ID of the posted message
	CreatedAt time.Time `json:"created_at"`
}

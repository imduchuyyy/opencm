package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/imduchuyyy/opencm/internal/database"
	"github.com/imduchuyyy/opencm/internal/llm"
)

// Executor handles executing tool calls from the AI agent
type Executor struct {
	bot     *tgbotapi.BotAPI
	cfg     *database.AgentConfig
	db      *database.DB
	botID   int64
	ownerID int64
}

func NewExecutor(bot *tgbotapi.BotAPI, cfg *database.AgentConfig, db *database.DB, botID, ownerID int64) *Executor {
	return &Executor{bot: bot, cfg: cfg, db: db, botID: botID, ownerID: ownerID}
}

// GetAvailableTools returns tool definitions based on agent permissions
func GetAvailableTools(cfg *database.AgentConfig) []llm.ToolDef {
	var tools []llm.ToolDef

	// skip - always available, allows AI to decide not to act
	tools = append(tools, llm.ToolDef{
		Name:        "skip",
		Description: "Skip these messages without taking any action. Use this when the messages don't require a response or action from you.",
		Parameters: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	})

	if cfg.CanReply {
		tools = append(tools, llm.ToolDef{
			Name:        "send_message",
			Description: "Send a text message to a chat. Use this to reply to users, answer questions, or participate in conversations.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"chat_id": map[string]interface{}{
						"type":        "integer",
						"description": "The chat ID to send the message to",
					},
					"text": map[string]interface{}{
						"type":        "string",
						"description": "The message text to send. Supports Markdown formatting.",
					},
					"reply_to_message_id": map[string]interface{}{
						"type":        "integer",
						"description": "Optional. Message ID to reply to.",
					},
				},
				"required": []string{"chat_id", "text"},
			},
		})
	}

	if cfg.CanBan {
		tools = append(tools, llm.ToolDef{
			Name:        "ban_member",
			Description: "Ban a user from the chat. Only use this for clear violations like spam, harassment, or rule-breaking.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"chat_id": map[string]interface{}{
						"type":        "integer",
						"description": "The chat ID",
					},
					"user_id": map[string]interface{}{
						"type":        "integer",
						"description": "The user ID to ban",
					},
				},
				"required": []string{"chat_id", "user_id"},
			},
		})

		tools = append(tools, llm.ToolDef{
			Name:        "unban_member",
			Description: "Unban a previously banned user from the chat.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"chat_id": map[string]interface{}{
						"type":        "integer",
						"description": "The chat ID",
					},
					"user_id": map[string]interface{}{
						"type":        "integer",
						"description": "The user ID to unban",
					},
				},
				"required": []string{"chat_id", "user_id"},
			},
		})

		tools = append(tools, llm.ToolDef{
			Name:        "mute_member",
			Description: "Restrict a user from sending messages for a duration. Use for minor violations or cooling off.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"chat_id": map[string]interface{}{
						"type":        "integer",
						"description": "The chat ID",
					},
					"user_id": map[string]interface{}{
						"type":        "integer",
						"description": "The user ID to mute",
					},
					"duration_seconds": map[string]interface{}{
						"type":        "integer",
						"description": "Duration in seconds (0 = forever, 30-366 days range for timed)",
					},
				},
				"required": []string{"chat_id", "user_id", "duration_seconds"},
			},
		})
	}

	if cfg.CanPin {
		tools = append(tools, llm.ToolDef{
			Name:        "pin_message",
			Description: "Pin a message in the chat so it stays visible at the top.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"chat_id": map[string]interface{}{
						"type":        "integer",
						"description": "The chat ID",
					},
					"message_id": map[string]interface{}{
						"type":        "integer",
						"description": "The message ID to pin",
					},
				},
				"required": []string{"chat_id", "message_id"},
			},
		})

		tools = append(tools, llm.ToolDef{
			Name:        "unpin_message",
			Description: "Unpin a message in the chat.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"chat_id": map[string]interface{}{
						"type":        "integer",
						"description": "The chat ID",
					},
					"message_id": map[string]interface{}{
						"type":        "integer",
						"description": "The message ID to unpin",
					},
				},
				"required": []string{"chat_id", "message_id"},
			},
		})
	}

	if cfg.CanPoll {
		tools = append(tools, llm.ToolDef{
			Name:        "send_poll",
			Description: "Send a poll to the chat for members to vote on.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"chat_id": map[string]interface{}{
						"type":        "integer",
						"description": "The chat ID",
					},
					"question": map[string]interface{}{
						"type":        "string",
						"description": "The poll question",
					},
					"options": map[string]interface{}{
						"type":        "array",
						"description": "Poll options (2-10 options)",
						"items":       map[string]interface{}{"type": "string"},
					},
					"is_anonymous": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether the poll is anonymous (default true)",
					},
					"allows_multiple_answers": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether multiple answers are allowed",
					},
				},
				"required": []string{"chat_id", "question", "options"},
			},
		})
	}

	if cfg.CanReact {
		tools = append(tools, llm.ToolDef{
			Name:        "set_reaction",
			Description: "Set a reaction emoji on a message.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"chat_id": map[string]interface{}{
						"type":        "integer",
						"description": "The chat ID",
					},
					"message_id": map[string]interface{}{
						"type":        "integer",
						"description": "The message ID to react to",
					},
					"emoji": map[string]interface{}{
						"type":        "string",
						"description": "The emoji to react with (e.g. '👍', '❤️', '🔥', '👀', '🎉')",
					},
				},
				"required": []string{"chat_id", "message_id", "emoji"},
			},
		})
	}

	if cfg.CanDelete {
		tools = append(tools, llm.ToolDef{
			Name:        "delete_message",
			Description: "Delete a message from the chat. Use for spam or inappropriate content.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"chat_id": map[string]interface{}{
						"type":        "integer",
						"description": "The chat ID",
					},
					"message_id": map[string]interface{}{
						"type":        "integer",
						"description": "The message ID to delete",
					},
				},
				"required": []string{"chat_id", "message_id"},
			},
		})
	}

	// Web viewing tool - always available
	tools = append(tools, llm.ToolDef{
		Name:        "view_website",
		Description: "Fetch and read the content of a URL/website that was shared in the chat. Use this to understand links shared by members.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"url": map[string]interface{}{
					"type":        "string",
					"description": "The URL to fetch and read",
				},
			},
			"required": []string{"url"},
		},
	})

	// Config tools - always available, admin-only enforcement is in the executor
	tools = append(tools, llm.ToolDef{
		Name:        "get_config",
		Description: "Get the current bot configuration. Returns system_prompt, bio, topics, chat_style, message_examples, model, and permission settings.",
		Parameters: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	})

	tools = append(tools, llm.ToolDef{
		Name:        "set_config",
		Description: "Update a bot configuration field. Only the group admin can use this. Available fields: system_prompt, bio, topics, chat_style, message_examples, model. For permissions use: can_reply, can_ban, can_pin, can_poll, can_react, can_delete (values: true/false).",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"field": map[string]interface{}{
					"type":        "string",
					"description": "The config field to update (e.g. system_prompt, bio, topics, chat_style, message_examples, model, can_reply, can_ban, can_pin, can_poll, can_react, can_delete)",
				},
				"value": map[string]interface{}{
					"type":        "string",
					"description": "The new value for the field. For boolean permission fields, use 'true' or 'false'.",
				},
				"requested_by_user_id": map[string]interface{}{
					"type":        "integer",
					"description": "The user ID of the person requesting the config change. This MUST be the UserID from the message.",
				},
			},
			"required": []string{"field", "value", "requested_by_user_id"},
		},
	})

	return tools
}

// Execute runs a tool call and returns the result as a string
func (e *Executor) Execute(tc llm.ToolCall) (string, error) {
	switch tc.Name {
	case "skip":
		return "Skipped.", nil
	case "send_message":
		return e.sendMessage(tc.Arguments)
	case "ban_member":
		return e.banMember(tc.Arguments)
	case "unban_member":
		return e.unbanMember(tc.Arguments)
	case "mute_member":
		return e.muteMember(tc.Arguments)
	case "pin_message":
		return e.pinMessage(tc.Arguments)
	case "unpin_message":
		return e.unpinMessage(tc.Arguments)
	case "send_poll":
		return e.sendPoll(tc.Arguments)
	case "set_reaction":
		return e.setReaction(tc.Arguments)
	case "delete_message":
		return e.deleteMessage(tc.Arguments)
	case "view_website":
		return e.viewWebsite(tc.Arguments)
	case "get_config":
		return e.getConfig()
	case "set_config":
		return e.setConfig(tc.Arguments)
	default:
		return "", fmt.Errorf("unknown tool: %s", tc.Name)
	}
}

func (e *Executor) sendMessage(args map[string]interface{}) (string, error) {
	chatID := int64(args["chat_id"].(float64))
	text := args["text"].(string)

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"

	if replyTo, ok := args["reply_to_message_id"]; ok {
		msg.ReplyToMessageID = int(replyTo.(float64))
	}

	sent, err := e.bot.Send(msg)
	if err != nil {
		// Retry without markdown if it fails
		msg.ParseMode = ""
		sent, err = e.bot.Send(msg)
		if err != nil {
			return "", fmt.Errorf("send message: %w", err)
		}
	}

	// Save the bot's own message to DB for context history
	if e.db != nil {
		botUser := e.bot.Self
		replyToID := 0
		if replyTo, ok := args["reply_to_message_id"]; ok {
			replyToID = int(replyTo.(float64))
		}
		dbMsg := &database.Message{
			BotID:            e.botID,
			ChatID:           chatID,
			ChatType:         "group",
			MessageID:        sent.MessageID,
			ReplyToMessageID: replyToID,
			FromUserID:       botUser.ID,
			FromUsername:     botUser.UserName,
			FromFirstName:    botUser.FirstName,
			Text:             text,
			IsProcessed:      true,
		}
		e.db.SaveMessage(dbMsg)
	}

	return fmt.Sprintf("Message sent (ID: %d)", sent.MessageID), nil
}

func (e *Executor) banMember(args map[string]interface{}) (string, error) {
	chatID := int64(args["chat_id"].(float64))
	userID := int64(args["user_id"].(float64))

	cfg := tgbotapi.BanChatMemberConfig{
		ChatMemberConfig: tgbotapi.ChatMemberConfig{
			ChatID: chatID,
			UserID: userID,
		},
	}
	_, err := e.bot.Request(cfg)
	if err != nil {
		return "", fmt.Errorf("ban member: %w", err)
	}
	return fmt.Sprintf("User %d banned from chat %d", userID, chatID), nil
}

func (e *Executor) unbanMember(args map[string]interface{}) (string, error) {
	chatID := int64(args["chat_id"].(float64))
	userID := int64(args["user_id"].(float64))

	cfg := tgbotapi.UnbanChatMemberConfig{
		ChatMemberConfig: tgbotapi.ChatMemberConfig{
			ChatID: chatID,
			UserID: userID,
		},
		OnlyIfBanned: true,
	}
	_, err := e.bot.Request(cfg)
	if err != nil {
		return "", fmt.Errorf("unban member: %w", err)
	}
	return fmt.Sprintf("User %d unbanned from chat %d", userID, chatID), nil
}

func (e *Executor) muteMember(args map[string]interface{}) (string, error) {
	chatID := int64(args["chat_id"].(float64))
	userID := int64(args["user_id"].(float64))
	duration := int64(args["duration_seconds"].(float64))

	permissions := tgbotapi.ChatPermissions{
		CanSendMessages:       false,
		CanSendMediaMessages:  false,
		CanSendPolls:          false,
		CanSendOtherMessages:  false,
		CanAddWebPagePreviews: false,
	}

	cfg := tgbotapi.RestrictChatMemberConfig{
		ChatMemberConfig: tgbotapi.ChatMemberConfig{
			ChatID: chatID,
			UserID: userID,
		},
		Permissions: &permissions,
	}
	if duration > 0 {
		cfg.UntilDate = int64(duration) + jsonTimeNow()
	}

	_, err := e.bot.Request(cfg)
	if err != nil {
		return "", fmt.Errorf("mute member: %w", err)
	}
	return fmt.Sprintf("User %d muted in chat %d for %d seconds", userID, chatID, duration), nil
}

func (e *Executor) pinMessage(args map[string]interface{}) (string, error) {
	chatID := int64(args["chat_id"].(float64))
	messageID := int(args["message_id"].(float64))

	cfg := tgbotapi.PinChatMessageConfig{
		ChatID:    chatID,
		MessageID: messageID,
	}
	_, err := e.bot.Request(cfg)
	if err != nil {
		return "", fmt.Errorf("pin message: %w", err)
	}
	return fmt.Sprintf("Message %d pinned in chat %d", messageID, chatID), nil
}

func (e *Executor) unpinMessage(args map[string]interface{}) (string, error) {
	chatID := int64(args["chat_id"].(float64))
	messageID := int(args["message_id"].(float64))

	cfg := tgbotapi.UnpinChatMessageConfig{
		ChatID:    chatID,
		MessageID: messageID,
	}
	_, err := e.bot.Request(cfg)
	if err != nil {
		return "", fmt.Errorf("unpin message: %w", err)
	}
	return fmt.Sprintf("Message %d unpinned in chat %d", messageID, chatID), nil
}

func (e *Executor) sendPoll(args map[string]interface{}) (string, error) {
	chatID := int64(args["chat_id"].(float64))
	question := args["question"].(string)

	optionsRaw := args["options"].([]interface{})
	var options []string
	for _, o := range optionsRaw {
		options = append(options, o.(string))
	}

	poll := tgbotapi.NewPoll(chatID, question, options...)

	if anon, ok := args["is_anonymous"]; ok {
		isAnon := anon.(bool)
		poll.IsAnonymous = isAnon
	}
	if multi, ok := args["allows_multiple_answers"]; ok {
		poll.AllowsMultipleAnswers = multi.(bool)
	}

	sent, err := e.bot.Send(poll)
	if err != nil {
		return "", fmt.Errorf("send poll: %w", err)
	}
	return fmt.Sprintf("Poll sent (Message ID: %d)", sent.MessageID), nil
}

func (e *Executor) setReaction(args map[string]interface{}) (string, error) {
	chatID := int64(args["chat_id"].(float64))
	messageID := int(args["message_id"].(float64))
	emoji := args["emoji"].(string)

	// Use raw API call for reactions since the library may not have direct support
	params := tgbotapi.Params{}
	params.AddFirstValid("chat_id", chatID)
	params.AddNonZero("message_id", messageID)

	reaction := []map[string]interface{}{
		{
			"type":  "emoji",
			"emoji": emoji,
		},
	}
	reactionJSON, _ := json.Marshal(reaction)
	params["reaction"] = string(reactionJSON)

	_, err := e.bot.MakeRequest("setMessageReaction", params)
	if err != nil {
		return "", fmt.Errorf("set reaction: %w", err)
	}
	return fmt.Sprintf("Reaction %s set on message %d", emoji, messageID), nil
}

func (e *Executor) deleteMessage(args map[string]interface{}) (string, error) {
	chatID := int64(args["chat_id"].(float64))
	messageID := int(args["message_id"].(float64))

	cfg := tgbotapi.NewDeleteMessage(chatID, messageID)
	_, err := e.bot.Request(cfg)
	if err != nil {
		return "", fmt.Errorf("delete message: %w", err)
	}
	return fmt.Sprintf("Message %d deleted from chat %d", messageID, chatID), nil
}

func (e *Executor) viewWebsite(args map[string]interface{}) (string, error) {
	url := args["url"].(string)
	if !strings.HasPrefix(url, "http") {
		url = "https://" + url
	}

	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; OpenCM Bot/1.0)")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch URL: %w", err)
	}
	defer resp.Body.Close()

	// Limit reading to 50KB
	body, err := io.ReadAll(io.LimitReader(resp.Body, 50*1024))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	content := string(body)
	content = stripHTMLTags(content)

	if len(content) > 5000 {
		content = content[:5000] + "\n... (truncated)"
	}

	return fmt.Sprintf("Website content from %s:\n%s", url, content), nil
}

// ----- Config tools -----

func (e *Executor) getConfig() (string, error) {
	cfg, err := e.db.GetAgentConfig(e.botID)
	if err != nil {
		return "", fmt.Errorf("get config: %w", err)
	}

	result := fmt.Sprintf("Current bot configuration:\n"+
		"- system_prompt: %s\n"+
		"- bio: %s\n"+
		"- topics: %s\n"+
		"- chat_style: %s\n"+
		"- message_examples: %s\n"+
		"- model: %s\n"+
		"- can_reply: %v\n"+
		"- can_ban: %v\n"+
		"- can_pin: %v\n"+
		"- can_poll: %v\n"+
		"- can_react: %v\n"+
		"- can_delete: %v",
		truncateStr(cfg.SystemPrompt, 200),
		truncateStr(cfg.Bio, 200),
		truncateStr(cfg.Topics, 200),
		truncateStr(cfg.ChatStyle, 200),
		truncateStr(cfg.MessageExamples, 200),
		cfg.Model,
		cfg.CanReply, cfg.CanBan, cfg.CanPin, cfg.CanPoll, cfg.CanReact, cfg.CanDelete,
	)
	return result, nil
}

func (e *Executor) setConfig(args map[string]interface{}) (string, error) {
	field, _ := args["field"].(string)
	value, _ := args["value"].(string)
	requestedBy := int64(0)
	if uid, ok := args["requested_by_user_id"]; ok {
		requestedBy = int64(uid.(float64))
	}

	// Check admin permission
	if requestedBy != e.ownerID {
		return "Permission denied. Only the group admin can change bot settings.", nil
	}

	// Permission fields (boolean)
	boolFields := map[string]bool{
		"can_reply": true, "can_ban": true, "can_pin": true,
		"can_poll": true, "can_react": true, "can_delete": true,
	}

	if boolFields[field] {
		boolVal := strings.ToLower(value) == "true" || value == "1" || strings.ToLower(value) == "on"
		if err := e.db.UpdateAgentConfigBool(e.botID, field, boolVal); err != nil {
			return "", fmt.Errorf("update config: %w", err)
		}
		return fmt.Sprintf("Configuration updated: %s = %v", field, boolVal), nil
	}

	// Text fields
	textFields := map[string]bool{
		"system_prompt": true, "bio": true, "topics": true,
		"message_examples": true, "chat_style": true, "model": true,
	}

	if textFields[field] {
		if err := e.db.UpdateAgentConfigField(e.botID, field, value); err != nil {
			return "", fmt.Errorf("update config: %w", err)
		}
		return fmt.Sprintf("Configuration updated: %s", field), nil
	}

	return fmt.Sprintf("Unknown config field: %s. Available fields: system_prompt, bio, topics, chat_style, message_examples, model, can_reply, can_ban, can_pin, can_poll, can_react, can_delete", field), nil
}

// ----- Helpers -----

// stripHTMLTags removes HTML tags from a string (basic implementation)
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
	if s == "" {
		return "(not set)"
	}
	return s
}

func jsonTimeNow() int64 {
	return time.Now().Unix()
}

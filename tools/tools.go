package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/imduchuyyy/opencm/database"
	"github.com/imduchuyyy/opencm/llm"
)

// Executor handles executing tool calls from the AI agent
type Executor struct {
	bot              *tgbotapi.BotAPI
	cfg              *database.GroupConfig
	db               *database.DB
	chatID           int64 // The group chat being processed
	langSearchAPIKey string
}

func NewExecutor(bot *tgbotapi.BotAPI, cfg *database.GroupConfig, db *database.DB, chatID int64, langSearchAPIKey string) *Executor {
	return &Executor{bot: bot, cfg: cfg, db: db, chatID: chatID, langSearchAPIKey: langSearchAPIKey}
}

// GetAvailableTools returns tool definitions based on group permissions
func GetAvailableTools(cfg *database.GroupConfig) []llm.ToolDef {
	var tools []llm.ToolDef

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

	// Web tools - always available
	tools = append(tools, llm.ToolDef{
		Name:        "web_search",
		Description: "Search the web for real-time information. Use this for current events, recent data, or any information beyond your knowledge cutoff. The current year is " + fmt.Sprintf("%d", time.Now().Year()) + ".",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "The search query",
				},
				"num_results": map[string]interface{}{
					"type":        "integer",
					"description": "Number of results to return (default: 5, max: 10)",
				},
			},
			"required": []string{"query"},
		},
	})

	tools = append(tools, llm.ToolDef{
		Name:        "web_fetch",
		Description: "Fetch and read the content of a URL/website. Returns the page content as plain text. Use this to read links shared in chat or referenced in search results.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"url": map[string]interface{}{
					"type":        "string",
					"description": "The URL to fetch (must start with http:// or https://)",
				},
			},
			"required": []string{"url"},
		},
	})

	// Config tools - always available, admin enforcement happens in executor via Telegram API
	tools = append(tools, llm.ToolDef{
		Name:        "get_config",
		Description: "Get the current bot configuration for this group. Returns system_prompt, bio, topics, chat_style, message_examples, model, and permission settings.",
		Parameters: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	})

	tools = append(tools, llm.ToolDef{
		Name:        "set_config",
		Description: "Update a bot configuration field for this group. Only group admins can use this. Available fields: system_prompt, bio, topics, chat_style, message_examples, model. For permissions use: can_reply, can_ban, can_pin, can_poll, can_delete (values: true/false).",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"field": map[string]interface{}{
					"type":        "string",
					"description": "The config field to update (e.g. system_prompt, bio, topics, chat_style, message_examples, model, can_reply, can_ban, can_pin, can_poll, can_delete)",
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
	case "delete_message":
		return e.deleteMessage(tc.Arguments)
	case "web_search":
		return e.webSearch(tc.Arguments)
	case "web_fetch":
		return e.webFetch(tc.Arguments)
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
		cfg.UntilDate = duration + time.Now().Unix()
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

func (e *Executor) webSearch(args map[string]interface{}) (string, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return "", fmt.Errorf("query is required")
	}

	numResults := 5
	if n, ok := args["num_results"].(float64); ok && int(n) > 0 {
		numResults = int(n)
		if numResults > 10 {
			numResults = 10
		}
	}

	if e.langSearchAPIKey == "" {
		return "Web search is not configured (missing LANGSEARCH_API_KEY).", nil
	}

	requestBody := map[string]interface{}{
		"query":     query,
		"count":     numResults,
		"summary":   true,
		"freshness": "noLimit",
	}

	bodyJSON, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	client := &http.Client{Timeout: 25 * time.Second}
	req, err := http.NewRequest("POST", "https://api.langsearch.com/v1/web-search", strings.NewReader(string(bodyJSON)))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.langSearchAPIKey)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("search request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("search error (%d): %s", resp.StatusCode, string(body))
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 200*1024))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	var searchResp struct {
		Code int `json:"code"`
		Data struct {
			WebPages struct {
				Value []struct {
					Name    string `json:"name"`
					URL     string `json:"url"`
					Snippet string `json:"snippet"`
					Summary string `json:"summary"`
				} `json:"value"`
			} `json:"webPages"`
		} `json:"data"`
	}

	if err := json.Unmarshal(respBody, &searchResp); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	if len(searchResp.Data.WebPages.Value) == 0 {
		return "No search results found. Try a different query.", nil
	}

	var results []string
	for i, page := range searchResp.Data.WebPages.Value {
		entry := fmt.Sprintf("%d. %s\n   URL: %s", i+1, page.Name, page.URL)
		if page.Summary != "" {
			summary := page.Summary
			if len(summary) > 1000 {
				summary = summary[:1000] + "..."
			}
			entry += fmt.Sprintf("\n   Summary: %s", summary)
		} else if page.Snippet != "" {
			entry += fmt.Sprintf("\n   Snippet: %s", page.Snippet)
		}
		results = append(results, entry)
	}

	output := fmt.Sprintf("Search results for \"%s\":\n\n%s", query, strings.Join(results, "\n\n"))
	if len(output) > 8000 {
		output = output[:8000] + "\n... (truncated)"
	}
	return output, nil
}

func (e *Executor) webFetch(args map[string]interface{}) (string, error) {
	url, _ := args["url"].(string)
	if url == "" {
		return "", fmt.Errorf("url is required")
	}

	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "https://" + url
	}

	jinaURL := "https://r.jina.ai/" + url

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("GET", jinaURL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "text/plain")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("request failed with status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	content := strings.TrimSpace(string(body))

	if len(content) > 8000 {
		content = content[:8000] + "\n... (truncated)"
	}

	return fmt.Sprintf("Content from %s:\n%s", url, content), nil
}

// ----- Config tools -----

func (e *Executor) getConfig() (string, error) {
	cfg, err := e.db.GetGroupConfig(e.chatID)
	if err != nil {
		return "", fmt.Errorf("get config: %w", err)
	}

	result := fmt.Sprintf("Current bot configuration for this group:\n"+
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
		"- can_delete: %v",
		truncateStr(cfg.SystemPrompt, 200),
		truncateStr(cfg.Bio, 200),
		truncateStr(cfg.Topics, 200),
		truncateStr(cfg.ChatStyle, 200),
		truncateStr(cfg.MessageExamples, 200),
		cfg.Model,
		cfg.CanReply, cfg.CanBan, cfg.CanPin, cfg.CanPoll, cfg.CanDelete,
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

	// Check admin permission via Telegram API
	if requestedBy == 0 {
		return "Permission denied. Could not identify the requesting user.", nil
	}

	admins, err := e.bot.GetChatAdministrators(tgbotapi.ChatAdministratorsConfig{
		ChatConfig: tgbotapi.ChatConfig{ChatID: e.chatID},
	})
	if err != nil {
		return "Permission denied. Could not verify admin status.", nil
	}

	isAdmin := false
	for _, admin := range admins {
		if admin.User.ID == requestedBy {
			isAdmin = true
			break
		}
	}
	if !isAdmin {
		return "Permission denied. Only group admins can change bot settings.", nil
	}

	// Permission fields (boolean)
	boolFields := map[string]bool{
		"can_reply": true, "can_ban": true, "can_pin": true,
		"can_poll": true, "can_delete": true,
	}

	if boolFields[field] {
		boolVal := strings.ToLower(value) == "true" || value == "1" || strings.ToLower(value) == "on"
		if err := e.db.UpdateGroupConfigBool(e.chatID, field, boolVal); err != nil {
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
		if err := e.db.UpdateGroupConfigField(e.chatID, field, value); err != nil {
			return "", fmt.Errorf("update config: %w", err)
		}
		return fmt.Sprintf("Configuration updated: %s", field), nil
	}

	return fmt.Sprintf("Unknown config field: %s. Available fields: system_prompt, bio, topics, chat_style, message_examples, model, can_reply, can_ban, can_pin, can_poll, can_delete", field), nil
}

// ----- Helpers -----

func truncateStr(s string, max int) string {
	if len(s) > max {
		return s[:max] + "..."
	}
	if s == "" {
		return "(not set)"
	}
	return s
}

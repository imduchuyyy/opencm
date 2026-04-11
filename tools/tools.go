package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/imduchuyyy/opencm/database"
	"github.com/imduchuyyy/opencm/llm"
	"github.com/imduchuyyy/opencm/plan"
)

// Executor handles executing tool calls from the AI agent
type Executor struct {
	bot              *tgbotapi.BotAPI
	db               *database.DB
	chatID           int64 // The group chat being processed
	langSearchAPIKey string
	openaiAPIKey     string // For sub-agent (search_chat_history)
	limits           plan.Limits
}

func NewExecutor(bot *tgbotapi.BotAPI, db *database.DB, chatID int64, langSearchAPIKey, openaiAPIKey string, limits plan.Limits) *Executor {
	return &Executor{bot: bot, db: db, chatID: chatID, langSearchAPIKey: langSearchAPIKey, openaiAPIKey: openaiAPIKey, limits: limits}
}

// GetAvailableTools returns all tool definitions for the agent.
// All tools are always included so the LLM knows they exist.
// Plan-based access control is enforced at execution time in the Executor.
func GetAvailableTools(limits plan.Limits) []llm.ToolDef {
	var tools []llm.ToolDef

	// send_poll - always available
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

	// Web tools - always included so the LLM knows they exist.
	// Plan enforcement happens in the Executor at execution time.
	tools = append(tools, llm.ToolDef{
		Name:        "web_search",
		Description: "Search the web for real-time information. Use this for current events, recent data, or any information beyond your knowledge cutoff. The current year is " + fmt.Sprintf("%d", time.Now().Year()) + ". Note: This tool requires a Pro plan or higher. If the group is on the Free plan, the tool will return an error explaining the upgrade path.",
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
		Description: "Fetch and read the content of a URL/website. Returns the page content as plain text. Use this to read links shared in chat or referenced in search results. Note: This tool requires a Pro plan or higher. If the group is on the Free plan, the tool will return an error explaining the upgrade path.",
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
		Name:        "search_chat_history",
		Description: "Search recent chat history for messages relevant to a topic or question. Use this when you need context from previous conversations to answer a question properly. A sub-agent will sift through recent messages and return only the relevant ones. Do NOT use this for every message - only when you genuinely need historical context that isn't in the current conversation.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "A description of what to search for in chat history (e.g. 'discussion about token launch date', 'questions about staking rewards')",
				},
			},
			"required": []string{"query"},
		},
	})

	tools = append(tools, llm.ToolDef{
		Name:        "get_config",
		Description: "Get the current bot configuration for this group. Returns system_prompt, bio, topics, chat_style, and message_examples.",
		Parameters: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	})

	tools = append(tools, llm.ToolDef{
		Name:        "set_config",
		Description: "Update a bot configuration field for this group. Only group admins can use this. Available fields: system_prompt, bio, topics, chat_style, message_examples.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"field": map[string]interface{}{
					"type":        "string",
					"description": "The config field to update (e.g. system_prompt, bio, topics, chat_style, message_examples)",
				},
				"value": map[string]interface{}{
					"type":        "string",
					"description": "The new value for the field.",
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
	case "send_poll":
		return e.sendPoll(tc.Arguments)
	case "web_search":
		return e.webSearch(tc.Arguments)
	case "web_fetch":
		return e.webFetch(tc.Arguments)
	case "search_chat_history":
		return e.searchChatHistory(tc.Arguments)
	case "get_config":
		return e.getConfig()
	case "set_config":
		return e.setConfig(tc.Arguments)
	default:
		return "", fmt.Errorf("unknown tool: %s", tc.Name)
	}
}

func (e *Executor) sendPoll(args map[string]interface{}) (string, error) {
	chatIDFloat, ok := args["chat_id"].(float64)
	if !ok {
		return "", fmt.Errorf("chat_id is required and must be a number")
	}
	chatID := int64(chatIDFloat)

	question, ok := args["question"].(string)
	if !ok || question == "" {
		return "", fmt.Errorf("question is required and must be a string")
	}

	optionsRaw, ok := args["options"].([]interface{})
	if !ok || len(optionsRaw) < 2 {
		return "", fmt.Errorf("options is required and must be an array with at least 2 items")
	}
	var options []string
	for _, o := range optionsRaw {
		if s, ok := o.(string); ok {
			options = append(options, s)
		}
	}
	if len(options) < 2 {
		return "", fmt.Errorf("at least 2 valid string options are required")
	}

	poll := tgbotapi.NewPoll(chatID, question, options...)

	if anon, ok := args["is_anonymous"].(bool); ok {
		poll.IsAnonymous = anon
	}
	if multi, ok := args["allows_multiple_answers"].(bool); ok {
		poll.AllowsMultipleAnswers = multi
	}

	sent, err := e.bot.Send(poll)
	if err != nil {
		return "", fmt.Errorf("send poll: %w", err)
	}
	return fmt.Sprintf("Poll sent (Message ID: %d)", sent.MessageID), nil
}

func (e *Executor) webSearch(args map[string]interface{}) (string, error) {
	if !e.limits.WebSearch {
		return "ERROR: Web search is not available on this group's current plan (Free). The group admin needs to upgrade to the Pro plan or higher to enable web search. Do NOT retry this tool - instead, answer the user's question using your own knowledge and let them know that web search requires a plan upgrade.", nil
	}

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
	if !e.limits.WebFetch {
		return "ERROR: Web fetch is not available on this group's current plan (Free). The group admin needs to upgrade to the Pro plan or higher to enable reading web pages. Do NOT retry this tool - instead, let the user know that fetching URLs requires a plan upgrade.", nil
	}

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
		"- message_examples: %s",
		truncateStr(cfg.SystemPrompt, 200),
		truncateStr(cfg.Bio, 200),
		truncateStr(cfg.Topics, 200),
		truncateStr(cfg.ChatStyle, 200),
		truncateStr(cfg.MessageExamples, 200),
	)
	return result, nil
}

func (e *Executor) setConfig(args map[string]interface{}) (string, error) {
	field, _ := args["field"].(string)
	value, _ := args["value"].(string)
	requestedBy := int64(0)
	if uid, ok := args["requested_by_user_id"].(float64); ok {
		requestedBy = int64(uid)
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

	// Text fields
	textFields := map[string]bool{
		"system_prompt": true, "bio": true, "topics": true,
		"message_examples": true, "chat_style": true,
	}

	if textFields[field] {
		if err := e.db.UpdateGroupConfigField(e.chatID, field, value); err != nil {
			return "", fmt.Errorf("update config: %w", err)
		}
		return fmt.Sprintf("Configuration updated: %s", field), nil
	}

	return fmt.Sprintf("Unknown config field: %s. Available fields: system_prompt, bio, topics, chat_style, message_examples", field), nil
}

// ----- Chat History Search (sub-agent) -----

func (e *Executor) searchChatHistory(args map[string]interface{}) (string, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return "", fmt.Errorf("query is required")
	}

	// Fetch recent messages from DB (last 50 with text content)
	msgs, err := e.db.SearchMessages(e.chatID, 50)
	if err != nil {
		return "", fmt.Errorf("search messages: %w", err)
	}
	if len(msgs) == 0 {
		return "No chat history available for this group.", nil
	}

	// Format messages for the sub-agent
	var lines []string
	for _, msg := range msgs {
		name := msg.FromFirstName
		if name == "" {
			name = msg.FromUsername
		}
		line := fmt.Sprintf("[%s, MsgID:%d] %s (@%s): %s",
			msg.CreatedAt.Format("2006-01-02 15:04"), msg.MessageID, name, msg.FromUsername, msg.Text)
		if msg.ReplyToMessageID > 0 {
			line = fmt.Sprintf("[%s, MsgID:%d, ReplyTo:%d] %s (@%s): %s",
				msg.CreatedAt.Format("2006-01-02 15:04"), msg.MessageID, msg.ReplyToMessageID, name, msg.FromUsername, msg.Text)
		}
		lines = append(lines, line)
	}
	chatLog := strings.Join(lines, "\n")

	// Call gpt-4o-mini as a sub-agent to filter for relevant messages
	subClient := llm.NewClient(e.openaiAPIKey, "gpt-4o-mini")

	systemPrompt := `You are a chat history search assistant. You will be given a chat log and a search query. Your job is to find and return ONLY the messages that are relevant to the query.

Rules:
- Return the relevant messages exactly as they appear (preserve the format)
- If multiple messages form a conversation thread about the topic, include all of them
- If no messages are relevant, say "No relevant messages found."
- Do NOT add commentary or explanations. Just return the matching messages.
- Return at most 15 messages to keep context manageable.`

	userMsg := fmt.Sprintf("Search query: %s\n\nChat log:\n%s", query, chatLog)
	userMessages := []llm.InputMessage{{Text: userMsg}}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := subClient.Chat(ctx, systemPrompt, userMessages, nil, "")
	if err != nil {
		return "", fmt.Errorf("sub-agent search: %w", err)
	}

	result := strings.TrimSpace(resp.Text)
	if result == "" {
		return "No relevant messages found in chat history.", nil
	}

	// Truncate if too long
	if len(result) > 4000 {
		result = result[:4000] + "\n... (truncated)"
	}

	return fmt.Sprintf("Chat history search results for \"%s\":\n\n%s", query, result), nil
}

// ----- Helpers -----

func truncateStr(s string, max int) string {
	if s == "" {
		return "(not set)"
	}
	runes := []rune(s)
	if len(runes) > max {
		return string(runes[:max]) + "..."
	}
	return s
}

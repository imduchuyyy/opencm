package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/imduchuyyy/opencm/database"
	"github.com/imduchuyyy/opencm/llm"
)

// generateAndSendPost researches a query (link or keyword), generates a post using LLM,
// and sends it to the group or configured channel. Returns the generated content or error.
func (a *Agent) generateAndSendPost(ctx context.Context, chatID int64, query, source string) (string, error) {
	groupCfg := a.getOrCreateGroupConfig(chatID)

	// Determine where to post
	targetChatID := chatID
	pc, err := a.db.GetPostChannel(chatID)
	if err == nil && pc != nil {
		targetChatID = pc.ChannelID
	}

	// Step 1: Research - search the web for the topic/link
	searchResults, err := a.researchQuery(ctx, query)
	if err != nil {
		return "", fmt.Errorf("research: %w", err)
	}

	// Step 2: If query is a URL, also fetch its content directly
	var urlContent string
	if strings.HasPrefix(query, "http://") || strings.HasPrefix(query, "https://") {
		urlContent, _ = a.fetchURL(query)
	}

	// Step 3: Generate post with LLM
	postContent, err := a.generatePostContent(ctx, groupCfg, query, searchResults, urlContent)
	if err != nil {
		return "", fmt.Errorf("generate post: %w", err)
	}

	// Step 4: Send to group/channel
	msgID, err := a.sendPost(targetChatID, postContent)
	if err != nil {
		return "", fmt.Errorf("send post: %w", err)
	}

	// Step 5: Save record
	gp := &database.GeneratedPost{
		ChatID:    chatID,
		ChannelID: targetChatID,
		Source:    source,
		Query:     query,
		Content:   postContent,
		MessageID: msgID,
	}
	if err := a.db.SaveGeneratedPost(gp); err != nil {
		log.Printf("[Posts] Error saving generated post record: %v", err)
	}

	return postContent, nil
}

// researchQuery uses LangSearch API to search the web for information about the query.
func (a *Agent) researchQuery(ctx context.Context, query string) (string, error) {
	apiKey := a.appConfig.LangSearchAPIKey
	if apiKey == "" {
		return "No search results available (search API not configured).", nil
	}

	// Build search query - if it's a URL, search for context about it
	searchQuery := query
	if strings.HasPrefix(query, "http://") || strings.HasPrefix(query, "https://") {
		searchQuery = query + " news analysis"
	}

	requestBody := map[string]interface{}{
		"query":     searchQuery,
		"count":     8,
		"summary":   true,
		"freshness": "day",
	}

	bodyJSON, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	client := &http.Client{Timeout: 25 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.langsearch.com/v1/web-search", strings.NewReader(string(bodyJSON)))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

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
		return "No search results found.", nil
	}

	var results []string
	for i, page := range searchResp.Data.WebPages.Value {
		entry := fmt.Sprintf("%d. %s\n   URL: %s", i+1, page.Name, page.URL)
		if page.Summary != "" {
			summary := page.Summary
			if len(summary) > 1500 {
				summary = summary[:1500] + "..."
			}
			entry += fmt.Sprintf("\n   Summary: %s", summary)
		} else if page.Snippet != "" {
			entry += fmt.Sprintf("\n   Snippet: %s", page.Snippet)
		}
		results = append(results, entry)
	}

	output := strings.Join(results, "\n\n")
	if len(output) > 10000 {
		output = output[:10000] + "\n... (truncated)"
	}
	return output, nil
}

// fetchURL fetches the content of a URL using Jina Reader.
func (a *Agent) fetchURL(url string) (string, error) {
	jinaURL := "https://r.jina.ai/" + url

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("GET", jinaURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/plain")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch failed with status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024))
	if err != nil {
		return "", err
	}

	content := strings.TrimSpace(string(body))
	if len(content) > 6000 {
		content = content[:6000] + "\n... (truncated)"
	}
	return content, nil
}

// generatePostContent uses LLM to write a post based on research results.
func (a *Agent) generatePostContent(ctx context.Context, groupCfg *database.GroupConfig, query, searchResults, urlContent string) (string, error) {
	client := a.llmClient

	// Build a focused system prompt for post generation
	var systemParts []string
	systemParts = append(systemParts, `You are a content writer for a Telegram community. Your job is to write an engaging, informative post based on the research provided.`)

	if groupCfg.Topics != "" {
		systemParts = append(systemParts, "Community topics: "+groupCfg.Topics)
	}
	if groupCfg.ChatStyle != "" {
		systemParts = append(systemParts, "Writing style: "+groupCfg.ChatStyle)
	}
	if groupCfg.Bio != "" {
		systemParts = append(systemParts, "Bot identity: "+groupCfg.Bio)
	}

	systemParts = append(systemParts, `Rules:
- Write a complete, ready-to-post Telegram message. This will be posted directly.
- Keep it concise but informative (2-5 short paragraphs max).
- Use Markdown formatting (bold, links, etc.) supported by Telegram.
- Include relevant source links where appropriate.
- Match the community's tone and style.
- Focus on what's interesting/important for the community.
- Do NOT include meta-commentary like "Here's a post about..." - just write the post itself.
- If the topic involves news/updates, lead with the key takeaway.
- End with a brief thought, opinion, or question to spark discussion if appropriate.`)

	systemPrompt := strings.Join(systemParts, "\n\n")

	// Build user message with all research
	var userParts []string
	userParts = append(userParts, fmt.Sprintf("Write a post about: %s", query))

	if searchResults != "" {
		userParts = append(userParts, fmt.Sprintf("Web search results:\n%s", searchResults))
	}
	if urlContent != "" {
		userParts = append(userParts, fmt.Sprintf("Content from the URL:\n%s", urlContent))
	}

	// Include recent posts to avoid repetition
	recentPosts, _ := a.db.GetRecentGeneratedPosts(groupCfg.ChatID, 3)
	if len(recentPosts) > 0 {
		var recentSummaries []string
		for _, rp := range recentPosts {
			summary := rp.Query
			if len(rp.Content) > 100 {
				summary += ": " + rp.Content[:100] + "..."
			}
			recentSummaries = append(recentSummaries, "- "+summary)
		}
		userParts = append(userParts, fmt.Sprintf("Recent posts (avoid repeating):\n%s", strings.Join(recentSummaries, "\n")))
	}

	userMsg := strings.Join(userParts, "\n\n---\n\n")
	userMessages := []llm.InputMessage{{Text: userMsg}}

	resp, err := client.Chat(ctx, systemPrompt, userMessages, nil, groupCfg.VectorStoreID)
	if err != nil {
		return "", fmt.Errorf("LLM chat: %w", err)
	}

	content := strings.TrimSpace(resp.Text)
	if content == "" {
		return "", fmt.Errorf("LLM returned empty response")
	}

	return content, nil
}

// sendPost sends the generated post to the target chat. Returns message ID.
func (a *Agent) sendPost(chatID int64, content string) (int, error) {
	msg := tgbotapi.NewMessage(chatID, content)
	msg.ParseMode = "Markdown"
	msg.DisableWebPagePreview = false

	sent, err := a.bot.Send(msg)
	if err != nil {
		// Retry without Markdown
		msg.ParseMode = ""
		sent, err = a.bot.Send(msg)
		if err != nil {
			return 0, fmt.Errorf("send post: %w", err)
		}
	}
	return sent.MessageID, nil
}

// generateScheduledPost creates and sends a post based on the group's configured topics.
func (a *Agent) generateScheduledPost(ctx context.Context, chatID int64) error {
	groupCfg := a.getOrCreateGroupConfig(chatID)

	topics := groupCfg.Topics
	if topics == "" {
		log.Printf("[Posts] No topics configured for group %d, skipping scheduled post", chatID)
		return nil
	}

	// Pick a topic-based search query. Use LLM to generate a timely search query from the topics.
	query, err := a.generateSearchQuery(ctx, groupCfg)
	if err != nil {
		return fmt.Errorf("generate search query: %w", err)
	}

	_, err = a.generateAndSendPost(ctx, chatID, query, "scheduled")
	return err
}

// generateSearchQuery uses a cheap LLM call to pick a timely, specific search query
// from the group's configured topics.
func (a *Agent) generateSearchQuery(ctx context.Context, groupCfg *database.GroupConfig) (string, error) {
	subClient := llm.NewClient(a.appConfig.OpenAIAPIKey, "gpt-4o-mini")

	systemPrompt := `You generate web search queries for a Telegram community bot. Given the community's topics, generate ONE specific, timely search query that would find interesting recent news or developments to post about.

Rules:
- Output ONLY the search query, nothing else
- Make it specific and current (include "2024" or "latest" or "today" if relevant)
- Focus on news, updates, developments - not tutorials or evergreen content
- Vary the topic - don't always pick the first one
- Prefer Twitter/X discussions when relevant (include "site:x.com" or "twitter" in some queries)`

	// Include recent posts so we don't repeat
	var recentContext string
	recentPosts, _ := a.db.GetRecentGeneratedPosts(groupCfg.ChatID, 5)
	if len(recentPosts) > 0 {
		var queries []string
		for _, rp := range recentPosts {
			queries = append(queries, "- "+rp.Query)
		}
		recentContext = "\n\nRecent queries (avoid repeating):\n" + strings.Join(queries, "\n")
	}

	userMsg := fmt.Sprintf("Community topics: %s\n\nToday's date: %s%s\n\nGenerate one search query:",
		groupCfg.Topics, time.Now().Format("January 2, 2006"), recentContext)

	userMessages := []llm.InputMessage{{Text: userMsg}}

	resp, err := subClient.Chat(ctx, systemPrompt, userMessages, nil, "")
	if err != nil {
		return "", fmt.Errorf("LLM: %w", err)
	}

	query := strings.TrimSpace(resp.Text)
	if query == "" {
		// Fallback: just use the topics directly
		query = "latest news " + groupCfg.Topics
	}

	// Clean up: remove quotes if the LLM wrapped the query
	query = strings.Trim(query, "\"'`")

	return query, nil
}

package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
)

// ToolCall represents a function call the AI wants to invoke
type ToolCall struct {
	CallID    string                 `json:"call_id"`
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

// Response from the LLM
type Response struct {
	Text      string     `json:"text"`
	ToolCalls []ToolCall `json:"tool_calls"`
	// ResponseID for multi-turn conversation continuation
	ResponseID string `json:"response_id"`
}

// ToolDef defines a function tool available to the AI
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ToolResult holds the result of executing a function tool
type ToolResult struct {
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

// InputMessage represents a user message with optional image attachments
type InputMessage struct {
	Text      string   // The text content of the message
	ImageURLs []string // Optional image URLs to include with the message
}

// Client wraps the OpenAI SDK
type Client struct {
	client *openai.Client
	model  string
}

// NewClient creates a new OpenAI client
func NewClient(apiKey, model string) *Client {
	client := openai.NewClient(
		option.WithAPIKey(apiKey),
	)
	return &Client{
		client: &client,
		model:  model,
	}
}

// Chat sends messages with tools to the Responses API and returns the result
func (c *Client) Chat(ctx context.Context, systemPrompt string, userMessages []InputMessage, tools []ToolDef, vectorStoreID string) (*Response, error) {
	// Build input items
	var inputItems []responses.ResponseInputItemUnionParam

	// System instruction as a developer message
	if systemPrompt != "" {
		inputItems = append(inputItems,
			responses.ResponseInputItemParamOfMessage(systemPrompt, responses.EasyInputMessageRoleDeveloper),
		)
	}

	// User messages (may contain images)
	for _, msg := range userMessages {
		if len(msg.ImageURLs) == 0 {
			// Text-only message
			inputItems = append(inputItems,
				responses.ResponseInputItemParamOfMessage(msg.Text, responses.EasyInputMessageRoleUser),
			)
		} else {
			// Multimodal message: text + images
			var contentParts responses.ResponseInputMessageContentListParam

			// Add text part
			if msg.Text != "" {
				contentParts = append(contentParts,
					responses.ResponseInputContentParamOfInputText(msg.Text),
				)
			}

			// Add image parts
			for _, imageURL := range msg.ImageURLs {
				contentParts = append(contentParts, responses.ResponseInputContentUnionParam{
					OfInputImage: &responses.ResponseInputImageParam{
						Detail:   responses.ResponseInputImageDetailAuto,
						ImageURL: param.NewOpt(imageURL),
					},
				})
			}

			inputItems = append(inputItems,
				responses.ResponseInputItemParamOfMessage(contentParts, responses.EasyInputMessageRoleUser),
			)
		}
	}

	// Build tools
	apiTools := c.buildTools(tools, vectorStoreID)

	// Make the API call
	params := responses.ResponseNewParams{
		Model: openai.ChatModel(c.model),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: inputItems,
		},
		Tools: apiTools,
	}

	resp, err := c.client.Responses.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("openai responses API: %w", err)
	}

	return c.parseResponse(resp)
}

// ContinueWithToolResults sends function call results back and gets the next response
func (c *Client) ContinueWithToolResults(ctx context.Context, previousResponseID string, toolResults []ToolResult, tools []ToolDef, vectorStoreID string) (*Response, error) {
	var inputItems []responses.ResponseInputItemUnionParam

	for _, tr := range toolResults {
		inputItems = append(inputItems, responses.ResponseInputItemUnionParam{
			OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
				CallID: tr.CallID,
				Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{
					OfString: param.NewOpt(tr.Output),
				},
			},
		})
	}

	apiTools := c.buildTools(tools, vectorStoreID)

	params := responses.ResponseNewParams{
		Model:              openai.ChatModel(c.model),
		PreviousResponseID: openai.String(previousResponseID),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: inputItems,
		},
		Tools: apiTools,
	}

	resp, err := c.client.Responses.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("openai continue response: %w", err)
	}

	return c.parseResponse(resp)
}

func (c *Client) buildTools(tools []ToolDef, vectorStoreID string) []responses.ToolUnionParam {
	var apiTools []responses.ToolUnionParam

	for _, t := range tools {
		apiTools = append(apiTools, responses.ToolUnionParam{
			OfFunction: &responses.FunctionToolParam{
				Name:        t.Name,
				Description: openai.String(t.Description),
				Parameters:  t.Parameters,
				Strict:      openai.Bool(false),
			},
		})
	}

	// Add file_search tool if vector store exists
	if vectorStoreID != "" {
		apiTools = append(apiTools, responses.ToolUnionParam{
			OfFileSearch: &responses.FileSearchToolParam{
				VectorStoreIDs: []string{vectorStoreID},
			},
		})
	}

	return apiTools
}

func (c *Client) parseResponse(resp *responses.Response) (*Response, error) {
	result := &Response{
		ResponseID: resp.ID,
	}

	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				if content.Type == "output_text" {
					result.Text += content.Text
				}
			}
		case "function_call":
			argsStr := item.Arguments.OfString
			var args map[string]interface{}
			if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
				args = map[string]interface{}{"raw": argsStr}
			}
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				CallID:    item.CallID,
				Name:      item.Name,
				Arguments: args,
			})
		case "file_search_call":
			log.Printf("[LLM] File search executed, queries: %v", item.Queries)
		}
	}

	return result, nil
}

// ----- Vector Store & File Management -----

// CreateVectorStore creates a new vector store for an agent
func (c *Client) CreateVectorStore(ctx context.Context, name string) (string, error) {
	vs, err := c.client.VectorStores.New(ctx, openai.VectorStoreNewParams{
		Name: openai.String(name),
	})
	if err != nil {
		return "", fmt.Errorf("create vector store: %w", err)
	}
	return vs.ID, nil
}

// UploadFileToVectorStore uploads a file to OpenAI and adds it to a vector store
func (c *Client) UploadFileToVectorStore(ctx context.Context, vectorStoreID string, filename string, content io.Reader) (string, error) {
	file, err := c.client.Files.New(ctx, openai.FileNewParams{
		File:    openai.File(content, filename, ""),
		Purpose: openai.FilePurposeAssistants,
	})
	if err != nil {
		return "", fmt.Errorf("upload file: %w", err)
	}

	_, err = c.client.VectorStores.Files.New(ctx, vectorStoreID, openai.VectorStoreFileNewParams{
		FileID: file.ID,
	})
	if err != nil {
		return "", fmt.Errorf("add file to vector store: %w", err)
	}

	return file.ID, nil
}

// UploadTextAsFile uploads text content as a file to a vector store
func (c *Client) UploadTextAsFile(ctx context.Context, vectorStoreID string, title string, content string) (string, error) {
	filename := strings.ReplaceAll(title, " ", "_") + ".txt"
	reader := strings.NewReader(content)
	return c.UploadFileToVectorStore(ctx, vectorStoreID, filename, reader)
}

// DeleteVectorStoreFile removes a file from a vector store and deletes the file
func (c *Client) DeleteVectorStoreFile(ctx context.Context, vectorStoreID, fileID string) error {
	_, err := c.client.VectorStores.Files.Delete(ctx, vectorStoreID, fileID)
	if err != nil {
		return err
	}
	// Also delete the underlying file
	_, err = c.client.Files.Delete(ctx, fileID)
	return err
}

// DeleteVectorStore removes a vector store
func (c *Client) DeleteVectorStore(ctx context.Context, vectorStoreID string) error {
	_, err := c.client.VectorStores.Delete(ctx, vectorStoreID)
	return err
}

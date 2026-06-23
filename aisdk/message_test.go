package aisdk

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestToSchemaMessagesConvertsAssistantToolUIParts(t *testing.T) {
	messages, err := ToSchemaMessages(CreateRunRequest{
		Messages: []Message{
			{
				ID:   "user_1",
				Role: "user",
				Parts: []Part{
					{Type: "text", Text: "查询郑州天气"},
				},
				Metadata: map[string]any{"custom": map[string]any{}},
			},
			{
				ID:   "assistant_1",
				Role: "assistant",
				Parts: []Part{
					{Type: "step-start"},
					{Type: "reasoning", Text: "需要查询天气", State: "done"},
					{Type: "text", Text: "我来查天气", State: "done"},
					{
						Type:       "tool-get_weather",
						ToolCallID: "call_1",
						State:      "output-available",
						Input:      map[string]any{"location": "郑州"},
						Output:     map[string]any{"weather": "sunny"},
					},
					{Type: "step-start"},
					{Type: "text", Text: "郑州晴天", State: "done"},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 {
		t.Fatalf("expected user, assistant tool call, tool result, assistant final, got %#v", messages)
	}
	if messages[0].Role != schema.User || messages[0].Content != "查询郑州天气" {
		t.Fatalf("unexpected user message: %#v", messages[0])
	}
	if len(messages[0].UserInputMultiContent) != 0 {
		t.Fatalf("text-only user message should not set multi content: %#v", messages[0].UserInputMultiContent)
	}
	if messages[1].Role != schema.Assistant || messages[1].Content != "我来查天气" || messages[1].ReasoningContent != "需要查询天气" {
		t.Fatalf("unexpected first assistant message: %#v", messages[1])
	}
	if len(messages[1].ToolCalls) != 1 || messages[1].ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("unexpected tool call: %#v", messages[1].ToolCalls)
	}
	if messages[1].ToolCalls[0].Function.Arguments != `{"location":"郑州"}` {
		t.Fatalf("unexpected tool input: %s", messages[1].ToolCalls[0].Function.Arguments)
	}
	if messages[2].Role != schema.Tool || messages[2].ToolCallID != "call_1" || messages[2].ToolName != "get_weather" {
		t.Fatalf("unexpected tool result message: %#v", messages[2])
	}
	if messages[3].Role != schema.Assistant || messages[3].Content != "郑州晴天" {
		t.Fatalf("unexpected final assistant message: %#v", messages[3])
	}
}

func TestToSchemaMessagesDoesNotSetContentWithMultiContent(t *testing.T) {
	messages, err := ToSchemaMessages(CreateRunRequest{
		Messages: []Message{{
			Role: "user",
			Parts: []Part{
				{Type: "text", Text: "描述这个文件"},
				{
					Type:      "file",
					URL:       "https://example.com/a.png",
					MediaType: "image/png",
					Filename:  "a.png",
				},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected one message, got %#v", messages)
	}
	if messages[0].Content != "" {
		t.Fatalf("multi-content user message should not set Content: %#v", messages[0])
	}
	if len(messages[0].UserInputMultiContent) != 2 {
		t.Fatalf("expected text and file input parts, got %#v", messages[0].UserInputMultiContent)
	}
}

func TestFromSchemaMessagesBuildsUIMessageParts(t *testing.T) {
	uiMessages := FromSchemaMessages([]*schema.Message{
		{
			Role:    schema.User,
			Content: "查询天气",
			Extra: map[string]any{
				uiIDExtraKey: "user_1",
				uiMetadataExtraKey: map[string]any{
					"custom": map[string]any{},
				},
			},
		},
		{
			Role:             schema.Assistant,
			Content:          "我来查天气",
			ReasoningContent: "需要工具",
			ToolCalls: []schema.ToolCall{{
				ID:   "call_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "get_weather",
					Arguments: `{"location":"郑州"}`,
				},
			}},
		},
		{
			Role:       schema.Tool,
			ToolCallID: "call_1",
			ToolName:   "get_weather",
			Content:    `{"weather":"sunny"}`,
		},
		{
			Role:    schema.Assistant,
			Content: "郑州晴天",
		},
	})

	if len(uiMessages) != 2 {
		t.Fatalf("expected user and assistant UI messages, got %#v", uiMessages)
	}
	if uiMessages[0].ID != "user_1" || uiMessages[0].Role != "user" || len(uiMessages[0].Parts) != 1 {
		t.Fatalf("unexpected user UI message: %#v", uiMessages[0])
	}
	assistant := uiMessages[1]
	if assistant.Role != "assistant" {
		t.Fatalf("unexpected assistant role: %#v", assistant)
	}
	if len(assistant.Parts) != 6 {
		t.Fatalf("expected two assistant steps in one UI message, got %#v", assistant.Parts)
	}
	toolPart := assistant.Parts[3]
	if toolPart.Type != "tool-get_weather" || toolPart.ToolCallID != "call_1" || toolPart.State != "output-available" {
		t.Fatalf("unexpected tool UI part: %#v", toolPart)
	}
	output, ok := toolPart.Output.(map[string]any)
	if !ok || output["weather"] != "sunny" {
		t.Fatalf("unexpected tool output: %#v", toolPart.Output)
	}
	if assistant.Parts[4].Type != "step-start" || assistant.Parts[5].Text != "郑州晴天" {
		t.Fatalf("unexpected final assistant step: %#v", assistant.Parts[4:])
	}
}

func TestFromSchemaMessagesAddsUsageToAssistantMetadata(t *testing.T) {
	uiMessages := FromSchemaMessages([]*schema.Message{
		{
			Role:    schema.Assistant,
			Content: "我来查天气",
			Extra: map[string]any{
				uiIDExtraKey: "assistant_1",
				uiMetadataExtraKey: map[string]any{
					"custom": map[string]any{
						"traceId": "trace_1",
					},
				},
			},
		},
		{
			Role:    schema.Assistant,
			Content: "郑州晴天",
			ResponseMeta: &schema.ResponseMeta{
				Usage: &schema.TokenUsage{
					PromptTokens:     10,
					CompletionTokens: 5,
					TotalTokens:      15,
					PromptTokenDetails: schema.PromptTokenDetails{
						CachedTokens: 3,
					},
					CompletionTokensDetails: schema.CompletionTokensDetails{
						ReasoningTokens: 2,
					},
				},
			},
		},
	})

	if len(uiMessages) != 1 {
		t.Fatalf("expected one merged assistant UI message, got %#v", uiMessages)
	}
	custom, ok := uiMessages[0].Metadata["custom"].(map[string]any)
	if !ok {
		t.Fatalf("expected custom metadata, got %#v", uiMessages[0].Metadata)
	}
	if custom["traceId"] != "trace_1" {
		t.Fatalf("expected existing custom metadata to be preserved, got %#v", custom)
	}
	usage, ok := custom["usage"].(map[string]any)
	if !ok {
		t.Fatalf("expected usage metadata, got %#v", custom)
	}
	if usage["inputTokens"] != 10 || usage["outputTokens"] != 5 || usage["totalTokens"] != 15 {
		t.Fatalf("unexpected usage metadata: %#v", usage)
	}
}

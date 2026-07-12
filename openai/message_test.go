package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestFromSchemaMessagesConvertsStoredHistory(t *testing.T) {
	messages := FromSchemaMessages([]*schema.Message{
		{
			Role:    schema.User,
			Content: "查询郑州天气",
		},
		{
			Role:    schema.Assistant,
			Content: "我来查天气",
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

	if len(messages) != 4 {
		t.Fatalf("expected four OpenAI messages, got %#v", messages)
	}
	if messages[0].Role != "user" || rawString(t, messages[0].Content) != "查询郑州天气" {
		t.Fatalf("unexpected user message: %#v", messages[0])
	}
	if messages[1].Role != "assistant" || len(messages[1].ToolCalls) != 1 {
		t.Fatalf("unexpected assistant tool message: %#v", messages[1])
	}
	toolCall := messages[1].ToolCalls[0]
	if toolCall.ID != "call_1" || toolCall.Function.Name != "get_weather" || toolCall.Function.Arguments != `{"location":"郑州"}` {
		t.Fatalf("unexpected tool call: %#v", toolCall)
	}
	if messages[2].Role != "tool" || messages[2].ToolCallID != "call_1" || rawString(t, messages[2].Content) != `{"weather":"sunny"}` {
		t.Fatalf("unexpected tool message: %#v", messages[2])
	}
	if messages[3].Role != "assistant" || rawString(t, messages[3].Content) != "郑州晴天" {
		t.Fatalf("unexpected final assistant message: %#v", messages[3])
	}
}

func TestToSchemaMessagesPreservesOpenAIMultimodalContent(t *testing.T) {
	raw := json.RawMessage(`[
		{"type":"text","text":"inspect"},
		{"type":"image_url","image_url":{"url":"https://example.com/a.png","detail":"high"}},
		{"type":"input_audio","input_audio":{"data":"YXVkaW8=","format":"wav"}},
		{"type":"video_url","video_url":{"url":"https://example.com/a.mp4","media_type":"video/mp4"}},
		{"type":"file","file":{"filename":"a.pdf","file_data":"ZmlsZQ==","media_type":"application/pdf"}}
	]`)
	messages, err := ToSchemaMessages(ChatCompletionsRequest{Messages: []ChatMessage{{Role: "user", Content: raw}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages[0].UserInputMultiContent) != 5 || messages[0].Content != "" {
		t.Fatalf("multimodal content was lost: %#v", messages[0])
	}
	if messages[0].UserInputMultiContent[1].Image == nil || *messages[0].UserInputMultiContent[1].Image.URL != "https://example.com/a.png" {
		t.Fatalf("image part was not preserved: %#v", messages[0].UserInputMultiContent[1])
	}
	if messages[0].UserInputMultiContent[4].File == nil || messages[0].UserInputMultiContent[4].File.Name != "a.pdf" {
		t.Fatalf("file part was not preserved: %#v", messages[0].UserInputMultiContent[4])
	}
}

func TestToSchemaMessagesRejectsMalformedImagePart(t *testing.T) {
	raw := json.RawMessage(`[{"type":"image_url","image_url":{}}]`)
	_, err := ToSchemaMessages(ChatCompletionsRequest{Messages: []ChatMessage{{Role: "user", Content: raw}}})
	if err == nil || !strings.Contains(err.Error(), "image_url.url") {
		t.Fatalf("expected explicit image error, got %v", err)
	}
}

func TestToSchemaMessagesPreservesAssistantAndToolFields(t *testing.T) {
	messages, err := ToSchemaMessages(ChatCompletionsRequest{Messages: []ChatMessage{
		{
			Role:             "assistant",
			Name:             "planner",
			ReasoningContent: "need weather",
			Content:          json.RawMessage(`"calling"`),
			ToolCalls: []ToolCall{{
				ID:       "call_1",
				Type:     "function",
				Function: FunctionCall{Name: "weather", Arguments: `{"city":"郑州"}`},
			}},
		},
		{Role: "tool", ToolCallID: "call_1", Content: json.RawMessage(`"sunny"`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if messages[0].Name != "planner" || messages[0].ReasoningContent != "need weather" || len(messages[0].ToolCalls) != 1 {
		t.Fatalf("assistant fields lost: %#v", messages[0])
	}
	if messages[1].Role != schema.Tool || messages[1].ToolCallID != "call_1" {
		t.Fatalf("tool fields lost: %#v", messages[1])
	}
}

func rawString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		t.Fatalf("decode content %s: %v", raw, err)
	}
	return text
}

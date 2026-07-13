package einoai

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestNewSessionRunResponsePreservesMessageSequence(t *testing.T) {
	messages := []*schema.Message{
		{
			Role:    schema.User,
			Content: "weather",
			Extra: map[string]any{
				"trace_id":        "t1",
				"_einoai_private": true,
			},
		},
		{
			Role:             schema.Assistant,
			ReasoningContent: "need a tool",
			ToolCalls: []schema.ToolCall{{
				ID:   "call_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "weather",
					Arguments: `{"city":"郑州"}`,
				},
			}},
		},
		{
			Role:       schema.Tool,
			ToolCallID: "call_1",
			ToolName:   "weather",
			Content:    `{"temp":26}`,
		},
		{
			Role:    schema.Assistant,
			Content: "26C",
			ResponseMeta: &schema.ResponseMeta{
				FinishReason: "stop",
				Usage: &schema.TokenUsage{
					PromptTokens:     8,
					CompletionTokens: 4,
					TotalTokens:      12,
				},
			},
		},
	}

	got, err := NewSessionRunResponse(&RunInfo{SessionID: "s1"}, messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 4 {
		t.Fatalf("expected four independent messages, got %#v", got.Messages)
	}
	if got.Messages[1].Parts[0].Type != "reasoning" || got.Messages[1].Parts[1].Type != "tool-call" {
		t.Fatalf("unexpected assistant parts: %#v", got.Messages[1].Parts)
	}
	if got.Messages[2].Role != "tool" || got.Messages[2].Parts[0].Type != "tool-result" {
		t.Fatalf("unexpected tool message: %#v", got.Messages[2])
	}
	if got.Messages[3].Usage == nil || got.Messages[3].Usage.TotalTokens != 12 {
		t.Fatalf("usage missing: %#v", got.Messages[3])
	}
	if got.Messages[0].Metadata["trace_id"] != "t1" {
		t.Fatalf("public metadata missing: %#v", got.Messages[0].Metadata)
	}
	if _, exists := got.Messages[0].Metadata["_einoai_private"]; exists {
		t.Fatalf("internal metadata leaked: %#v", got.Messages[0].Metadata)
	}
}

func TestNewSessionRunResponseRejectsNonJSONMetadata(t *testing.T) {
	_, err := NewSessionRunResponse(nil, []*schema.Message{{
		Role:  schema.User,
		Extra: map[string]any{"bad": make(chan int)},
	}})
	if err == nil || !strings.Contains(err.Error(), "message JSON") {
		t.Fatalf("expected metadata error, got %v", err)
	}
}

func TestNewSessionRunResponsePreservesMultimodalParts(t *testing.T) {
	imageURL := "https://example.com/a.png"
	audioData := "YXVkaW8="
	videoURL := "https://example.com/a.mp4"
	fileData := "ZmlsZQ=="
	response, err := NewSessionRunResponse(nil, []*schema.Message{
		{
			Role: schema.User,
			UserInputMultiContent: []schema.MessageInputPart{
				{Type: schema.ChatMessagePartTypeText, Text: "inspect"},
				{
					Type: schema.ChatMessagePartTypeImageURL,
					Image: &schema.MessageInputImage{
						MessagePartCommon: schema.MessagePartCommon{URL: &imageURL, MIMEType: "image/png"},
						Detail:            schema.ImageURLDetailHigh,
					},
				},
				{
					Type: schema.ChatMessagePartTypeAudioURL,
					Audio: &schema.MessageInputAudio{MessagePartCommon: schema.MessagePartCommon{
						Base64Data: &audioData,
						MIMEType:   "audio/wav",
					}},
				},
				{
					Type: schema.ChatMessagePartTypeVideoURL,
					Video: &schema.MessageInputVideo{MessagePartCommon: schema.MessagePartCommon{
						URL:      &videoURL,
						MIMEType: "video/mp4",
					}},
				},
				{
					Type: schema.ChatMessagePartTypeFileURL,
					File: &schema.MessageInputFile{
						MessagePartCommon: schema.MessagePartCommon{Base64Data: &fileData, MIMEType: "application/pdf"},
						Name:              "a.pdf",
					},
				},
			},
		},
		{
			Role: schema.Assistant,
			AssistantGenMultiContent: []schema.MessageOutputPart{
				{
					Type:      schema.ChatMessagePartTypeReasoning,
					Reasoning: &schema.MessageOutputReasoning{Text: "inspect pixels", Signature: "sig_1"},
				},
				{
					Type: schema.ChatMessagePartTypeImageURL,
					Image: &schema.MessageOutputImage{MessagePartCommon: schema.MessagePartCommon{
						URL:      &imageURL,
						MIMEType: "image/png",
					}},
				},
				{Type: schema.ChatMessagePartType("provider_blob"), Text: "opaque", Extra: map[string]any{"provider_id": "p1"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := response.Messages[0].Parts; len(got) != 5 || got[1].Type != "image" || got[4].Name != "a.pdf" {
		t.Fatalf("unexpected input parts: %#v", got)
	}
	if got := response.Messages[1].Parts; len(got) != 3 || got[0].Signature != "sig_1" || got[2].Type != "data" {
		t.Fatalf("unexpected output parts: %#v", got)
	}
	unknown, ok := response.Messages[1].Parts[2].Data.(map[string]any)
	if !ok || unknown["text"] != "opaque" {
		t.Fatalf("unknown output data was lost: %#v", response.Messages[1].Parts[2])
	}
}

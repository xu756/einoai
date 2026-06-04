package openai

import (
	"encoding/json"
	"errors"

	"github.com/cloudwego/eino/schema"
)

type contentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL any    `json:"image_url,omitempty"`
}

// ToSchemaMessages converts all OpenAI request messages into Eino messages.
func ToSchemaMessages(req ChatCompletionsRequest) ([]*schema.Message, error) {
	if len(req.Messages) == 0 {
		return nil, errors.New("messages is required")
	}

	messages := make([]*schema.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		msg := &schema.Message{
			Role:       toSchemaRole(m.Role),
			Content:    contentToText(m.Content),
			ToolCallID: m.ToolCallID,
		}
		if len(m.ToolCalls) > 0 {
			msg.ToolCalls = make([]schema.ToolCall, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
					Index: tc.Index,
					ID:    tc.ID,
					Type:  tc.Type,
					Function: schema.FunctionCall{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				})
			}
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

func toSchemaRole(role string) schema.RoleType {
	switch role {
	case "assistant":
		return schema.Assistant
	case "system":
		return schema.System
	case "tool":
		return schema.Tool
	default:
		return schema.User
	}
}

func contentToText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		out := ""
		for _, p := range parts {
			if p.Type == "text" {
				out += p.Text
			}
		}
		return out
	}
	return string(raw)
}

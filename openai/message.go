package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

type contentPart struct {
	Type       string           `json:"type"`
	Text       string           `json:"text,omitempty"`
	ImageURL   *imageURLPart    `json:"image_url,omitempty"`
	InputAudio *inputAudioPart  `json:"input_audio,omitempty"`
	VideoURL   *resourceURLPart `json:"video_url,omitempty"`
	File       *filePart        `json:"file,omitempty"`
}

type imageURLPart struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type inputAudioPart struct {
	Data   string `json:"data"`
	Format string `json:"format"`
}

type resourceURLPart struct {
	URL       string `json:"url"`
	MediaType string `json:"media_type,omitempty"`
}

type filePart struct {
	Filename  string `json:"filename,omitempty"`
	FileData  string `json:"file_data,omitempty"`
	URL       string `json:"url,omitempty"`
	MediaType string `json:"media_type,omitempty"`
}

// ToSchemaMessages converts all OpenAI request messages into Eino messages.
func ToSchemaMessages(req ChatCompletionsRequest) ([]*schema.Message, error) {
	if len(req.Messages) == 0 {
		return nil, errors.New("messages is required")
	}

	messages := make([]*schema.Message, 0, len(req.Messages))
	for index, m := range req.Messages {
		content, parts, err := contentToSchema(m)
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", index, err)
		}
		msg := &schema.Message{
			Role:             toSchemaRole(m.Role),
			Content:          content,
			Name:             m.Name,
			ReasoningContent: m.ReasoningContent,
			ToolCallID:       m.ToolCallID,
			ToolCalls:        toSchemaToolCalls(m.ToolCalls),
		}
		if len(parts) > 0 {
			if msg.Role == schema.User || msg.Role == schema.Tool {
				msg.Content = ""
				msg.UserInputMultiContent = parts
			} else {
				text, ok := textOnlyInputParts(parts)
				if !ok {
					return nil, fmt.Errorf("message %d: multimodal input parts require user or tool role", index)
				}
				msg.Content = text
			}
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

// FromSchemaMessages converts Eino schema messages into OpenAI chat messages.
func FromSchemaMessages(messages []*schema.Message) []ChatMessage {
	out := make([]ChatMessage, 0, len(messages))
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		out = append(out, ChatMessage{
			Role:             fromSchemaRole(msg.Role),
			Content:          textContent(msg.Content),
			Name:             msg.Name,
			ReasoningContent: msg.ReasoningContent,
			ToolCallID:       msg.ToolCallID,
			ToolCalls:        fromSchemaToolCalls(msg.ToolCalls),
		})
	}
	return out
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

func fromSchemaRole(role schema.RoleType) string {
	switch role {
	case schema.Assistant:
		return "assistant"
	case schema.System:
		return "system"
	case schema.Tool:
		return "tool"
	default:
		return "user"
	}
}

func fromSchemaToolCalls(calls []schema.ToolCall) []ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]ToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, ToolCall{
			ID:    call.ID,
			Type:  call.Type,
			Index: call.Index,
			Function: FunctionCall{
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			},
		})
	}
	return out
}

func textContent(text string) json.RawMessage {
	data, _ := json.Marshal(text)
	return data
}

func contentToSchema(message ChatMessage) (string, []schema.MessageInputPart, error) {
	if len(message.Content) == 0 || string(message.Content) == "null" {
		return "", nil, nil
	}
	var text string
	if err := json.Unmarshal(message.Content, &text); err == nil {
		return text, nil, nil
	}
	var parts []contentPart
	if err := json.Unmarshal(message.Content, &parts); err != nil {
		return "", nil, fmt.Errorf("decode content parts: %w", err)
	}
	converted := make([]schema.MessageInputPart, 0, len(parts))
	for index, part := range parts {
		item, err := contentPartToSchema(part)
		if err != nil {
			return "", nil, fmt.Errorf("content part %d: %w", index, err)
		}
		converted = append(converted, item)
	}
	return "", converted, nil
}

func contentPartToSchema(part contentPart) (schema.MessageInputPart, error) {
	switch part.Type {
	case "text":
		return schema.MessageInputPart{Type: schema.ChatMessagePartTypeText, Text: part.Text}, nil
	case "image_url":
		if part.ImageURL == nil || part.ImageURL.URL == "" {
			return schema.MessageInputPart{}, errors.New("image_url.url is required")
		}
		url := part.ImageURL.URL
		return schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageInputImage{
				MessagePartCommon: schema.MessagePartCommon{URL: &url},
				Detail:            schema.ImageURLDetail(part.ImageURL.Detail),
			},
		}, nil
	case "input_audio":
		if part.InputAudio == nil || part.InputAudio.Data == "" || part.InputAudio.Format == "" {
			return schema.MessageInputPart{}, errors.New("input_audio.data and input_audio.format are required")
		}
		data := part.InputAudio.Data
		return schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeAudioURL,
			Audio: &schema.MessageInputAudio{MessagePartCommon: schema.MessagePartCommon{
				Base64Data: &data,
				MIMEType:   audioMIMEType(part.InputAudio.Format),
			}},
		}, nil
	case "video_url":
		if part.VideoURL == nil || part.VideoURL.URL == "" {
			return schema.MessageInputPart{}, errors.New("video_url.url is required")
		}
		url := part.VideoURL.URL
		return schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeVideoURL,
			Video: &schema.MessageInputVideo{MessagePartCommon: schema.MessagePartCommon{
				URL:      &url,
				MIMEType: part.VideoURL.MediaType,
			}},
		}, nil
	case "file":
		if part.File == nil || (part.File.URL == "" && part.File.FileData == "") {
			return schema.MessageInputPart{}, errors.New("file.url or file.file_data is required")
		}
		common := schema.MessagePartCommon{MIMEType: part.File.MediaType}
		if part.File.URL != "" {
			url := part.File.URL
			common.URL = &url
		}
		if part.File.FileData != "" {
			data := part.File.FileData
			common.Base64Data = &data
		}
		return schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeFileURL,
			File: &schema.MessageInputFile{MessagePartCommon: common, Name: part.File.Filename},
		}, nil
	default:
		return schema.MessageInputPart{}, fmt.Errorf("unsupported content part type %q", part.Type)
	}
}

func textOnlyInputParts(parts []schema.MessageInputPart) (string, bool) {
	var text strings.Builder
	for _, part := range parts {
		if part.Type != schema.ChatMessagePartTypeText {
			return "", false
		}
		text.WriteString(part.Text)
	}
	return text.String(), true
}

func toSchemaToolCalls(calls []ToolCall) []schema.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]schema.ToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, schema.ToolCall{
			Index: call.Index,
			ID:    call.ID,
			Type:  call.Type,
			Function: schema.FunctionCall{
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			},
		})
	}
	return out
}

func audioMIMEType(format string) string {
	switch strings.ToLower(format) {
	case "mp3":
		return "audio/mpeg"
	default:
		return "audio/" + strings.ToLower(format)
	}
}

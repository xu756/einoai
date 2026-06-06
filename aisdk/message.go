package aisdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

const (
	uiIDExtraKey       = "_einoai_ui_id"
	uiMetadataExtraKey = "_einoai_ui_metadata"
)

// ToSchemaMessages converts AI SDK messages into Eino schema messages.
func ToSchemaMessages(req CreateRunRequest) ([]*schema.Message, error) {
	if len(req.Messages) == 0 {
		return nil, errors.New("messages is required")
	}

	msgs := make([]*schema.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		converted, err := toSchemaMessages(m)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, converted...)
	}

	return msgs, nil
}

func toSchemaMessages(m Message) ([]*schema.Message, error) {
	switch m.Role {
	case "assistant":
		return assistantToSchemaMessages(m)
	case "system":
		msg := &schema.Message{Role: schema.System, Content: textFromParts(m.Parts)}
		attachUIExtra(msg, m)
		return []*schema.Message{msg}, nil
	default:
		multiParts, _ := toInputParts(m.Parts)
		msg := &schema.Message{
			Role:                  schema.User,
			Content:               textFromParts(m.Parts),
			UserInputMultiContent: multiParts,
		}
		attachUIExtra(msg, m)
		return []*schema.Message{msg}, nil
	}
}

func assistantToSchemaMessages(m Message) ([]*schema.Message, error) {
	var out []*schema.Message
	current := &schema.Message{Role: schema.Assistant}
	attachUIExtra(current, m)

	flushCurrent := func() {
		if !hasAssistantContent(current) {
			return
		}
		out = append(out, current)
		current = &schema.Message{Role: schema.Assistant}
		attachUIExtra(current, m)
	}

	for _, part := range m.Parts {
		switch {
		case part.Type == "step-start":
			flushCurrent()
		case part.Type == "text":
			current.Content += part.Text
		case part.Type == "reasoning":
			current.ReasoningContent += part.Text
		case strings.HasPrefix(part.Type, "tool-"):
			name := strings.TrimPrefix(part.Type, "tool-")
			arguments, err := marshalPartValue(part.Input)
			if err != nil {
				return nil, fmt.Errorf("marshal tool input %s: %w", name, err)
			}
			current.ToolCalls = append(current.ToolCalls, schema.ToolCall{
				ID:   part.ToolCallID,
				Type: "function",
				Function: schema.FunctionCall{
					Name:      name,
					Arguments: arguments,
				},
			})
			if part.State == "output-available" || part.State == "output-error" {
				flushCurrent()
				content, err := marshalToolOutput(part)
				if err != nil {
					return nil, fmt.Errorf("marshal tool output %s: %w", name, err)
				}
				out = append(out, &schema.Message{
					Role:       schema.Tool,
					ToolCallID: part.ToolCallID,
					ToolName:   name,
					Content:    content,
				})
			}
		}
	}
	flushCurrent()
	return out, nil
}

// FromSchemaMessages converts Eino schema messages into AI SDK messages.
func FromSchemaMessages(messages []*schema.Message) []Message {
	out := make([]Message, 0, len(messages))
	for i := 0; i < len(messages); i++ {
		msg := messages[i]
		if msg == nil {
			continue
		}
		if msg.Role == schema.Tool {
			if len(out) == 0 || out[len(out)-1].Role != "assistant" {
				continue
			}
			mergeToolOutput(&out[len(out)-1], msg)
			continue
		}
		if msg.Role == schema.Assistant && shouldAppendAssistantMessage(out, msg) {
			appendAssistantParts(&out[len(out)-1], msg)
			continue
		}
		out = append(out, fromSchemaMessage(msg, len(out)))
	}
	return out
}

func shouldAppendAssistantMessage(messages []Message, msg *schema.Message) bool {
	if len(messages) == 0 || messages[len(messages)-1].Role != "assistant" {
		return false
	}
	id := uiIDFromExtra(msg)
	return id == "" || messages[len(messages)-1].ID == "" || messages[len(messages)-1].ID == id
}

func fromSchemaMessage(msg *schema.Message, index int) Message {
	m := Message{
		ID:       uiID(msg, index),
		Role:     fromSchemaRole(msg.Role),
		Metadata: uiMetadata(msg),
	}
	switch msg.Role {
	case schema.Assistant:
		appendAssistantParts(&m, msg)
	case schema.User:
		if len(msg.UserInputMultiContent) > 0 {
			m.Parts = fromInputParts(msg.UserInputMultiContent)
		} else if msg.Content != "" {
			m.Parts = []Part{{Type: "text", Text: msg.Content}}
		}
	default:
		if msg.Content != "" {
			m.Parts = []Part{{Type: "text", Text: msg.Content}}
		}
	}
	if m.Parts == nil {
		m.Parts = []Part{}
	}
	return m
}

func appendAssistantParts(m *Message, msg *schema.Message) {
	m.Parts = append(m.Parts, Part{Type: "step-start"})
	if msg.ReasoningContent != "" {
		m.Parts = append(m.Parts, Part{Type: "reasoning", Text: msg.ReasoningContent, State: "done"})
	}
	if len(msg.AssistantGenMultiContent) > 0 {
		m.Parts = append(m.Parts, fromOutputParts(msg.AssistantGenMultiContent)...)
	} else if msg.Content != "" {
		m.Parts = append(m.Parts, Part{Type: "text", Text: msg.Content, State: "done"})
	}
	for _, call := range msg.ToolCalls {
		m.Parts = append(m.Parts, Part{
			Type:       "tool-" + call.Function.Name,
			ToolCallID: call.ID,
			State:      "input-available",
			Input:      parseMaybeJSON(call.Function.Arguments),
		})
	}
}

func fromSchemaRole(role schema.RoleType) string {
	switch role {
	case schema.Assistant:
		return "assistant"
	case schema.System:
		return "system"
	default:
		return "user"
	}
}

func attachUIExtra(msg *schema.Message, m Message) {
	if m.ID == "" && len(m.Metadata) == 0 {
		return
	}
	if msg.Extra == nil {
		msg.Extra = make(map[string]any)
	}
	if m.ID != "" {
		msg.Extra[uiIDExtraKey] = m.ID
	}
	if len(m.Metadata) > 0 {
		msg.Extra[uiMetadataExtraKey] = m.Metadata
	}
}

func uiID(msg *schema.Message, index int) string {
	if id := uiIDFromExtra(msg); id != "" {
		return id
	}
	return fmt.Sprintf("msg_%d", index)
}

func uiIDFromExtra(msg *schema.Message) string {
	if msg.Extra != nil {
		if id, ok := msg.Extra[uiIDExtraKey].(string); ok && id != "" {
			return id
		}
	}
	return ""
}

func uiMetadata(msg *schema.Message) map[string]any {
	if msg.Extra == nil {
		return nil
	}
	metadata, ok := msg.Extra[uiMetadataExtraKey].(map[string]any)
	if !ok {
		return nil
	}
	return metadata
}

func hasAssistantContent(msg *schema.Message) bool {
	return msg.Content != "" || msg.ReasoningContent != "" || len(msg.ToolCalls) > 0
}

func textFromParts(parts []Part) string {
	var sb strings.Builder
	for _, part := range parts {
		if part.Type == "text" {
			sb.WriteString(part.Text)
		}
	}
	return sb.String()
}

func marshalPartValue(v any) (string, error) {
	if v == nil {
		return "{}", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func marshalToolOutput(part Part) (string, error) {
	if part.State == "output-error" {
		return marshalPartValue(map[string]any{"error": part.ErrorText})
	}
	return marshalPartValue(part.Output)
}

func mergeToolOutput(message *Message, msg *schema.Message) {
	for i := range message.Parts {
		part := &message.Parts[i]
		if !strings.HasPrefix(part.Type, "tool-") || part.ToolCallID != msg.ToolCallID {
			continue
		}
		part.State = "output-available"
		part.Output = parseMaybeJSON(msg.Content)
		return
	}
	message.Parts = append(message.Parts, Part{
		Type:       "tool-" + msg.ToolName,
		ToolCallID: msg.ToolCallID,
		State:      "output-available",
		Input:      map[string]any{},
		Output:     parseMaybeJSON(msg.Content),
	})
}

func toInputParts(parts []Part) ([]schema.MessageInputPart, bool) {
	var out []schema.MessageInputPart
	hasTextPart := false
	for _, part := range parts {
		switch part.Type {
		case "text":
			hasTextPart = true
			out = append(out, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeText,
				Text: part.Text,
			})
		case "file":
			out = append(out, toFileInputPart(part))
		}
	}
	return out, hasTextPart
}

func fromInputParts(parts []schema.MessageInputPart) []Part {
	out := make([]Part, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case schema.ChatMessagePartTypeText:
			out = append(out, Part{Type: "text", Text: part.Text})
		default:
			uiPart := fromMessagePartCommon(inputPartCommon(part))
			if part.File != nil {
				uiPart.Filename = part.File.Name
			}
			out = append(out, uiPart)
		}
	}
	return out
}

func fromOutputParts(parts []schema.MessageOutputPart) []Part {
	out := make([]Part, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case schema.ChatMessagePartTypeText:
			out = append(out, Part{Type: "text", Text: part.Text, State: "done"})
		case schema.ChatMessagePartTypeReasoning:
			if part.Reasoning != nil {
				out = append(out, Part{Type: "reasoning", Text: part.Reasoning.Text, State: "done"})
			}
		default:
			out = append(out, fromMessagePartCommon(outputPartCommon(part)))
		}
	}
	return out
}

func inputPartCommon(part schema.MessageInputPart) schema.MessagePartCommon {
	switch {
	case part.Image != nil:
		return part.Image.MessagePartCommon
	case part.Audio != nil:
		return part.Audio.MessagePartCommon
	case part.Video != nil:
		return part.Video.MessagePartCommon
	case part.File != nil:
		return part.File.MessagePartCommon
	default:
		return schema.MessagePartCommon{}
	}
}

func outputPartCommon(part schema.MessageOutputPart) schema.MessagePartCommon {
	switch {
	case part.Image != nil:
		return part.Image.MessagePartCommon
	case part.Audio != nil:
		return part.Audio.MessagePartCommon
	case part.Video != nil:
		return part.Video.MessagePartCommon
	default:
		return schema.MessagePartCommon{}
	}
}

func fromMessagePartCommon(common schema.MessagePartCommon) Part {
	part := Part{Type: "file", MediaType: common.MIMEType}
	if common.URL != nil {
		part.URL = *common.URL
	}
	if common.Base64Data != nil {
		part.URL = "data:" + common.MIMEType + ";base64," + *common.Base64Data
	}
	return part
}

func toFileInputPart(part Part) schema.MessageInputPart {
	partType := schema.ChatMessagePartType("file_url")
	if strings.HasPrefix(part.MediaType, "image/") {
		partType = schema.ChatMessagePartType("image_url")
	} else if strings.HasPrefix(part.MediaType, "audio/") {
		partType = schema.ChatMessagePartType("audio_url")
	} else if strings.HasPrefix(part.MediaType, "video/") {
		partType = schema.ChatMessagePartType("video_url")
	}

	urlStr := part.URL
	var base64Data *string
	mimeType := part.MediaType
	if strings.HasPrefix(urlStr, "data:") {
		commaIdx := strings.Index(urlStr, ",")
		if commaIdx != -1 {
			b64 := urlStr[commaIdx+1:]
			base64Data = &b64
			meta := urlStr[5:commaIdx]
			semiIdx := strings.Index(meta, ";")
			if semiIdx != -1 {
				mimeType = meta[:semiIdx]
			} else {
				mimeType = meta
			}
			urlStr = ""
		}
	}

	common := schema.MessagePartCommon{MIMEType: mimeType}
	if urlStr != "" {
		common.URL = &urlStr
	}
	if base64Data != nil {
		common.Base64Data = base64Data
	}

	inputPart := schema.MessageInputPart{Type: partType}
	switch partType {
	case schema.ChatMessagePartType("image_url"):
		inputPart.Image = &schema.MessageInputImage{MessagePartCommon: common}
	case schema.ChatMessagePartType("audio_url"):
		inputPart.Audio = &schema.MessageInputAudio{MessagePartCommon: common}
	case schema.ChatMessagePartType("video_url"):
		inputPart.Video = &schema.MessageInputVideo{MessagePartCommon: common}
	default:
		inputPart.File = &schema.MessageInputFile{
			MessagePartCommon: common,
			Name:              part.Filename,
		}
	}
	return inputPart
}

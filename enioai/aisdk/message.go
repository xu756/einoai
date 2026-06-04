package aisdk

import (
	"errors"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// ToSchemaMessages converts AI SDK messages into Eino schema messages.
func ToSchemaMessages(req CreateRunRequest) ([]*schema.Message, error) {
	if len(req.Messages) == 0 {
		return nil, errors.New("messages is required")
	}

	msgs := make([]*schema.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		role := schema.User
		switch m.Role {
		case "assistant":
			role = schema.Assistant
		case "system":
			role = schema.System
		case "tool":
			role = schema.Tool
		}

		msg := &schema.Message{Role: role}
		multiParts, hasTextPart := toInputParts(m.Parts)
		if len(multiParts) > 0 && (role == schema.User || role == schema.Tool) {
			if m.Content != "" && !hasTextPart {
				multiParts = append([]schema.MessageInputPart{{
					Type: schema.ChatMessagePartTypeText,
					Text: m.Content,
				}}, multiParts...)
			}
			msg.UserInputMultiContent = multiParts
			msg.Content = m.Content
		} else if len(multiParts) > 0 && role == schema.Assistant {
			msg.AssistantGenMultiContent = toOutputParts(multiParts, m.Content, hasTextPart)
			msg.Content = m.Content
		} else if m.Content == "" && hasTextPart {
			var sb strings.Builder
			for _, part := range m.Parts {
				if part.Type == "text" {
					sb.WriteString(part.Text)
				}
			}
			msg.Content = sb.String()
		} else {
			msg.Content = m.Content
		}
		msgs = append(msgs, msg)
	}

	return msgs, nil
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
		case "file", "image":
			out = append(out, toFileInputPart(part))
		}
	}
	return out, hasTextPart
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

func toOutputParts(parts []schema.MessageInputPart, content string, hasTextPart bool) []schema.MessageOutputPart {
	var out []schema.MessageOutputPart
	if content != "" && !hasTextPart {
		out = append(out, schema.MessageOutputPart{
			Type: schema.ChatMessagePartTypeText,
			Text: content,
		})
	}
	for _, part := range parts {
		outputPart := schema.MessageOutputPart{
			Type: part.Type,
			Text: part.Text,
		}
		if part.Image != nil {
			outputPart.Image = &schema.MessageOutputImage{MessagePartCommon: part.Image.MessagePartCommon}
		} else if part.Audio != nil {
			outputPart.Audio = &schema.MessageOutputAudio{MessagePartCommon: part.Audio.MessagePartCommon}
		} else if part.Video != nil {
			outputPart.Video = &schema.MessageOutputVideo{MessagePartCommon: part.Video.MessagePartCommon}
		}
		out = append(out, outputPart)
	}
	return out
}

package einoai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

const sessionMessageIDExtraKey = "_einoai_message_id"

// SessionRunResponse is the protocol-neutral session history response.
type SessionRunResponse struct {
	Run      *RunInfo         `json:"run"`
	Messages []SessionMessage `json:"messages"`
}

// SessionMessage is one stored message in execution order.
type SessionMessage struct {
	ID           string         `json:"id"`
	Role         string         `json:"role"`
	Name         string         `json:"name,omitempty"`
	Parts        []SessionPart  `json:"parts"`
	FinishReason string         `json:"finish_reason,omitempty"`
	Usage        *SessionUsage  `json:"usage,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// SessionPart is a tagged content, media, reasoning, or tool part.
type SessionPart struct {
	Type       string         `json:"type"`
	Text       string         `json:"text,omitempty"`
	Signature  string         `json:"signature,omitempty"`
	URL        string         `json:"url,omitempty"`
	Base64Data string         `json:"base64_data,omitempty"`
	MediaType  string         `json:"media_type,omitempty"`
	Name       string         `json:"name,omitempty"`
	Detail     string         `json:"detail,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolName   string         `json:"tool_name,omitempty"`
	Input      any            `json:"input,omitempty"`
	Output     any            `json:"output,omitempty"`
	DataType   string         `json:"data_type,omitempty"`
	Data       any            `json:"data,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// SessionUsage is a protocol-neutral token usage breakdown.
type SessionUsage struct {
	InputTokens        int                       `json:"input_tokens"`
	OutputTokens       int                       `json:"output_tokens"`
	TotalTokens        int                       `json:"total_tokens"`
	InputTokenDetails  SessionInputTokenDetails  `json:"input_token_details"`
	OutputTokenDetails SessionOutputTokenDetails `json:"output_token_details"`
}

// SessionInputTokenDetails describes cached and uncached input tokens.
type SessionInputTokenDetails struct {
	CachedTokens   int `json:"cached_tokens"`
	UncachedTokens int `json:"uncached_tokens"`
}

// SessionOutputTokenDetails describes reasoning and text output tokens.
type SessionOutputTokenDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
	TextTokens      int `json:"text_tokens"`
}

// NewSessionRunResponse converts stored Eino messages into the unified session format.
func NewSessionRunResponse(run *RunInfo, messages []*schema.Message) (SessionRunResponse, error) {
	converted := make([]SessionMessage, 0, len(messages))
	for index, message := range messages {
		if message == nil {
			continue
		}
		item, err := newSessionMessage(message, index)
		if err != nil {
			return SessionRunResponse{}, fmt.Errorf("convert message %d: %w", index, err)
		}
		converted = append(converted, item)
	}
	return SessionRunResponse{Run: run, Messages: converted}, nil
}

func newSessionMessage(message *schema.Message, index int) (SessionMessage, error) {
	out := SessionMessage{
		ID:       sessionMessageID(message, index),
		Role:     string(message.Role),
		Name:     message.Name,
		Parts:    []SessionPart{},
		Metadata: publicMetadata(message.Extra),
	}
	if message.ReasoningContent != "" {
		out.Parts = append(out.Parts, SessionPart{Type: "reasoning", Text: message.ReasoningContent})
	}
	if message.Role == schema.Tool {
		out.Parts = append(out.Parts, SessionPart{
			Type:       "tool-result",
			ToolCallID: message.ToolCallID,
			ToolName:   message.ToolName,
			Output:     parseSessionValue(message.Content),
		})
	} else {
		switch {
		case len(message.UserInputMultiContent) > 0:
			for _, part := range message.UserInputMultiContent {
				out.Parts = append(out.Parts, inputSessionPart(part))
			}
		case len(message.AssistantGenMultiContent) > 0:
			for _, part := range message.AssistantGenMultiContent {
				out.Parts = append(out.Parts, outputSessionPart(part))
			}
		case message.Content != "":
			out.Parts = append(out.Parts, SessionPart{Type: "text", Text: message.Content})
		}
	}
	for _, call := range message.ToolCalls {
		out.Parts = append(out.Parts, SessionPart{
			Type:       "tool-call",
			ToolCallID: call.ID,
			ToolName:   call.Function.Name,
			Input:      parseSessionValue(call.Function.Arguments),
			Metadata:   publicMetadata(call.Extra),
		})
	}
	if message.ResponseMeta != nil {
		out.FinishReason = message.ResponseMeta.FinishReason
		out.Usage = newSessionUsage(message.ResponseMeta.Usage)
	}
	if _, err := json.Marshal(out); err != nil {
		return SessionMessage{}, fmt.Errorf("message JSON: %w", err)
	}
	return out, nil
}

func sessionMessageID(message *schema.Message, index int) string {
	if message.Extra != nil {
		if id, _ := message.Extra[sessionMessageIDExtraKey].(string); id != "" {
			return id
		}
		if id, _ := message.Extra["_einoai_ui_id"].(string); id != "" {
			return id
		}
	}
	return fmt.Sprintf("msg_%d", index)
}

func assignSessionMessageID(message *schema.Message, runID, namespace string, index int) {
	if message == nil {
		return
	}
	if message.Extra == nil {
		message.Extra = make(map[string]any)
	}
	if id, _ := message.Extra[sessionMessageIDExtraKey].(string); id != "" {
		return
	}
	if uiID, _ := message.Extra["_einoai_ui_id"].(string); uiID != "" {
		message.Extra[sessionMessageIDExtraKey] = uiID
		return
	}
	message.Extra[sessionMessageIDExtraKey] = fmt.Sprintf("msg_%s_%s_%d", runID, namespace, index)
}

func assignSessionMessageIDs(messages []*schema.Message, runID, namespace string) {
	for index, message := range messages {
		assignSessionMessageID(message, runID, namespace, index)
	}
}

func publicMetadata(extra map[string]any) map[string]any {
	var out map[string]any
	for key, value := range extra {
		if strings.HasPrefix(key, "_einoai_") {
			continue
		}
		if out == nil {
			out = make(map[string]any)
		}
		out[key] = value
	}
	return out
}

func parseSessionValue(value string) any {
	if value == "" {
		return ""
	}
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err == nil {
		return decoded
	}
	return value
}

func newSessionUsage(usage *schema.TokenUsage) *SessionUsage {
	normalized := NormalizeTokenUsage(usage)
	if normalized == nil {
		return nil
	}
	return &SessionUsage{
		InputTokens:  normalized.InputTokens,
		OutputTokens: normalized.OutputTokens,
		TotalTokens:  normalized.TotalTokens,
		InputTokenDetails: SessionInputTokenDetails{
			CachedTokens:   normalized.CachedInputTokens,
			UncachedTokens: normalized.UncachedInputTokens,
		},
		OutputTokenDetails: SessionOutputTokenDetails{
			ReasoningTokens: normalized.ReasoningTokens,
			TextTokens:      normalized.TextOutputTokens,
		},
	}
}

func inputSessionPart(part schema.MessageInputPart) SessionPart {
	switch part.Type {
	case schema.ChatMessagePartTypeText:
		return SessionPart{Type: "text", Text: part.Text, Metadata: publicMetadata(part.Extra)}
	case schema.ChatMessagePartTypeImageURL:
		return mediaSessionPart("image", inputPartCommon(part), inputImageDetail(part), "", part.Extra)
	case schema.ChatMessagePartTypeAudioURL:
		return mediaSessionPart("audio", inputPartCommon(part), "", "", part.Extra)
	case schema.ChatMessagePartTypeVideoURL:
		return mediaSessionPart("video", inputPartCommon(part), "", "", part.Extra)
	case schema.ChatMessagePartTypeFileURL:
		name := ""
		if part.File != nil {
			name = part.File.Name
		}
		return mediaSessionPart("file", inputPartCommon(part), "", name, part.Extra)
	default:
		return SessionPart{Type: "data", DataType: string(part.Type), Data: unknownInputPartData(part)}
	}
}

func outputSessionPart(part schema.MessageOutputPart) SessionPart {
	switch part.Type {
	case schema.ChatMessagePartTypeText:
		return SessionPart{Type: "text", Text: part.Text, Metadata: publicMetadata(part.Extra)}
	case schema.ChatMessagePartTypeReasoning:
		if part.Reasoning == nil {
			return SessionPart{Type: "reasoning", Metadata: publicMetadata(part.Extra)}
		}
		return SessionPart{
			Type:      "reasoning",
			Text:      part.Reasoning.Text,
			Signature: part.Reasoning.Signature,
			Metadata:  publicMetadata(part.Extra),
		}
	case schema.ChatMessagePartTypeImageURL:
		return mediaSessionPart("image", outputPartCommon(part), "", "", part.Extra)
	case schema.ChatMessagePartTypeAudioURL:
		return mediaSessionPart("audio", outputPartCommon(part), "", "", part.Extra)
	case schema.ChatMessagePartTypeVideoURL:
		return mediaSessionPart("video", outputPartCommon(part), "", "", part.Extra)
	default:
		return SessionPart{Type: "data", DataType: string(part.Type), Data: unknownOutputPartData(part)}
	}
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

func inputImageDetail(part schema.MessageInputPart) string {
	if part.Image == nil {
		return ""
	}
	return string(part.Image.Detail)
}

func mediaSessionPart(partType string, common schema.MessagePartCommon, detail, name string, extra map[string]any) SessionPart {
	out := SessionPart{
		Type:      partType,
		MediaType: common.MIMEType,
		Detail:    detail,
		Name:      name,
		Metadata:  publicMetadata(extra),
	}
	if common.URL != nil {
		out.URL = *common.URL
	}
	if common.Base64Data != nil {
		out.Base64Data = *common.Base64Data
	}
	return out
}

func unknownInputPartData(part schema.MessageInputPart) map[string]any {
	data := make(map[string]any)
	if part.Text != "" {
		data["text"] = part.Text
	}
	if metadata := publicMetadata(part.Extra); len(metadata) > 0 {
		data["metadata"] = metadata
	}
	if part.Image != nil {
		data["image"] = mediaPartData(part.Image.MessagePartCommon, string(part.Image.Detail), "")
	}
	if part.Audio != nil {
		data["audio"] = mediaPartData(part.Audio.MessagePartCommon, "", "")
	}
	if part.Video != nil {
		data["video"] = mediaPartData(part.Video.MessagePartCommon, "", "")
	}
	if part.File != nil {
		data["file"] = mediaPartData(part.File.MessagePartCommon, "", part.File.Name)
	}
	if part.ToolSearchResult != nil {
		data["tool_search_result"] = part.ToolSearchResult
	}
	return data
}

func unknownOutputPartData(part schema.MessageOutputPart) map[string]any {
	data := make(map[string]any)
	if part.Text != "" {
		data["text"] = part.Text
	}
	if metadata := publicMetadata(part.Extra); len(metadata) > 0 {
		data["metadata"] = metadata
	}
	if part.Image != nil {
		data["image"] = mediaPartData(part.Image.MessagePartCommon, "", "")
	}
	if part.Audio != nil {
		data["audio"] = mediaPartData(part.Audio.MessagePartCommon, "", "")
	}
	if part.Video != nil {
		data["video"] = mediaPartData(part.Video.MessagePartCommon, "", "")
	}
	if part.Reasoning != nil {
		data["reasoning"] = map[string]any{
			"text":      part.Reasoning.Text,
			"signature": part.Reasoning.Signature,
		}
	}
	return data
}

func mediaPartData(common schema.MessagePartCommon, detail, name string) map[string]any {
	data := make(map[string]any)
	if common.URL != nil {
		data["url"] = *common.URL
	}
	if common.Base64Data != nil {
		data["base64_data"] = *common.Base64Data
	}
	if common.MIMEType != "" {
		data["media_type"] = common.MIMEType
	}
	if detail != "" {
		data["detail"] = detail
	}
	if name != "" {
		data["name"] = name
	}
	if metadata := publicMetadata(common.Extra); len(metadata) > 0 {
		data["metadata"] = metadata
	}
	return data
}

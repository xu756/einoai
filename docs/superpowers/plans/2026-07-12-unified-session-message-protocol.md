# Unified Session Message Protocol Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make both session endpoints return one lossless protocol-neutral message format while bringing AI SDK and OpenAI streaming, non-streaming, usage, reasoning, tool, and multimodal behavior into alignment with their documented protocols.

**Architecture:** Add session message and normalized usage types to the core `einoai` package, convert each stored Eino message exactly once, and make both protocol response packages delegate to that converter. Keep AI SDK and OpenAI request/stream adapters separate, but reuse core usage normalization and cover every behavior with focused red-green tests.

**Tech Stack:** Go 1.25, CloudWeGo Eino schema v0.9.2, Gin, standard `encoding/json` and `net/http/httptest`, Go testing, Redis/miniredis integration tests.

---

## File Structure

- Create `usage.go`: protocol-neutral token normalization and non-negative derived counts.
- Create `usage_test.go`: normalization edge cases.
- Create `session_message.go`: public session types, schema-to-session conversion, metadata filtering, multimodal conversion, JSON value parsing.
- Create `session_message_test.go`: scalar, tool, multimodal, metadata, usage, and ID conversion tests.
- Modify `service.go`: attach stable IDs to request snapshot messages before persistence.
- Modify `runner.go`: attach stable IDs to generated assistant and tool messages.
- Modify `service_test.go` and `runner_test.go`: persisted ID coverage.
- Modify `aisdk/response.go` and `openai/response.go`: delegate session responses to the core converter and return conversion errors.
- Add `aisdk/response_test.go` and modify `openai/message_test.go`: cross-adapter JSON equality and response error coverage.
- Modify `cmd/server/ai_handler.go` and `cmd/server/openai_handler.go`: handle the new response constructor error and parse OpenAI subscription usage options.
- Add `cmd/server/openai_handler_test.go`: subscription query parsing.
- Modify `openai/request.go` and `openai/message.go`: complete multimodal/reasoning request conversion.
- Modify `openai/message_test.go`: multimodal and invalid-part tests.
- Modify `openai/stream.go` and `openai/stream_test.go`: pure `data:` SSE, reasoning/tool chunks, usage opt-in, and shared normalization.
- Modify `aisdk/message.go`, `aisdk/stream.go`, `aisdk/message_test.go`, and `aisdk/stream_test.go`: shared normalized usage with non-negative details.
- Modify `README.md`, `docs/api.md`, `aisdk/README.md`, and `openai/README.md`: breaking session format and corrected stream/non-stream behavior.

### Task 1: Normalize Token Usage in the Core Package

**Files:**
- Create: `usage.go`
- Create: `usage_test.go`

- [ ] **Step 1: Write failing normalization tests**

```go
package einoai

import (
    "testing"

    "github.com/cloudwego/eino/schema"
)

func TestNormalizeTokenUsage(t *testing.T) {
    got := NormalizeTokenUsage(&schema.TokenUsage{
        PromptTokens:     10,
        CompletionTokens: 7,
        TotalTokens:      17,
        PromptTokenDetails: schema.PromptTokenDetails{
            CachedTokens: 4,
        },
        CompletionTokensDetails: schema.CompletionTokensDetails{
            ReasoningTokens: 3,
        },
    })
    if got == nil || got.InputTokens != 10 || got.UncachedInputTokens != 6 {
        t.Fatalf("unexpected input usage: %#v", got)
    }
    if got.OutputTokens != 7 || got.TextOutputTokens != 4 || got.ReasoningTokens != 3 {
        t.Fatalf("unexpected output usage: %#v", got)
    }
}

func TestNormalizeTokenUsageClampsDerivedCounts(t *testing.T) {
    got := NormalizeTokenUsage(&schema.TokenUsage{
        PromptTokens:     2,
        CompletionTokens: 1,
        TotalTokens:      3,
        PromptTokenDetails: schema.PromptTokenDetails{
            CachedTokens: 5,
        },
        CompletionTokensDetails: schema.CompletionTokensDetails{
            ReasoningTokens: 4,
        },
    })
    if got.UncachedInputTokens != 0 || got.TextOutputTokens != 0 {
        t.Fatalf("derived counts must be non-negative: %#v", got)
    }
}

func TestNormalizeTokenUsageAcceptsNil(t *testing.T) {
    if got := NormalizeTokenUsage(nil); got != nil {
        t.Fatalf("expected nil, got %#v", got)
    }
}
```

- [ ] **Step 2: Run tests and verify the missing API failure**

Run: `go test ./... -run 'TestNormalizeTokenUsage'`

Expected: FAIL because `NormalizeTokenUsage` is undefined.

- [ ] **Step 3: Implement normalized usage**

```go
package einoai

import "github.com/cloudwego/eino/schema"

type NormalizedTokenUsage struct {
    InputTokens        int
    OutputTokens       int
    TotalTokens        int
    CachedInputTokens  int
    UncachedInputTokens int
    ReasoningTokens    int
    TextOutputTokens   int
}

func NormalizeTokenUsage(usage *schema.TokenUsage) *NormalizedTokenUsage {
    if usage == nil {
        return nil
    }
    cached := max(usage.PromptTokenDetails.CachedTokens, 0)
    reasoning := max(usage.CompletionTokensDetails.ReasoningTokens, 0)
    return &NormalizedTokenUsage{
        InputTokens:         usage.PromptTokens,
        OutputTokens:        usage.CompletionTokens,
        TotalTokens:         usage.TotalTokens,
        CachedInputTokens:   cached,
        UncachedInputTokens: max(usage.PromptTokens-cached, 0),
        ReasoningTokens:     reasoning,
        TextOutputTokens:    max(usage.CompletionTokens-reasoning, 0),
    }
}
```

- [ ] **Step 4: Run the focused tests**

Run: `go test ./... -run 'TestNormalizeTokenUsage'`

Expected: PASS.

- [ ] **Step 5: Commit the normalization unit**

```bash
git add usage.go usage_test.go
git commit -m "feat: normalize token usage"
```

### Task 2: Add the Unified Session Types and Scalar/Tool Conversion

**Files:**
- Create: `session_message.go`
- Create: `session_message_test.go`

- [ ] **Step 1: Write a failing scalar, reasoning, tool, usage, and metadata test**

```go
func TestNewSessionRunResponsePreservesMessageSequence(t *testing.T) {
    messages := []*schema.Message{
        {Role: schema.User, Content: "weather", Extra: map[string]any{"trace_id": "t1", "_einoai_private": true}},
        {
            Role: schema.Assistant,
            ReasoningContent: "need a tool",
            ToolCalls: []schema.ToolCall{{
                ID: "call_1", Type: "function",
                Function: schema.FunctionCall{Name: "weather", Arguments: `{"city":"郑州"}`},
            }},
        },
        {Role: schema.Tool, ToolCallID: "call_1", ToolName: "weather", Content: `{"temp":26}`},
        {
            Role: schema.Assistant, Content: "26C",
            ResponseMeta: &schema.ResponseMeta{
                FinishReason: "stop",
                Usage: &schema.TokenUsage{PromptTokens: 8, CompletionTokens: 4, TotalTokens: 12},
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
    if _, exists := got.Messages[0].Metadata["_einoai_private"]; exists {
        t.Fatalf("internal metadata leaked: %#v", got.Messages[0].Metadata)
    }
}
```

- [ ] **Step 2: Run the test and verify the missing type failure**

Run: `go test ./... -run TestNewSessionRunResponsePreservesMessageSequence`

Expected: FAIL because `NewSessionRunResponse` and session types are undefined.

- [ ] **Step 3: Define the public session types**

```go
const sessionMessageIDExtraKey = "_einoai_message_id"

type SessionRunResponse struct {
    Run      *RunInfo         `json:"run"`
    Messages []SessionMessage `json:"messages"`
}

type SessionMessage struct {
    ID           string         `json:"id"`
    Role         string         `json:"role"`
    Name         string         `json:"name,omitempty"`
    Parts        []SessionPart  `json:"parts"`
    FinishReason string         `json:"finish_reason,omitempty"`
    Usage        *SessionUsage  `json:"usage,omitempty"`
    Metadata     map[string]any `json:"metadata,omitempty"`
}

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

type SessionUsage struct {
    InputTokens        int                       `json:"input_tokens"`
    OutputTokens       int                       `json:"output_tokens"`
    TotalTokens        int                       `json:"total_tokens"`
    InputTokenDetails  SessionInputTokenDetails  `json:"input_token_details"`
    OutputTokenDetails SessionOutputTokenDetails `json:"output_token_details"`
}

type SessionInputTokenDetails struct {
    CachedTokens   int `json:"cached_tokens"`
    UncachedTokens int `json:"uncached_tokens"`
}

type SessionOutputTokenDetails struct {
    ReasoningTokens int `json:"reasoning_tokens"`
    TextTokens      int `json:"text_tokens"`
}
```

- [ ] **Step 4: Implement the scalar and tool converter**

```go
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
            Type: "tool-result", ToolCallID: message.ToolCallID,
            ToolName: message.ToolName, Output: parseSessionValue(message.Content),
        })
    } else if message.Content != "" && len(message.UserInputMultiContent) == 0 && len(message.AssistantGenMultiContent) == 0 {
        out.Parts = append(out.Parts, SessionPart{Type: "text", Text: message.Content})
    }
    for _, call := range message.ToolCalls {
        out.Parts = append(out.Parts, SessionPart{
            Type: "tool-call", ToolCallID: call.ID, ToolName: call.Function.Name,
            Input: parseSessionValue(call.Function.Arguments), Metadata: publicMetadata(call.Extra),
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
        InputTokens: normalized.InputTokens,
        OutputTokens: normalized.OutputTokens,
        TotalTokens: normalized.TotalTokens,
        InputTokenDetails: SessionInputTokenDetails{
            CachedTokens: normalized.CachedInputTokens,
            UncachedTokens: normalized.UncachedInputTokens,
        },
        OutputTokenDetails: SessionOutputTokenDetails{
            ReasoningTokens: normalized.ReasoningTokens,
            TextTokens: normalized.TextOutputTokens,
        },
    }
}
```

- [ ] **Step 5: Run the focused test**

Run: `go test ./... -run TestNewSessionRunResponsePreservesMessageSequence`

Expected: PASS.

- [ ] **Step 6: Commit the scalar conversion**

```bash
git add session_message.go session_message_test.go
git commit -m "feat: add unified session messages"
```

### Task 3: Preserve Multimodal and Unknown Eino Parts

**Files:**
- Modify: `session_message.go`
- Modify: `session_message_test.go`

- [ ] **Step 1: Write failing multimodal input/output tests**

```go
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
                {Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{URL: &imageURL, MIMEType: "image/png"}, Detail: schema.ImageURLDetailHigh}},
                {Type: schema.ChatMessagePartTypeAudioURL, Audio: &schema.MessageInputAudio{MessagePartCommon: schema.MessagePartCommon{Base64Data: &audioData, MIMEType: "audio/wav"}}},
                {Type: schema.ChatMessagePartTypeVideoURL, Video: &schema.MessageInputVideo{MessagePartCommon: schema.MessagePartCommon{URL: &videoURL, MIMEType: "video/mp4"}}},
                {Type: schema.ChatMessagePartTypeFileURL, File: &schema.MessageInputFile{MessagePartCommon: schema.MessagePartCommon{Base64Data: &fileData, MIMEType: "application/pdf"}, Name: "a.pdf"}},
            },
        },
        {
            Role: schema.Assistant,
            AssistantGenMultiContent: []schema.MessageOutputPart{
                {Type: schema.ChatMessagePartTypeReasoning, Reasoning: &schema.MessageOutputReasoning{Text: "inspect pixels", Signature: "sig_1"}},
                {Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageOutputImage{MessagePartCommon: schema.MessagePartCommon{URL: &imageURL, MIMEType: "image/png"}}},
                {Type: schema.ChatMessagePartType("provider_blob"), Extra: map[string]any{"provider_id": "p1"}},
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
}
```

- [ ] **Step 2: Run the test and verify missing multimodal conversion**

Run: `go test ./... -run TestNewSessionRunResponsePreservesMultimodalParts`

Expected: FAIL because the converter currently returns no multimodal parts.

- [ ] **Step 3: Add input and output part converters**

```go
func inputSessionPart(part schema.MessageInputPart) SessionPart {
    switch part.Type {
    case schema.ChatMessagePartTypeText:
        return SessionPart{Type: "text", Text: part.Text, Metadata: publicMetadata(part.Extra)}
    case schema.ChatMessagePartTypeImageURL:
        return mediaSessionPart("image", inputCommon(part), imageDetail(part), "", part.Extra)
    case schema.ChatMessagePartTypeAudioURL:
        return mediaSessionPart("audio", inputCommon(part), "", "", part.Extra)
    case schema.ChatMessagePartTypeVideoURL:
        return mediaSessionPart("video", inputCommon(part), "", "", part.Extra)
    case schema.ChatMessagePartTypeFileURL:
        name := ""
        if part.File != nil {
            name = part.File.Name
        }
        return mediaSessionPart("file", inputCommon(part), "", name, part.Extra)
    default:
        data := map[string]any{"metadata": publicMetadata(part.Extra)}
        if part.ToolSearchResult != nil {
            data["tool_search_result"] = part.ToolSearchResult
        }
        return SessionPart{Type: "data", DataType: string(part.Type), Data: data}
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
        return SessionPart{Type: "reasoning", Text: part.Reasoning.Text, Signature: part.Reasoning.Signature, Metadata: publicMetadata(part.Extra)}
    case schema.ChatMessagePartTypeImageURL:
        return mediaSessionPart("image", outputCommon(part), "", "", part.Extra)
    case schema.ChatMessagePartTypeAudioURL:
        return mediaSessionPart("audio", outputCommon(part), "", "", part.Extra)
    case schema.ChatMessagePartTypeVideoURL:
        return mediaSessionPart("video", outputCommon(part), "", "", part.Extra)
    default:
        return SessionPart{Type: "data", DataType: string(part.Type), Data: publicMetadata(part.Extra)}
    }
}

func inputCommon(part schema.MessageInputPart) schema.MessagePartCommon {
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

func outputCommon(part schema.MessageOutputPart) schema.MessagePartCommon {
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

func imageDetail(part schema.MessageInputPart) string {
    if part.Image == nil {
        return ""
    }
    return string(part.Image.Detail)
}

func mediaSessionPart(partType string, common schema.MessagePartCommon, detail, name string, extra map[string]any) SessionPart {
    out := SessionPart{
        Type: partType, MediaType: common.MIMEType, Detail: detail,
        Name: name, Metadata: publicMetadata(extra),
    }
    if common.URL != nil {
        out.URL = *common.URL
    }
    if common.Base64Data != nil {
        out.Base64Data = *common.Base64Data
    }
    return out
}
```

- [ ] **Step 4: Wire the part converters into `newSessionMessage`**

```go
switch {
case len(message.UserInputMultiContent) > 0:
    for _, part := range message.UserInputMultiContent {
        out.Parts = append(out.Parts, inputSessionPart(part))
    }
case len(message.AssistantGenMultiContent) > 0:
    for _, part := range message.AssistantGenMultiContent {
        out.Parts = append(out.Parts, outputSessionPart(part))
    }
case message.Role != schema.Tool && message.Content != "":
    out.Parts = append(out.Parts, SessionPart{Type: "text", Text: message.Content})
}
```

- [ ] **Step 5: Add metadata validation tests**

```go
func TestNewSessionRunResponseRejectsNonJSONMetadata(t *testing.T) {
    _, err := NewSessionRunResponse(nil, []*schema.Message{{
        Role: schema.User,
        Extra: map[string]any{"bad": make(chan int)},
    }})
    if err == nil || !strings.Contains(err.Error(), "message JSON") {
        t.Fatalf("expected metadata error, got %v", err)
    }
}
```

- [ ] **Step 6: Run all core session tests**

Run: `go test ./... -run 'TestNewSessionRunResponse'`

Expected: PASS.

- [ ] **Step 7: Commit multimodal conversion**

```bash
git add session_message.go session_message_test.go
git commit -m "feat: preserve session multimodal parts"
```

### Task 4: Persist Stable Session Message IDs

**Files:**
- Modify: `service.go`
- Modify: `service_test.go`
- Modify: `runner.go`
- Modify: `runner_test.go`

- [ ] **Step 1: Write failing ID assignment tests**

```go
func TestAssignSessionMessageIDsPreservesExistingIDs(t *testing.T) {
    messages := []*schema.Message{
        {Role: schema.User, Extra: map[string]any{sessionMessageIDExtraKey: "client_1"}},
        {Role: schema.User},
    }
    assignSessionMessageIDs(messages, "run_1", "input")
    if messages[0].Extra[sessionMessageIDExtraKey] != "client_1" {
        t.Fatalf("existing ID changed: %#v", messages[0].Extra)
    }
    if messages[1].Extra[sessionMessageIDExtraKey] != "msg_run_1_input_1" {
        t.Fatalf("generated ID missing: %#v", messages[1].Extra)
    }
}

func TestRunEventBuilderAssignsIDsToGeneratedMessages(t *testing.T) {
    builder := newRunEventBuilder(nil, "s1", "run_1")
    builder.outputMessages = append(builder.outputMessages, &schema.Message{Role: schema.Tool})
    assignSessionMessageID(builder.outputMessages[0], builder.runID, "output", 0)
    if builder.outputMessages[0].Extra[sessionMessageIDExtraKey] != "msg_run_1_output_0" {
        t.Fatalf("generated output ID missing: %#v", builder.outputMessages[0])
    }
}
```

- [ ] **Step 2: Run tests and verify helper failure**

Run: `go test ./... -run 'TestAssignSessionMessageIDs|TestRunEventBuilderAssignsIDs'`

Expected: FAIL because the assignment helpers do not exist.

- [ ] **Step 3: Implement deterministic persisted IDs**

```go
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
```

In `CreateRun`, generate `run` before persisting the active snapshot, call `assignSessionMessageIDs(snapshotMessages, run.RunID, "input")`, then copy the assigned snapshot into `runMessages`.

In `runEventBuilder.writeMessage`, call `assignSessionMessageID(msg, b.runID, "output", len(b.outputMessages))` before appending a tool message. In `commitAssistantMessage`, call the same helper before appending the concatenated assistant message. The input/output namespace prevents collisions even when both sequences start at zero.

- [ ] **Step 4: Run service and runner tests**

Run: `go test ./... -run 'TestAssignSessionMessageIDs|TestRunEventBuilderAssignsIDs|TestCommitRunMessages|TestRequestSnapshotMessages'`

Expected: PASS.

- [ ] **Step 5: Commit stable ID persistence**

```bash
git add service.go service_test.go runner.go runner_test.go
git commit -m "feat: persist session message ids"
```

### Task 5: Make Both Session Adapters Return Identical JSON

**Files:**
- Modify: `aisdk/response.go`
- Create: `aisdk/response_test.go`
- Modify: `openai/response.go`
- Create: `openai/response_test.go`
- Create: `session_response_test.go`
- Modify: `cmd/server/ai_handler.go`
- Modify: `cmd/server/openai_handler.go`

- [ ] **Step 1: Write failing cross-adapter equality tests**

```go
func TestRunResponseUsesUnifiedSessionFormat(t *testing.T) {
    run := &einoai.RunInfo{SessionID: "s1", RunID: "r1", Status: einoai.RunStatusCompleted}
    history := []*schema.Message{{Role: schema.Assistant, ReasoningContent: "think", Content: "answer"}}
    got, err := NewRunResponse(run, history)
    if err != nil {
        t.Fatal(err)
    }
    body, err := json.Marshal(got)
    if err != nil {
        t.Fatal(err)
    }
    if !bytes.Contains(body, []byte(`"parts":[{"type":"reasoning"`)) {
        t.Fatalf("unified parts missing: %s", body)
    }
}
```

Create `session_response_test.go` with package `einoai_test` to verify exact cross-adapter equality:

```go
func TestRunResponsesAreEqual(t *testing.T) {
    run := &einoai.RunInfo{SessionID: "s1", RunID: "r1", Status: einoai.RunStatusCompleted}
    history := []*schema.Message{{Role: schema.Assistant, ReasoningContent: "think", Content: "answer"}}
    aiResponse, err := aisdk.NewRunResponse(run, history)
    if err != nil {
        t.Fatal(err)
    }
    openAIResponse, err := openai.NewRunResponse(run, history)
    if err != nil {
        t.Fatal(err)
    }
    aiJSON, _ := json.Marshal(aiResponse)
    openAIJSON, _ := json.Marshal(openAIResponse)
    if !bytes.Equal(aiJSON, openAIJSON) {
        t.Fatalf("responses differ:\nAI SDK: %s\nOpenAI: %s", aiJSON, openAIJSON)
    }
}
```

- [ ] **Step 2: Run tests and verify signature/shape failure**

Run: `go test ./aisdk ./openai -run TestRunResponseUsesUnifiedSessionFormat`

Expected: FAIL because both packages still return protocol-specific message slices and no error.

- [ ] **Step 3: Delegate both adapters to the core response**

```go
type RunResponse = einoai.SessionRunResponse

func NewRunResponse(run *einoai.RunInfo, messages []*schema.Message) (RunResponse, error) {
    return einoai.NewSessionRunResponse(run, messages)
}
```

Place the shown alias and constructor verbatim in both `aisdk/response.go` and `openai/response.go`.

- [ ] **Step 4: Update both GET handlers to handle conversion errors**

```go
response, err := aisdk.NewRunResponse(run, messages)
if err != nil {
    writeAIError(c, err)
    return
}
c.JSON(http.StatusOK, response)
```

In `getOpenAIRun`, use the same control flow with `openai.NewRunResponse` and `writeOpenAIError`.

- [ ] **Step 5: Run adapter tests**

Run: `go test ./aisdk ./openai ./cmd/server -run 'TestRunResponseUsesUnifiedSessionFormat|TestRunResponsesAreEqual'`

Expected: PASS.

- [ ] **Step 6: Commit the unified endpoint responses**

```bash
git add aisdk/response.go aisdk/response_test.go openai/response.go openai/response_test.go session_response_test.go cmd/server/ai_handler.go cmd/server/openai_handler.go
git commit -m "feat: unify session history responses"
```

### Task 6: Complete OpenAI Request Message Conversion

**Files:**
- Modify: `openai/request.go`
- Modify: `openai/message.go`
- Modify: `openai/message_test.go`

- [ ] **Step 1: Write failing multimodal request tests**

```go
func TestToSchemaMessagesPreservesOpenAIMultimodalContent(t *testing.T) {
    raw := json.RawMessage(`[
        {"type":"text","text":"inspect"},
        {"type":"image_url","image_url":{"url":"https://example.com/a.png","detail":"high"}},
        {"type":"input_audio","input_audio":{"data":"YXVkaW8=","format":"wav"}},
        {"type":"video_url","video_url":{"url":"https://example.com/a.mp4"}},
        {"type":"file","file":{"filename":"a.pdf","file_data":"ZmlsZQ==","media_type":"application/pdf"}}
    ]`)
    messages, err := ToSchemaMessages(ChatCompletionsRequest{Messages: []ChatMessage{{Role: "user", Content: raw}}})
    if err != nil {
        t.Fatal(err)
    }
    if len(messages[0].UserInputMultiContent) != 5 || messages[0].Content != "" {
        t.Fatalf("multimodal content was lost: %#v", messages[0])
    }
}

func TestToSchemaMessagesRejectsMalformedImagePart(t *testing.T) {
    raw := json.RawMessage(`[{"type":"image_url","image_url":{}}]`)
    _, err := ToSchemaMessages(ChatCompletionsRequest{Messages: []ChatMessage{{Role: "user", Content: raw}}})
    if err == nil || !strings.Contains(err.Error(), "image_url.url") {
        t.Fatalf("expected explicit image error, got %v", err)
    }
}
```

Add this assistant/tool test:

```go
func TestToSchemaMessagesPreservesAssistantAndToolFields(t *testing.T) {
    messages, err := ToSchemaMessages(ChatCompletionsRequest{Messages: []ChatMessage{
        {
            Role: "assistant", Name: "planner", ReasoningContent: "need weather",
            Content: json.RawMessage(`"calling"`),
            ToolCalls: []ToolCall{{ID: "call_1", Type: "function", Function: FunctionCall{Name: "weather", Arguments: `{"city":"郑州"}`}}},
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
```

- [ ] **Step 2: Run tests and verify non-text parts are currently dropped**

Run: `go test ./openai -run 'TestToSchemaMessagesPreservesOpenAIMultimodalContent|TestToSchemaMessagesRejectsMalformedImagePart'`

Expected: FAIL because `contentToText` only concatenates text.

- [ ] **Step 3: Expand the request types**

```go
type ChatMessage struct {
    Role             string          `json:"role"`
    Content          json.RawMessage `json:"content"`
    Name             string          `json:"name,omitempty"`
    ReasoningContent string          `json:"reasoning_content,omitempty"`
    ToolCallID       string          `json:"tool_call_id,omitempty"`
    ToolCalls        []ToolCall      `json:"tool_calls,omitempty"`
}

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
```

- [ ] **Step 4: Replace `contentToText` with an error-returning content converter**

```go
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
        return schema.MessageInputPart{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{
            MessagePartCommon: schema.MessagePartCommon{URL: &url},
            Detail: schema.ImageURLDetail(part.ImageURL.Detail),
        }}, nil
    case "input_audio":
        if part.InputAudio == nil || part.InputAudio.Data == "" || part.InputAudio.Format == "" {
            return schema.MessageInputPart{}, errors.New("input_audio.data and input_audio.format are required")
        }
        data := part.InputAudio.Data
        return schema.MessageInputPart{Type: schema.ChatMessagePartTypeAudioURL, Audio: &schema.MessageInputAudio{MessagePartCommon: schema.MessagePartCommon{
            Base64Data: &data, MIMEType: "audio/" + part.InputAudio.Format,
        }}}, nil
    case "video_url":
        if part.VideoURL == nil || part.VideoURL.URL == "" {
            return schema.MessageInputPart{}, errors.New("video_url.url is required")
        }
        url := part.VideoURL.URL
        return schema.MessageInputPart{Type: schema.ChatMessagePartTypeVideoURL, Video: &schema.MessageInputVideo{MessagePartCommon: schema.MessagePartCommon{
            URL: &url, MIMEType: part.VideoURL.MediaType,
        }}}, nil
    case "file":
        if part.File == nil || (part.File.URL == "" && part.File.FileData == "") {
            return schema.MessageInputPart{}, errors.New("file.url or file.file_data is required")
        }
        common := schema.MessagePartCommon{MIMEType: part.File.MediaType}
        if part.File.URL != "" {
            common.URL = &part.File.URL
        }
        if part.File.FileData != "" {
            common.Base64Data = &part.File.FileData
        }
        return schema.MessageInputPart{Type: schema.ChatMessagePartTypeFileURL, File: &schema.MessageInputFile{MessagePartCommon: common, Name: part.File.Filename}}, nil
    default:
        return schema.MessageInputPart{}, fmt.Errorf("unsupported content part type %q", part.Type)
    }
}
```

Construct each Eino message with the following exact assignment pattern:

```go
content, parts, err := contentToSchema(m)
if err != nil {
    return nil, fmt.Errorf("message %d: %w", index, err)
}
msg := &schema.Message{
    Role: toSchemaRole(m.Role), Name: m.Name, Content: content,
    ReasoningContent: m.ReasoningContent, ToolCallID: m.ToolCallID,
    ToolCalls: toSchemaToolCalls(m.ToolCalls),
}
if len(parts) > 0 {
    if msg.Role != schema.User && msg.Role != schema.Tool {
        return nil, fmt.Errorf("message %d: multimodal input parts require user or tool role", index)
    }
    msg.UserInputMultiContent = parts
}
messages = append(messages, msg)
```

Add the tool-call helper used by that loop:

```go
func toSchemaToolCalls(calls []ToolCall) []schema.ToolCall {
    out := make([]schema.ToolCall, 0, len(calls))
    for _, call := range calls {
        out = append(out, schema.ToolCall{
            Index: call.Index, ID: call.ID, Type: call.Type,
            Function: schema.FunctionCall{Name: call.Function.Name, Arguments: call.Function.Arguments},
        })
    }
    return out
}
```

- [ ] **Step 5: Run all OpenAI message tests**

Run: `go test ./openai -run 'TestToSchemaMessages|TestFromSchemaMessages'`

Expected: PASS.

- [ ] **Step 6: Commit request conversion**

```bash
git add openai/request.go openai/message.go openai/message_test.go
git commit -m "feat: preserve OpenAI multimodal requests"
```

### Task 7: Make OpenAI Streaming Wire Output Exact and Add Session Usage Opt-in

**Files:**
- Modify: `openai/stream.go`
- Modify: `openai/stream_test.go`
- Modify: `cmd/server/openai_handler.go`
- Create: `cmd/server/openai_handler_test.go`

- [ ] **Step 1: Write failing SSE and usage-query tests**

```go
func TestWriteChatCompletionStreamUsesDataLinesOnly(t *testing.T) {
    var buf bytes.Buffer
    err := WriteChatCompletionStreamTo(context.Background(), &buf, nil, ChatCompletionsRequest{Model: "gpt-4o", Stream: true}, &sliceEventStream{events: []*einoai.RunEvent{
        {ID: "1-0", Type: einoai.EventReasoningDelta, Data: einoai.ReasoningData{Delta: "think"}},
        {ID: "1-1", Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "stop"}},
    }})
    if err != nil {
        t.Fatal(err)
    }
    if strings.Contains(buf.String(), "id: ") {
        t.Fatalf("unexpected SSE id field:\n%s", buf.String())
    }
    if !strings.Contains(buf.String(), `"reasoning_content":"think"`) {
        t.Fatalf("reasoning chunk missing:\n%s", buf.String())
    }
}
```

```go
func TestOpenAISubscribeRequestIncludesUsage(t *testing.T) {
    recorder := httptest.NewRecorder()
    context, _ := gin.CreateTestContext(recorder)
    context.Request = httptest.NewRequest(http.MethodPost, "/?model=gpt-4o&include_usage=true", nil)
    request := openAISubscribeRequest(context)
    if request.StreamOptions == nil || !request.StreamOptions.IncludeUsage {
        t.Fatalf("include_usage was not propagated: %#v", request)
    }
}
```

- [ ] **Step 2: Run tests and verify the current `id:` and missing query failures**

Run: `go test ./openai ./cmd/server -run 'TestWriteChatCompletionStreamUsesDataLinesOnly|TestOpenAISubscribeRequestIncludesUsage'`

Expected: FAIL because the writer emits `id:` and the subscription request has no usage option helper.

- [ ] **Step 3: Remove the SSE event-ID write**

```go
func (w chatCompletionStreamWriter) writeChunk(chunk chatCompletionChunk) error {
    body, err := json.Marshal(chunk)
    if err != nil {
        return err
    }
    if _, err := fmt.Fprintf(w.writer, "data: %s\n\n", body); err != nil {
        return err
    }
    w.flushNow()
    return nil
}
```

Mechanically replace each `w.writeChunk(eventID, state.chunk(...))` call with `w.writeChunk(state.chunk(...))`; replace the initial `out.writeChunk("", state.chunk(...))` call the same way. Do not change the sequence that writes the finish chunk, optional usage chunk, and `[DONE]`.

- [ ] **Step 4: Parse the session subscription options**

```go
func openAISubscribeRequest(c *gin.Context) openai.ChatCompletionsRequest {
    includeUsage, _ := strconv.ParseBool(c.Query("include_usage"))
    request := openai.ChatCompletionsRequest{Model: c.Query("model"), Stream: true}
    if includeUsage {
        request.StreamOptions = &openai.StreamOptions{IncludeUsage: true}
    }
    return request
}
```

Replace the literal request construction in `subscribeOpenAIEvents` with:

```go
req := openAISubscribeRequest(c)
```

- [ ] **Step 5: Add exact tool-call and usage-chunk assertions**

```go
func TestWriteChatCompletionStreamOrdersReasoningToolsFinishAndUsage(t *testing.T) {
    var buf bytes.Buffer
    err := WriteChatCompletionStreamTo(context.Background(), &buf, nil, ChatCompletionsRequest{
        Model: "gpt-4o", Stream: true,
        StreamOptions: &StreamOptions{IncludeUsage: true},
    }, &sliceEventStream{events: []*einoai.RunEvent{
        {Type: einoai.EventReasoningDelta, Data: einoai.ReasoningData{Delta: "think"}},
        {Type: einoai.EventToolCall, Data: einoai.ToolCallData{ID: "call_1", Name: "weather", Index: 0}},
        {Type: einoai.EventToolCall, Data: einoai.ToolCallData{ID: "call_1", Name: "weather", Arguments: `{"city":"郑州"}`, Index: 0}},
        {Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "tool_calls"}},
        {Type: einoai.EventTextDelta, Data: einoai.TextData{Delta: "sunny"}},
        {Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "stop", Usage: &schema.TokenUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}}},
    }})
    if err != nil {
        t.Fatal(err)
    }
    chunks := decodeChunks(t, buf.String())
    if chunks[0].Choices[0].Delta.Role != "assistant" || chunks[1].Choices[0].Delta.ReasoningContent != "think" {
        t.Fatalf("role/reasoning order is wrong: %#v", chunks)
    }
    if chunks[2].Choices[0].Delta.ToolCalls[0].ID != "call_1" || chunks[3].Choices[0].Delta.ToolCalls[0].Function.Arguments == "" {
        t.Fatalf("tool deltas are wrong: %#v", chunks[2:4])
    }
    final := chunks[len(chunks)-1]
    if len(final.Choices) != 0 || final.Usage == nil || final.Usage.TotalTokens != 7 {
        t.Fatalf("final usage chunk is wrong: %#v", final)
    }
    if !strings.HasSuffix(buf.String(), "data: [DONE]\n\n") {
        t.Fatalf("DONE is not last:\n%s", buf.String())
    }
}
```

- [ ] **Step 6: Run OpenAI stream and handler tests**

Run: `go test ./openai ./cmd/server -run 'TestWriteChatCompletionStream|TestOpenAISubscribeRequest'`

Expected: PASS.

- [ ] **Step 7: Commit OpenAI streaming changes**

```bash
git add openai/stream.go openai/stream_test.go cmd/server/openai_handler.go cmd/server/openai_handler_test.go
git commit -m "fix: align OpenAI streaming output"
```

### Task 8: Complete OpenAI Non-streaming Responses

**Files:**
- Modify: `openai/stream.go`
- Modify: `openai/stream_test.go`

- [ ] **Step 1: Write a failing non-streaming aggregation test**

```go
func TestCollectChatCompletionIncludesReasoningToolsAndUsage(t *testing.T) {
    body, err := CollectChatCompletion(context.Background(), ChatCompletionsRequest{Model: "gpt-4o"}, &sliceEventStream{events: []*einoai.RunEvent{
        {Type: einoai.EventReasoningDelta, Data: einoai.ReasoningData{Delta: "think"}},
        {Type: einoai.EventToolCall, Data: einoai.ToolCallData{ID: "call_1", Name: "weather", Index: 0}},
        {Type: einoai.EventToolCall, Data: einoai.ToolCallData{ID: "call_1", Name: "weather", Arguments: `{"city":"郑州"}`, Index: 0}},
        {Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "tool_calls", Usage: &schema.TokenUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}}},
    }})
    if err != nil {
        t.Fatal(err)
    }
    choices := body["choices"].([]map[string]any)
    message := choices[0]["message"].(map[string]any)
    if message["reasoning_content"] != "think" || len(message["tool_calls"].([]ToolCall)) != 1 {
        t.Fatalf("missing completion data: %#v", body)
    }
    if body["usage"].(*usage).TotalTokens != 7 {
        t.Fatalf("usage missing: %#v", body)
    }
}

func TestCollectChatCompletionOmitsAutomaticallyExecutedIntermediateTools(t *testing.T) {
    body, err := CollectChatCompletion(context.Background(), ChatCompletionsRequest{Model: "gpt-4o"}, &sliceEventStream{events: []*einoai.RunEvent{
        {Type: einoai.EventToolCall, Data: einoai.ToolCallData{ID: "call_1", Name: "weather", Arguments: `{}`, Index: 0}},
        {Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "tool_calls"}},
        {Type: einoai.EventToolResult, Data: einoai.ToolResultData{ToolCallID: "call_1", Name: "weather", Content: "sunny"}},
        {Type: einoai.EventTextDelta, Data: einoai.TextData{Delta: "It is sunny."}},
        {Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "stop"}},
    }})
    if err != nil {
        t.Fatal(err)
    }
    message := body["choices"].([]map[string]any)[0]["message"].(map[string]any)
    if _, exists := message["tool_calls"]; exists {
        t.Fatalf("intermediate tool calls leaked into final answer: %#v", message)
    }
}
```

- [ ] **Step 2: Run the test and verify missing fields**

Run: `go test ./openai -run TestCollectChatCompletionIncludesReasoningToolsAndUsage`

Expected: FAIL because the collector currently only accumulates text and finish reason.

- [ ] **Step 3: Add collector state for reasoning, tools, and final usage**

```go
var content strings.Builder
var reasoning strings.Builder
toolCalls := map[int]*ToolCall{}
var finalUsage *usage
finishReason := "stop"
```

Use the following event rules:

```go
case einoai.EventReasoningDelta:
    data, _ := einoai.DecodeEventData[einoai.ReasoningData](event)
    reasoning.WriteString(data.Delta)
case einoai.EventToolCall:
    data, _ := einoai.DecodeEventData[einoai.ToolCallData](event)
    call := toolCalls[data.Index]
    if call == nil {
        call = &ToolCall{ID: data.ID, Type: "function", Index: &data.Index, Function: FunctionCall{Name: data.Name}}
        toolCalls[data.Index] = call
    }
    call.Function.Arguments += data.Arguments
case einoai.EventToolResult:
    toolCalls = map[int]*ToolCall{}
case einoai.EventFinish:
    data, _ := einoai.DecodeEventData[einoai.FinishData](event)
    finishReason = normalizeFinishReason(data.FinishReason)
    finalUsage = convertUsage(data.Usage)
```

After EOF, build the response deterministically:

```go
indexes := make([]int, 0, len(toolCalls))
for index := range toolCalls {
    indexes = append(indexes, index)
}
sort.Ints(indexes)
orderedCalls := make([]ToolCall, 0, len(indexes))
for _, index := range indexes {
    orderedCalls = append(orderedCalls, *toolCalls[index])
}
message := map[string]any{"role": "assistant", "content": content.String()}
if reasoning.Len() > 0 {
    message["reasoning_content"] = reasoning.String()
}
if len(orderedCalls) > 0 {
    message["tool_calls"] = orderedCalls
}
body := map[string]any{
    "id": "chatcmpl-" + fmt.Sprintf("%d", time.Now().UnixNano()),
    "object": "chat.completion", "created": time.Now().Unix(), "model": req.Model,
    "choices": []map[string]any{{"index": 0, "message": message, "finish_reason": finishReason}},
}
if finalUsage != nil {
    body["usage"] = finalUsage
}
return body, nil
```

- [ ] **Step 4: Run all OpenAI stream/collector tests**

Run: `go test ./openai -run 'TestCollectChatCompletion|TestWriteChatCompletionStream'`

Expected: PASS.

- [ ] **Step 5: Commit non-streaming parity**

```bash
git add openai/stream.go openai/stream_test.go
git commit -m "fix: complete OpenAI non-stream responses"
```

### Task 9: Reuse Normalized Usage in AI SDK and OpenAI Adapters

**Files:**
- Modify: `aisdk/message.go`
- Modify: `aisdk/message_test.go`
- Modify: `aisdk/stream.go`
- Modify: `aisdk/stream_test.go`
- Modify: `openai/stream.go`
- Modify: `openai/stream_test.go`

- [ ] **Step 1: Write failing non-negative adapter usage tests**

```go
func TestWriteEventStreamOrdersReasoningToolsAndFinalUsage(t *testing.T) {
    var buf bytes.Buffer
    err := WriteEventStreamTo(context.Background(), &buf, nil, &aisdkSliceEventStream{events: []*einoai.RunEvent{
        {RunID: "run_1", Type: einoai.EventReasoningStart, Data: einoai.ReasoningData{ID: "reasoning_1"}},
        {RunID: "run_1", Type: einoai.EventReasoningDelta, Data: einoai.ReasoningData{ID: "reasoning_1", Delta: "think"}},
        {RunID: "run_1", Type: einoai.EventReasoningEnd, Data: einoai.ReasoningData{ID: "reasoning_1"}},
        {RunID: "run_1", Type: einoai.EventToolCall, Data: einoai.ToolCallData{ID: "call_1", Name: "weather", Arguments: `{}`, Index: 0}},
        {RunID: "run_1", Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "tool_calls"}},
        {RunID: "run_1", Type: einoai.EventToolResult, Data: einoai.ToolResultData{ToolCallID: "call_1", Name: "weather", Content: "sunny"}},
        {RunID: "run_1", Type: einoai.EventTextDelta, Data: einoai.TextData{ID: "text_1", Delta: "sunny"}},
        {RunID: "run_1", Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "stop", Usage: &schema.TokenUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}}},
    }})
    if err != nil {
        t.Fatal(err)
    }
    body := buf.String()
    ordered := []string{`"type":"start"`, `"type":"start-step"`, `"type":"reasoning-start"`, `"type":"reasoning-delta"`, `"type":"reasoning-end"`, `"type":"tool-input-start"`, `"type":"tool-input-delta"`, `"type":"tool-input-available"`, `"type":"finish-step"`, `"type":"start-step"`, `"type":"tool-output-available"`, `"type":"text-delta"`, `"type":"finish"`, "data: [DONE]"}
    position := -1
    for _, marker := range ordered {
        next := strings.Index(body[position+1:], marker)
        if next < 0 {
            t.Fatalf("missing or out-of-order %s:\n%s", marker, body)
        }
        position += next + 1
    }
    if !strings.Contains(body, `"totalTokens":7`) {
        t.Fatalf("final usage missing:\n%s", body)
    }
}

func TestUsageMetadataClampsDerivedTokenCounts(t *testing.T) {
    got := usageMetadata(&schema.TokenUsage{
        PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2,
        PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: 3},
        CompletionTokensDetails: schema.CompletionTokensDetails{ReasoningTokens: 4},
    })
    input := got["inputTokenDetails"].(map[string]any)
    output := got["outputTokenDetails"].(map[string]any)
    if input["noCacheTokens"] != 0 || output["textTokens"] != 0 {
        t.Fatalf("negative derived usage leaked: %#v", got)
    }
}
```

Add this OpenAI assertion:

```go
func TestConvertUsageUsesNormalizedCounts(t *testing.T) {
    got := convertUsage(&schema.TokenUsage{
        PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3,
        PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: 5},
        CompletionTokensDetails: schema.CompletionTokensDetails{ReasoningTokens: 4},
    })
    if got.PromptTokensDetails.CachedTokens != 5 || got.CompletionTokensDetails.ReasoningTokens != 4 {
        t.Fatalf("detail counts were lost: %#v", got)
    }
}
```

- [ ] **Step 2: Run tests and verify negative values**

Run: `go test ./aisdk ./openai -run 'TestUsageMetadataClamps|TestConvertUsageUsesNormalized'`

Expected: FAIL because AI SDK currently subtracts counts directly.

- [ ] **Step 3: Map both adapters from `einoai.NormalizeTokenUsage`**

```go
func usageMetadata(usage *schema.TokenUsage) map[string]any {
    normalized := einoai.NormalizeTokenUsage(usage)
    if normalized == nil {
        return nil
    }
    return map[string]any{
        "inputTokens": normalized.InputTokens,
        "outputTokens": normalized.OutputTokens,
        "totalTokens": normalized.TotalTokens,
        "cachedInputTokens": normalized.CachedInputTokens,
        "inputTokenDetails": map[string]any{
            "cacheReadTokens": normalized.CachedInputTokens,
            "noCacheTokens": normalized.UncachedInputTokens,
        },
        "outputTokenDetails": map[string]any{
            "textTokens": normalized.TextOutputTokens,
            "reasoningTokens": normalized.ReasoningTokens,
        },
        "reasoningTokens": normalized.ReasoningTokens,
    }
}
```

Replace OpenAI `convertUsage` with:

```go
func convertUsage(u *schema.TokenUsage) *usage {
    normalized := einoai.NormalizeTokenUsage(u)
    if normalized == nil {
        return nil
    }
    return &usage{
        PromptTokens: normalized.InputTokens,
        CompletionTokens: normalized.OutputTokens,
        TotalTokens: normalized.TotalTokens,
        PromptTokensDetails: promptTokensDetails{CachedTokens: normalized.CachedInputTokens},
        CompletionTokensDetails: completionTokensDetails{ReasoningTokens: normalized.ReasoningTokens},
    }
}
```

- [ ] **Step 4: Run adapter tests**

Run: `go test ./aisdk ./openai`

Expected: PASS.

- [ ] **Step 5: Commit shared usage behavior**

```bash
git add aisdk/message.go aisdk/message_test.go aisdk/stream.go aisdk/stream_test.go openai/stream.go openai/stream_test.go
git commit -m "refactor: share token usage normalization"
```

### Task 10: Update Protocol Documentation and Run Full Verification

**Files:**
- Modify: `README.md`
- Modify: `docs/api.md`
- Modify: `aisdk/README.md`
- Modify: `openai/README.md`

- [ ] **Step 1: Update the unified session examples**

Replace both protocol-specific session history examples with the approved `messages[].parts` format. Include text, reasoning, tool-call, tool-result, multimodal media, finish reason, and usage examples.

- [ ] **Step 2: Document the intentional breaking change**

Add a migration note with this exact guidance:

```markdown
Session history is now protocol-neutral. Clients must render `messages[].parts`; the session GET endpoints no longer return AI SDK `UIMessage` or OpenAI `ChatMessage` arrays. Live completion streams remain protocol-specific.
```

- [ ] **Step 3: Correct OpenAI stream and non-stream documentation**

Document:

- `reasoning_content` as an OpenAI-compatible extension.
- `stream_options.include_usage` behavior.
- `include_usage=true` on session subscriptions.
- pure `data:` SSE lines with no `id:` field.
- non-streaming reasoning, tool calls, and usage.
- explicit rejection of malformed multimodal parts.

- [ ] **Step 4: Format every changed Go file**

Run: `gofmt -w usage.go usage_test.go session_message.go session_message_test.go session_response_test.go service.go service_test.go runner.go runner_test.go aisdk/response.go aisdk/response_test.go aisdk/message.go aisdk/message_test.go aisdk/stream.go aisdk/stream_test.go openai/response.go openai/response_test.go openai/request.go openai/message.go openai/message_test.go openai/stream.go openai/stream_test.go cmd/server/ai_handler.go cmd/server/openai_handler.go cmd/server/openai_handler_test.go`

Expected: exit code 0.

- [ ] **Step 5: Run static analysis**

Run: `go vet ./...`

Expected: exit code 0 with no diagnostics.

- [ ] **Step 6: Run the complete test suite**

Run: `go test ./...`

Expected: every package reports `ok` and there are zero failures.

- [ ] **Step 7: Inspect the final diff and protocol requirements**

Run: `git diff --check && git status --short && git diff --stat`

Expected: no whitespace errors; only the planned source, test, and documentation files are modified.

- [ ] **Step 8: Commit documentation and final integration**

```bash
git add README.md docs/api.md aisdk/README.md openai/README.md
git commit -m "docs: document unified session protocol"
```

- [ ] **Step 9: Re-run verification after the final commit**

Run: `go vet ./... && go test ./... && git status --short`

Expected: vet and tests exit 0; the worktree is clean.

# Unified Session Message Protocol Design

## Context

The project exposes AI SDK and OpenAI-compatible adapters over the same Eino run and message store. Their real-time streams already carry text, reasoning, tool calls, finish reasons, and token usage, but their session-history responses do not have equivalent fidelity.

The AI SDK history converter merges assistant and tool steps into UI messages and preserves reasoning and usage metadata. The OpenAI history converter emits basic chat messages and currently drops reasoning, token usage, and multimodal content. Its request converter also reduces content-part arrays to text, while the non-streaming completion collector omits reasoning, tool calls, and usage.

Backward compatibility for the existing session-history JSON is not required. The new design therefore introduces one stable, protocol-neutral session format instead of exposing Eino's version-dependent `schema.Message` JSON directly.

## Goals

- Return the same lossless session-history JSON from the AI SDK and OpenAI session endpoints.
- Preserve message order and the original `system`, `user`, `assistant`, and `tool` roles.
- Preserve text, reasoning, reasoning signatures, tool calls, tool results, multimodal inputs and outputs, finish reasons, usage, and safe public metadata.
- Keep AI SDK and OpenAI real-time streams native to their respective protocols.
- Make OpenAI streaming, non-streaming, and request conversion consistently support the information available in the core event and message model.
- Reject malformed protocol input explicitly instead of silently discarding it.

## Non-goals

- Preserving the previous AI SDK or OpenAI session-history response shape.
- Making AI SDK and OpenAI streaming chunks identical to one another.
- Exposing Eino's raw JSON representation or internal `_einoai_*` metadata as the public session contract.
- Inventing an official OpenAI field for reasoning. `reasoning_content` remains an explicitly documented OpenAI-compatible extension.

## Public Session Response

Both endpoints return the same response schema:

- `GET /api/usechat/sessions/:sessionId`
- `GET /api/v1/sessions/:sessionId`

```json
{
  "run": {
    "session_id": "session_001",
    "run_id": "run_001",
    "status": "completed"
  },
  "messages": [
    {
      "id": "msg_0",
      "role": "user",
      "parts": [
        {"type": "text", "text": "分析这张图片"},
        {
          "type": "image",
          "url": "https://example.com/image.png",
          "media_type": "image/png"
        }
      ]
    },
    {
      "id": "msg_1",
      "role": "assistant",
      "parts": [
        {"type": "reasoning", "text": "需要先识别图片内容"},
        {
          "type": "tool-call",
          "tool_call_id": "call_1",
          "tool_name": "recognize_image",
          "input": {"url": "https://example.com/image.png"}
        }
      ]
    },
    {
      "id": "msg_2",
      "role": "tool",
      "parts": [
        {
          "type": "tool-result",
          "tool_call_id": "call_1",
          "tool_name": "recognize_image",
          "output": {"objects": ["cat"]}
        }
      ]
    },
    {
      "id": "msg_3",
      "role": "assistant",
      "parts": [{"type": "text", "text": "图片中有一只猫"}],
      "finish_reason": "stop",
      "usage": {
        "input_tokens": 100,
        "output_tokens": 30,
        "total_tokens": 130,
        "input_token_details": {
          "cached_tokens": 20,
          "uncached_tokens": 80
        },
        "output_token_details": {
          "reasoning_tokens": 10,
          "text_tokens": 20
        }
      }
    }
  ]
}
```

## Core Types

The core `einoai` package owns the protocol-neutral response types so the adapters cannot diverge.

```go
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
```

`SessionPart` is a tagged structure. Only fields applicable to its `type` are serialized. Supported types are:

- `text`
- `reasoning`
- `image`
- `audio`
- `video`
- `file`
- `tool-call`
- `tool-result`
- `data`

Media parts can carry `url`, `base64_data`, `media_type`, `name`, and `detail`. Reasoning parts can carry `text` and `signature`. Tool parts carry `tool_call_id`, `tool_name`, and either `input` or `output`. `data` carries the original supported JSON value plus enough type metadata to diagnose an Eino extension that the public model does not yet recognize.

`SessionUsage` uses protocol-neutral input/output terminology:

```json
{
  "input_tokens": 100,
  "output_tokens": 30,
  "total_tokens": 130,
  "input_token_details": {
    "cached_tokens": 20,
    "uncached_tokens": 80
  },
  "output_token_details": {
    "reasoning_tokens": 10,
    "text_tokens": 20
  }
}
```

Derived counts are clamped at zero if upstream detail counts exceed their totals.

## Conversion Rules

Each stored Eino message becomes exactly one session message. Assistant, tool, and later assistant messages remain separate, preserving the actual execution sequence. The session converter does not perform the AI SDK UI-message merging used by the AI SDK request and presentation adapter.

Conversion rules are:

1. Use an existing stable message ID from internal message metadata when available.
2. For history written after this change, attach a generated ID to the stored schema message.
3. For older history without an ID, generate a deterministic `msg_<index>` ID from its stable position in the returned session history.
4. Convert `ReasoningContent` and reasoning output parts to `reasoning` parts. Preserve an output part's signature.
5. Prefer input/output multimodal arrays over the plain `Content` field when Eino indicates that the arrays are authoritative.
6. Convert every tool call into a `tool-call` part and every tool-role message into a `tool-result` part.
7. Decode tool arguments and results as JSON when valid. Preserve the original string when they are not valid JSON.
8. Convert unknown but JSON-safe parts to `data` rather than dropping them.
9. Copy `ResponseMeta.FinishReason` and token usage onto the same session message.
10. Filter keys prefixed with `_einoai_` from public metadata. Preserve other JSON-safe metadata.
11. Nil messages are skipped. Messages with no content still return an empty `parts` array so the JSON type remains stable.
12. If both URL and base64 media data exist, retain both; the history endpoint reports stored state and does not choose one representation.

## Component Boundaries

- The core package defines the session types, schema-message converter, and shared usage normalization.
- The core constructor returns `(SessionRunResponse, error)` so metadata validation completes before an HTTP success response is written.
- `aisdk.NewRunResponse` and `openai.NewRunResponse` delegate to that constructor and therefore serialize identically.
- AI SDK-specific UI-message conversion remains in `aisdk` for requests and UI presentation use cases.
- OpenAI-specific chat-message conversion remains in `openai` for Chat Completions requests.
- The two stream writers continue to consume the protocol-neutral `RunEvent` stream and emit their own wire protocols.

This structure makes the session contract independent from Eino, AI SDK, and OpenAI model structs while keeping each adapter focused on its own wire protocol.

## AI SDK Stream Behavior

The AI SDK stream continues to emit UI Message Stream v1 events:

1. `start`
2. `start-step`
3. reasoning, text, and tool events in core event order
4. `finish-step` at each completed model step
5. `start-step` after an intermediate `tool_calls` finish
6. final message metadata and `finish`
7. `[DONE]`

The final `finish.messageMetadata.custom.usage` contains complete normalized usage. Direct completions and session subscriptions use the same writer and headers, including `x-vercel-ai-ui-message-stream: v1`.

## OpenAI Stream Behavior

The OpenAI writer emits `chat.completion.chunk` payloads using `data:` SSE lines followed by `data: [DONE]`. It does not emit additional SSE `id:` lines.

- The first chunk establishes the assistant role.
- Text is emitted through `delta.content`.
- Reasoning is emitted through the documented compatible extension `delta.reasoning_content`.
- Tool calls use indexed `delta.tool_calls` fragments and only include ID, type, and function name on the first fragment for a call.
- Finish reasons use OpenAI underscore naming such as `tool_calls` and `content_filter`.
- With `stream_options.include_usage=true`, non-usage chunks contain `"usage": null`, and the final pre-DONE chunk contains `choices: []` plus usage.
- Without that option, usage is omitted from chunks.
- Session subscriptions accept `include_usage=true` as a query parameter and translate it into the same stream option.
- Tool results are internal execution events and are not emitted as a non-standard OpenAI `tool_result` delta.

## OpenAI Non-streaming Behavior

The non-streaming collector returns a standard `chat.completion` envelope and includes all applicable final information:

- `message.content`
- the compatible `message.reasoning_content` extension
- `message.tool_calls` when the final completion ends for tool calls
- `finish_reason`
- top-level `usage`, including cached and reasoning token details

Intermediate automatically executed tool results are not inserted into the final Chat Completions response. They remain available in the unified session history.

## OpenAI Request Conversion

OpenAI request conversion no longer reduces content arrays to text. It supports:

- `text`
- `image_url`
- `input_audio`
- compatible video and file parts supported by Eino
- assistant reasoning content
- assistant tool calls
- tool messages and tool call IDs
- message names

Malformed parts and unsupported representations return an OpenAI-formatted request error. The converter never silently discards a non-text part.

## Error Handling

- Protocol request errors use the adapter's existing JSON error envelope.
- Errors after streaming begins use that protocol's stream error shape and terminate the stream.
- Missing optional usage, reasoning, metadata, and media fields are valid.
- Malformed tool argument/result JSON is treated as opaque text, not as an endpoint error.
- Internal metadata is filtered before serialization.
- Public metadata that cannot be represented safely as JSON causes session conversion to fail before the handler writes a success status or body. Unknown message parts still use the JSON-safe `data` fallback and are not discarded.

## Testing Strategy

Implementation follows red-green-refactor. Every changed behavior starts with a focused failing test that is observed failing for the expected reason.

Required coverage includes:

- Text, reasoning, reasoning signatures, tool calls, and tool results.
- User image, audio, video, and file inputs using URL and base64 forms.
- Assistant multimodal output.
- Unknown-part fallback without data loss.
- Usage normalization, finish reasons, negative-derived-count protection, metadata filtering, nil handling, and deterministic IDs.
- Full JSON equality between AI SDK and OpenAI session responses.
- OpenAI multimodal request conversion and explicit invalid-part errors.
- Exact AI SDK event ordering and final usage metadata.
- Exact OpenAI chunk behavior for reasoning, tool calls, usage opt-in, absence of SSE `id:` lines, and `[DONE]`.
- Non-streaming OpenAI reasoning, tool-call, finish-reason, and usage output.
- Handler tests for the session subscription `include_usage` query.
- Documentation examples for the new session schema and both stream protocols.

Final verification runs formatting, static analysis, and the complete Go test suite, including at minimum:

```bash
gofmt -w <changed-go-files>
go vet ./...
go test ./...
```

## Documentation and Migration

The root README, `docs/api.md`, `aisdk/README.md`, and `openai/README.md` are updated to show the unified session response and clarify that session history is protocol-neutral even though live streams are protocol-specific.

This is an intentional breaking change for consumers of both session GET endpoints. Release notes must call out that clients should render `messages[].parts` instead of interpreting AI SDK UI messages or OpenAI chat messages from session history.

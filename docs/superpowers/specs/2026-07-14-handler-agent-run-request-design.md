# Handler Agent Run Request Design

## Context

The example server creates its chat model in `cmd/server/main.go` and injects that configured model into every resolved Eino agent. The AI SDK and OpenAI handlers currently also copy request-level `protocol`, `model`, and `params` values into `einoai.CreateRunRequest.Metadata`.

That metadata is persisted on `RunInfo`; it does not configure the agent or select the model. Keeping request model metadata next to the configured agent makes the responsibility boundary unclear and can suggest that the request changes the Eino model when it does not.

## Decision

All four example-server run creation paths will construct `einoai.CreateRunRequest` with only:

- `SessionID`
- `Messages`
- `Agent`

The affected handlers are:

- `openAICompletions`
- `createOpenAIRun`
- `aiCompletions`
- `createAIRun`

A small `newAgentRunRequest` helper in `cmd/server` will own this construction and prevent the four call sites from drifting.

## Boundaries

- Do not remove `Metadata` from the public core `einoai.CreateRunRequest` type. Other library consumers may still use it deliberately.
- Do not use request `model`, AI SDK `params`, or protocol names to configure the Eino agent.
- Keep OpenAI `req.Model` for OpenAI wire behavior: response `model` fields and default session ID resolution.
- Keep AI SDK request decoding backward compatible; accepted `model` and `params` fields are ignored by the example server after message conversion.
- The configured model remains exclusively defined by the agent/model setup in `cmd/server/main.go`.

## Helper

```go
func newAgentRunRequest(
    sessionID string,
    messages []*schema.Message,
    agent adk.Agent,
) einoai.CreateRunRequest {
    return einoai.CreateRunRequest{
        SessionID: sessionID,
        Messages:  messages,
        Agent:     agent,
    }
}
```

## Error Handling

The change introduces no new error cases. Existing handler decoding, message conversion, agent resolution, run creation, and response errors remain unchanged.

## Testing

Add a focused unit test in `cmd/server` that constructs a request with `newAgentRunRequest` and verifies:

- session ID is preserved;
- message pointers are preserved;
- agent is preserved;
- metadata is nil.

Then run `go test ./...`, `go vet ./...`, formatting checks, and a final clean-worktree check.

## Documentation

Update example snippets that currently pass handler metadata so they show the same responsibility boundary as the implementation. Core metadata documentation remains valid because the core API still supports it.

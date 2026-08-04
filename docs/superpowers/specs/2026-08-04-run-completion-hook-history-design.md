# Run Completion Hook and Business-Owned History Design

## Context

The current `einoai` service persists both run execution state and session history in Redis. This makes the core package responsible for business data ownership, forces every consumer to use the same history representation, and causes `cmd/server` to expose history endpoints backed by implementation details of the core package.

The runner already aggregates complete assistant and tool output messages before marking a run completed. The refactor will expose that result to the caller through a per-run completion hook. `einoai` will continue to persist run metadata and events so asynchronous execution, streaming, cancellation, and reconnecting subscribers keep working, but it will stop persisting session history.

## Goals

- Make application code the owner of session history persistence.
- Deliver the complete input/output message sequence to application code after successful completion.
- Keep Redis-backed run metadata, event streams, cancellation, and status tracking.
- Keep AI SDK and OpenAI streaming behavior unchanged for real-time requests.
- Ensure cancellation and failed runs never invoke the completion hook.
- Make completion-hook failures observable without changing a successfully completed run to failed.
- Remove history reads and writes from the core service and update `cmd/server`/README examples accordingly.

## Non-goals

- Adding a built-in SQL, document, or alternative history store.
- Retrying hooks or guaranteeing delivery after process crashes.
- Returning partial assistant output as persisted history for cancelled or failed runs.
- Changing the wire format of AI SDK or OpenAI streaming events unless required by compilation after the history API removal.

## Public API

`CreateRunRequest` gains an optional per-run callback. The callback is kept in process memory and is never serialized to Redis.

```go
type RunResult struct {
    Run      *RunInfo
    Input    []*schema.Message
    Output   []*schema.Message
    Messages []*schema.Message // Input followed by complete output messages
    Usage    *schema.TokenUsage
}

type OnRunCompleted func(context.Context, *RunResult) error

type CreateRunRequest struct {
    SessionID   string
    Messages    []*schema.Message
    Agent       adk.Agent
    Metadata    map[string]any
    OnCompleted OnRunCompleted
}
```

`RunResult` is constructed only for a normal model completion. `Input` is the request snapshot, `Output` contains the complete assistant/tool messages assembled by the runner, and `Messages` is a newly allocated concatenation of both. The result owns neither caller-provided message memory nor a Redis-backed history record; callers should copy values if they mutate them after returning from the callback.

The core `Service` no longer exposes `GetMessages`. `DeleteSession` is retained only as run-artifact cleanup and cancellation; its documentation and implementation must not imply that application history is deleted. Existing consumers that need history must read/write their own store.

The completion callback is optional. A nil callback is valid and does not change run execution.

## Lifecycle and data flow

1. `CreateRun` validates session ID, agent, and input messages, snapshots the input, creates run metadata/current-run keys, and starts the asynchronous runner.
2. The runner appends protocol-neutral events to Redis while aggregating output messages in memory.
3. On an agent error, context cancellation, or explicit run cancellation, the runner emits the existing terminal events/status and exits without invoking `OnCompleted`.
4. On a normal final finish, the runner closes open blocks, commits the final assistant message, and builds `RunResult` from the input snapshot, output messages, and usage.
5. The run status is set to `completed` and the current-run pointer is cleared.
6. If `OnCompleted` is non-nil, it is invoked with a background-derived context after the run reaches `completed`. Its error is logged/recorded through the service's observability hook but does not change status or emit a second failed run.

Because the callback is in-process, a process crash before invocation loses the callback. This is intentional for the first version; durable delivery belongs in the consuming application's job/outbox system.

Redis retains only:

- run metadata and status;
- current run pointer;
- per-run event stream;
- cancellation bookkeeping held by the service process.

The session message list and per-run active message snapshot keys are removed. No `GetMessages` implementation should reconstruct history from events.

## Hook failure and observability

Hook errors are side-effect errors, not model execution errors. The run remains `completed`, and the stream remains a successful stream. The service should expose a narrow error-reporting option (or use the existing package logger if no option exists) so applications can count and log failures without coupling the core to a logging framework. The error must include session ID and run ID.

The callback runs with a non-request context derived from `context.Background()` so a disconnected HTTP client does not cancel business persistence. It should still have a bounded timeout supplied by the application or a documented default.

## `cmd/server` changes

- Pass an `OnCompleted` callback when constructing a run. The example callback demonstrates where an application would write `RunResult.Messages` to its own repository; it does not write Redis history.
- Keep direct AI SDK/OpenAI completion and event-subscription endpoints, since they consume the event stream.
- Remove or rewrite session `GET` handlers that currently call `GetMessages`. The example server should expose run status only through the core, and any history endpoint should be explicitly marked as application-owned (a minimal in-memory example is acceptable only if it is clearly sample code).
- Keep delete/cancel endpoints, documenting that delete removes run artifacts maintained by einoai, not user history.
- Deduplicate protocol-independent run creation and completion-hook wiring where practical; protocol adapters should remain responsible for request decoding and stream writing.

## README and package documentation

README examples must state that:

- einoai does not save session history;
- `CreateRunRequest.OnCompleted` receives the final message sequence only on successful completion;
- cancelled/failed runs do not invoke the hook;
- hook errors do not change run status;
- Redis is still required for the built-in run/event implementation;
- `GetMessages` is removed and application history should be queried from the application's own store.

Protocol package READMEs and `docs/api.md` must remove claims that session endpoints return Redis-backed schema history and must describe the revised run/status response.

## Error handling

- Validation errors remain synchronous errors from `CreateRun`.
- Agent and stream errors retain existing event/status behavior and never invoke the hook.
- Hook errors are reported asynchronously and do not emit `EventError` for the model run, because clients must not see a completed generation as failed.
- If constructing `RunResult` fails because message aggregation fails, the run is treated as an execution failure before completion and the hook is not called.
- Callback panics must not crash the runner goroutine; recover and report them as hook failures.

## Testing strategy

Add focused tests for:

- successful completion invokes the hook exactly once;
- the hook receives input, complete assistant/tool output, merged messages, and usage;
- nil hooks are valid;
- cancelled, failed, and malformed runs do not invoke the hook;
- hook errors leave run status as `completed` and are observable;
- callback context remains valid after the request context is cancelled;
- no session-message or active-message Redis keys are written;
- `DeleteSession` removes run artifacts but does not claim to delete application history;
- AI SDK and OpenAI stream tests continue to pass after removal of history reads.

The implementation should preserve existing runner event-builder tests and add regression coverage around multi-step tool calls, final assistant message concatenation, and usage propagation into `RunResult`.

## Compatibility and rollout

This is a deliberate API change for the pre-1.0 package. The old `GetMessages` contract and Redis history keys are removed rather than maintained as a second source of truth. Consumers should migrate by supplying `OnCompleted` and moving session-history reads to their own repository before upgrading.

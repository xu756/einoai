# Event Stream Context Cancellation Design

## Context

`einoai` implements persisted event subscriptions with Redis `XREAD COUNT 1 BLOCK 15000`. The subscription uses the HTTP/SSE request context directly. When the client disconnects or cancels the request, Redis returns `context canceled`.

In v0.0.5, `redisEventStream.Next` checks `ctx.Err()` only after attempting `XREAD`. If the context was already canceled before the next polling iteration, einoai still issues an XREAD with that canceled context. The resulting operation completes almost immediately and Redis OpenTelemetry instrumentation records it as an error span.

The AI SDK and OpenAI stream writers also treat `context.Canceled` like a server or Redis failure, attempt to emit an error event to the disconnected client, and return the cancellation as a stream error.

## Decision

Treat request `context.Canceled` as normal client stream termination while preserving all genuine errors.

The fix has two parts:

1. `redisEventStream.Next` checks `ctx.Err()` before every Redis read, including before the first XREAD.
2. AI SDK and OpenAI stream writers return successfully when `stream.Next` returns an error matching `context.Canceled`. They do not emit error SSE or `[DONE]` because the client has already canceled the request.

## Error Semantics

- `context.Canceled`: normal client disconnect; return `nil` from protocol writers.
- `io.EOF`: normal completed stream; emit the protocol's `[DONE]` marker.
- `context.DeadlineExceeded`: remain an error so configured request/server timeouts stay observable.
- Redis connection errors, decoding errors, and other stream failures: preserve current error event and return behavior.

`redisEventStream.Next` continues returning `context.Canceled` to generic core consumers. Only the HTTP protocol writers normalize it into successful client termination.

## Redis and Tracing Boundary

The Redis subscription command remains:

```text
XREAD COUNT 1 BLOCK 15000 STREAMS <run-events-key> <last-id>
```

The early context check eliminates calls made with an already-canceled context, including the observed near-zero-duration error span.

If cancellation occurs while XREAD is already blocked, the go-redis `redisotel` hook may still mark that individual Redis span as an error before einoai receives the cancellation. Eliminating those remaining spans would require service tracing configuration or a more invasive blocking-client design and is outside this fix.

## Components

- `event_stream.go`: add the pre-read context check.
- `event_stream_test.go`: prove an already-canceled context does not invoke Redis XREAD.
- `aisdk/stream.go`: normalize `context.Canceled` without writing an error event.
- `aisdk/stream_test.go`: verify canceled stream termination is silent and successful.
- `openai/stream.go`: normalize `context.Canceled` without writing an OpenAI error event.
- `openai/stream_test.go`: verify only the initial role chunk is present and no error/DONE is appended after cancellation.

## Testing Strategy

Implementation follows red-green-refactor:

1. Add a Redis hook that counts XREAD commands.
2. Call `redisEventStream.Next` with an already-canceled context and assert the count remains zero.
3. Add protocol test streams whose `Next` returns `context.Canceled`.
4. Assert both writers return nil and do not serialize cancellation as an error.
5. Run all existing event, AI SDK, OpenAI, Redis, service, vet, and full repository tests.

## Non-goals

- Filtering XREAD spans in the service's OpenTelemetry setup.
- Replacing Redis Streams or the blocking XREAD subscription mechanism.
- Treating request deadlines as successful completion.
- Changing run cancellation or agent execution semantics.

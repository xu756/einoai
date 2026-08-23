# Protocol Streams After Server-Executed Tools

## Goal

Return valid SSE for both supported wire protocols after Eino executes tools on the server, while returning the complete Eino output to the Go caller for persistence.

## Protocol boundaries

The OpenAI adapter always emits Chat Completions SSE, regardless of the request's `stream` value. It emits only standard Chat Completions fields. Eino reasoning, intermediate tool calls, and tool results are internal agent steps and are not serialized as client-executed OpenAI tool calls. The terminal agent step supplies the visible assistant text, standard finish reason, optional usage chunk, and `[DONE]`.

The AI SDK adapter emits UI Message Stream SSE. Server-executed tool input and output parts carry `providerExecuted: true`. A tool step remains open until all tool results for that step arrive, then emits `finish-step` followed by `start-step`. Tool tracking is scoped to one step so response-scoped tool call IDs can be reused safely in later steps.

Both writers obtain `FinishData.Output` from the terminal event and return it as `[]*schema.Message`. The output remains outside both wire protocols.

## Error and cancellation behavior

OpenAI writes a standard error object inside the SSE stream, then `[DONE]`, and returns the run error to Go. AI SDK writes an `error` part for failures and an `abort` part for cancellation, followed by `[DONE]`. Partial Eino output is returned when the terminal finish event contains it.

## Testing

Regression tests cover repeated tool call IDs across consecutive AI SDK steps, parallel tool results, strict OpenAI filtering, output return on success and failure, cancellation, usage ordering, and SSE termination.

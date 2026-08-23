# Protocol Streams After Server-Executed Tools Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make OpenAI Chat Completions and AI SDK UI Message Stream SSE correct after server-executed tool calls while returning complete Eino output to Go callers.

**Architecture:** Keep the protocol adapters independent. OpenAI filters internal agent steps and emits standard Chat Completions chunks; AI SDK exposes provider-executed tool lifecycle parts with state scoped per LLM step. Both consume terminal `FinishData.Output` without putting it on the wire.

**Tech Stack:** Go 1.25, Eino schema messages, Redis-backed run events, standard `testing` package.

---

### Task 1: Scope AI SDK tool state to a step

**Files:**
- Modify: `aisdk/stream_test.go`
- Modify: `aisdk/stream.go`

- [ ] **Step 1: Write the failing repeated-ID test**

Add a stream containing two complete tool steps that both use `call_0`. Assert that the wire contains two `tool-input-start`, two `tool-input-available`, two `tool-output-available`, and two intermediate `finish-step` parts.

```go
if got := strings.Count(body, `"type":"tool-input-start"`); got != 2 {
    t.Fatalf("expected two independent tool starts, got %d: %s", got, body)
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run: `go test ./aisdk -run TestWriteEventStreamScopesRepeatedToolCallIDPerStep -count=1`

Expected: FAIL because the second call reuses the completed state from the first step.

- [ ] **Step 3: Reset state after closing a tool step**

Add a helper and call it only after all outputs for the pending step have been written:

```go
func beginNextStep(state *streamState) {
    state.pendingStepFinish = false
    state.toolCalls = make(map[string]*toolState)
    state.toolOrder = nil
}
```

- [ ] **Step 4: Run the focused test and verify GREEN**

Run: `go test ./aisdk -run TestWriteEventStreamScopesRepeatedToolCallIDPerStep -count=1`

Expected: PASS.

### Task 2: Verify parallel server-executed tools close once

**Files:**
- Modify: `aisdk/stream_test.go`
- Modify only if required: `aisdk/stream.go`

- [ ] **Step 1: Write the parallel-tool regression test**

Stream two tool calls followed by the intermediate finish marker and two results. Assert no `finish-step` is emitted after the first result and exactly one is emitted after the second result before the next text step.

- [ ] **Step 2: Run the focused test**

Run: `go test ./aisdk -run TestWriteEventStreamWaitsForAllParallelToolResults -count=1`

Expected: PASS with the current completion accounting; if it fails, minimally correct `allToolOutputsCompleted`.

### Task 3: Verify strict OpenAI SSE behavior

**Files:**
- Modify: `openai/stream_test.go`
- Modify only if required: `openai/stream.go`

- [ ] **Step 1: Add/adjust protocol assertions**

Assert the stream contains the role chunk, final visible text, terminal standard finish reason, optional usage chunk, and `[DONE]`; assert it omits `reasoning_content`, internal `tool_calls`, and tool results.

- [ ] **Step 2: Run OpenAI protocol tests**

Run: `go test ./openai -count=1`

Expected: PASS.

### Task 4: Verify returned Eino output and terminal behavior

**Files:**
- Test: `event_decode_test.go`
- Test: `runner_test.go`
- Test: `aisdk/stream_test.go`
- Test: `openai/stream_test.go`

- [ ] **Step 1: Run output and failure tests repeatedly**

Run: `go test . ./aisdk ./openai -run 'Output|RunError|Cancelled' -count=10`

Expected: PASS.

- [ ] **Step 2: Run formatting and static checks**

Run: `gofmt -w aisdk/stream.go aisdk/stream_test.go openai/stream.go openai/stream_test.go event.go event_decode_test.go runner.go runner_test.go service.go cmd/server/ai_handler.go cmd/server/openai_handler.go`

Run: `git diff --check && go vet ./...`

Expected: both commands exit successfully.

- [ ] **Step 3: Run the complete test suite**

Run: `go test ./...`

Expected: PASS. If the pre-existing Redis close test still fails, report it separately with the focused result and do not describe the full suite as green.

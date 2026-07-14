# Event Stream Context Cancellation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent already-canceled HTTP stream contexts from issuing Redis XREAD commands and treat client cancellation as normal AI SDK/OpenAI stream termination.

**Architecture:** Keep core stream cancellation visible as `context.Canceled`, but check it before every Redis read. Normalize only that error at the two HTTP protocol writer boundaries so real Redis failures, request deadlines, decoding failures, and completed EOF streams keep their current behavior.

**Tech Stack:** Go 1.25, go-redis v9, Redis Streams, CloudWeGo Eino events, AI SDK/OpenAI SSE writers, Go testing with miniredis.

---

## File Structure

- Create `event_stream_test.go`: count Redis XREAD calls for an already-canceled context.
- Modify `event_stream.go`: check context before entering each blocking Redis read.
- Modify `aisdk/stream_test.go`: reproduce canceled AI SDK stream termination.
- Modify `aisdk/stream.go`: return nil without error SSE on `context.Canceled`.
- Modify `openai/stream_test.go`: reproduce canceled OpenAI stream termination.
- Modify `openai/stream.go`: return nil without OpenAI error or DONE on `context.Canceled`.
- Modify `README.md`, `aisdk/README.md`, and `openai/README.md`: document cancellation semantics and the remaining redisotel boundary.

### Task 1: Avoid XREAD When the Context Is Already Canceled

**Files:**
- Create: `event_stream_test.go`
- Modify: `event_stream.go`

- [ ] **Step 1: Write a failing XREAD-count test**

```go
package einoai

import (
    "context"
    "errors"
    "strings"
    "testing"

    "github.com/redis/go-redis/v9"
)

type xreadCountingHook struct {
    calls int
}

func (h *xreadCountingHook) DialHook(next redis.DialHook) redis.DialHook {
    return next
}

func (h *xreadCountingHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
    return func(ctx context.Context, cmd redis.Cmder) error {
        if strings.EqualFold(cmd.Name(), "xread") {
            h.calls++
        }
        return next(ctx, cmd)
    }
}

func (h *xreadCountingHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
    return next
}

func TestRedisEventStreamDoesNotReadAfterContextCancellation(t *testing.T) {
    store, cleanup := newTestRedisStore(t)
    defer cleanup()
    hook := &xreadCountingHook{}
    store.rdb.AddHook(hook)

    ctx, cancel := context.WithCancel(context.Background())
    cancel()

    stream := &redisEventStream{store: store, sessionID: "s1", runID: "r1"}
    _, err := stream.Next(ctx)
    if !errors.Is(err, context.Canceled) {
        t.Fatalf("expected context cancellation, got %v", err)
    }
    if hook.calls != 0 {
        t.Fatalf("canceled context issued %d XREAD commands", hook.calls)
    }
}
```

- [ ] **Step 2: Run the test and verify RED**

Run: `env GOCACHE=/tmp/edgeinfer-go-build go test ./... -run TestRedisEventStreamDoesNotReadAfterContextCancellation -count=1`

Expected: FAIL because the current implementation issues one XREAD before checking `ctx.Err()`.

- [ ] **Step 3: Add the pre-read context check**

```go
func (s *redisEventStream) Next(ctx context.Context) (*RunEvent, error) {
    if s.closed {
        return nil, io.EOF
    }

    for {
        if err := ctx.Err(); err != nil {
            return nil, err
        }
        events, err := s.store.readAfter(ctx, s.sessionID, s.runID, s.lastID, 15*time.Second, 1)
        if err != nil {
            return nil, err
        }
        if len(events) > 0 {
            s.lastID = events[0].ID
            return events[0], nil
        }

        run, err := s.store.getRun(ctx, s.sessionID, s.runID)
        if err != nil {
            return nil, err
        }
        if run == nil || isTerminalRunStatus(run.Status) {
            return nil, io.EOF
        }
    }
}
```

Remove the redundant context check at the bottom of the loop because every iteration now checks before Redis access.

- [ ] **Step 4: Run the focused test**

Run: `env GOCACHE=/tmp/edgeinfer-go-build go test ./... -run TestRedisEventStreamDoesNotReadAfterContextCancellation -count=1`

Expected: PASS.

- [ ] **Step 5: Commit core cancellation handling**

```bash
git add event_stream.go event_stream_test.go
git commit -m "fix: avoid Redis reads after stream cancellation"
```

### Task 2: Normalize Client Cancellation in Both Protocol Writers

**Files:**
- Modify: `aisdk/stream.go`
- Modify: `aisdk/stream_test.go`
- Modify: `openai/stream.go`
- Modify: `openai/stream_test.go`

- [ ] **Step 1: Write failing protocol cancellation tests**

Add this stream type to both protocol test packages:

```go
type canceledEventStream struct{}

func (*canceledEventStream) Next(context.Context) (*einoai.RunEvent, error) {
    return nil, context.Canceled
}

func (*canceledEventStream) Close() error {
    return nil
}
```

AI SDK test:

```go
func TestWriteEventStreamTreatsContextCancellationAsClientDisconnect(t *testing.T) {
    var buf bytes.Buffer
    err := WriteEventStreamTo(context.Background(), &buf, nil, &canceledEventStream{})
    if err != nil {
        t.Fatalf("client cancellation must not be a stream error: %v", err)
    }
    if buf.Len() != 0 {
        t.Fatalf("canceled client must not receive error or DONE data: %q", buf.String())
    }
}
```

OpenAI test:

```go
func TestWriteChatCompletionStreamTreatsContextCancellationAsClientDisconnect(t *testing.T) {
    var buf bytes.Buffer
    err := WriteChatCompletionStreamTo(
        context.Background(),
        &buf,
        nil,
        ChatCompletionsRequest{Model: "gpt-4o", Stream: true},
        &canceledEventStream{},
    )
    if err != nil {
        t.Fatalf("client cancellation must not be a stream error: %v", err)
    }
    body := buf.String()
    if strings.Contains(body, `"error"`) || strings.Contains(body, "[DONE]") {
        t.Fatalf("canceled client received terminal error data: %s", body)
    }
}
```

- [ ] **Step 2: Run tests and verify RED**

Run: `env GOCACHE=/tmp/edgeinfer-go-build go test ./aisdk ./openai -run 'ContextCancellationAsClientDisconnect' -count=1`

Expected: both tests FAIL because writers currently return `context.Canceled` and serialize it as an error.

- [ ] **Step 3: Normalize only `context.Canceled`**

In each writer loop, insert this branch after `io.EOF` and before generic errors:

```go
if errors.Is(err, context.Canceled) {
    return nil
}
```

Import `errors` in both protocol stream files. Do not normalize `context.DeadlineExceeded`.

- [ ] **Step 4: Run protocol tests**

Run: `env GOCACHE=/tmp/edgeinfer-go-build go test ./aisdk ./openai -count=1`

Expected: PASS.

- [ ] **Step 5: Commit protocol cancellation handling**

```bash
git add aisdk/stream.go aisdk/stream_test.go openai/stream.go openai/stream_test.go
git commit -m "fix: treat canceled streams as client disconnects"
```

### Task 3: Document and Verify Cancellation Semantics

**Files:**
- Modify: `README.md`
- Modify: `aisdk/README.md`
- Modify: `openai/README.md`

- [ ] **Step 1: Document cancellation behavior**

Add these explicit points to the stream sections:

```markdown
- Client `context.Canceled` is treated as a normal disconnect: no error SSE and no `[DONE]` write is attempted.
- `context.DeadlineExceeded` and genuine Redis/stream errors remain observable errors.
- A cancellation that occurs while XREAD is already blocked can still be marked as an error by the service's redisotel hook; einoai prevents new XREAD calls after cancellation but does not configure service tracing.
```

- [ ] **Step 2: Run formatting and static analysis**

Run: `gofmt -w event_stream.go event_stream_test.go aisdk/stream.go aisdk/stream_test.go openai/stream.go openai/stream_test.go && env GOCACHE=/tmp/edgeinfer-go-build go vet ./...`

Expected: exit code 0 with no diagnostics.

- [ ] **Step 3: Run the complete test suite without cache**

Run: `env GOCACHE=/tmp/edgeinfer-go-build go test ./... -count=1`

Expected: all packages report `ok` with zero failures.

- [ ] **Step 4: Inspect and commit documentation**

Run: `git diff --check && git status --short && git diff --stat`

Expected: no whitespace errors; only the three documentation files remain after code commits.

```bash
git add README.md aisdk/README.md openai/README.md
git commit -m "docs: explain stream cancellation behavior"
```

- [ ] **Step 5: Verify final committed state**

Run: `env GOCACHE=/tmp/edgeinfer-go-build go vet ./... && env GOCACHE=/tmp/edgeinfer-go-build go test ./... -count=1 && git status --short`

Expected: vet and tests exit 0; worktree is clean.

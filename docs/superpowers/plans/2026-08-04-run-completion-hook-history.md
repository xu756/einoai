# Run Completion Hook and Business-Owned History Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove Redis-backed session history from `einoai`, expose successful run results through a completion hook, and update adapters, `cmd/server`, tests, and documentation.

**Architecture:** Redis remains the run/event backend. The runner keeps input snapshots and aggregated output messages in memory, creates a `RunResult` only for normal completion, marks the run completed, and invokes an optional in-process `OnCompleted` callback with a background-derived context. Application code owns all history persistence and history reads.

**Tech Stack:** Go, CloudWeGo Eino ADK, `schema.Message`, Redis Streams, Gin, AI SDK/OpenAI protocol adapters, `miniredis` tests.

---

### Task 1: Define the completion result API and remove history from the service contract

**Files:**
- Modify: `run.go`
- Modify: `service.go`
- Test: `service_test.go`
- Test: `runner_test.go`

- [ ] **Step 1: Write failing API tests**

Add tests that construct a `CreateRunRequest` with `OnCompleted`, assert the callback receives `Input`, `Output`, `Messages`, and `Usage`, and assert the exported `Service` contract no longer requires `GetMessages`. Use the existing test Redis helper and a deterministic fake agent/event iterator already used by runner tests.

- [ ] **Step 2: Run the focused tests and verify failure**

Run `env GOCACHE=/tmp/edgeinfer-go-build go test ./... -run 'Test.*Completed|Test.*History' -count=1`.

Expected: compile/test failure because `OnCompleted`, `RunResult`, and the new service behavior do not yet exist.

- [ ] **Step 3: Add the public types and request field**

In `run.go`, add:

```go
type RunResult struct {
    Run      *RunInfo
    Input    []*schema.Message
    Output   []*schema.Message
    Messages []*schema.Message
    Usage    *schema.TokenUsage
}

type OnRunCompleted func(context.Context, *RunResult) error

type CompletionErrorHandler func(context.Context, string, string, error)
```

Add `OnCompleted OnRunCompleted` to `CreateRunRequest`. Add a `WithCompletionErrorHandler` service option using `CompletionErrorHandler`; its default logs session ID, run ID, and hook error. Remove `GetMessages` from `Service`. Keep `DeleteSession`, but update its comment to say it deletes einoai run artifacts and cancels active execution; it does not delete application history.

- [ ] **Step 4: Run package tests**

Run `env GOCACHE=/tmp/edgeinfer-go-build go test ./... -run 'Test.*Completed|Test.*History' -count=1` and then `env GOCACHE=/tmp/edgeinfer-go-build go test ./...`.

Expected: the new API tests still fail only on callback invocation; unrelated existing tests compile after callers stop relying on `Service.GetMessages`.

- [ ] **Step 5: Commit the API boundary**

Run `git add run.go service.go service_test.go runner_test.go && git commit -m "feat: expose completed run result hook"`.

### Task 2: Remove session-history storage and active snapshots from Redis

**Files:**
- Modify: `service.go`
- Modify: `store_redis.go`
- Modify: `store_redis_test.go`
- Modify: `service_test.go`

- [ ] **Step 1: Add failing storage assertions**

Add a test that creates a run with input messages, waits for completion, scans `chat:sessions:<session>:*`, and asserts no `messages` or `active_messages` keys exist. Add a test that `DeleteSession` removes run metadata/events/current-run keys while not invoking any application history operation (there is no history store in the service).

- [ ] **Step 2: Run the storage tests and verify failure**

Run `env GOCACHE=/tmp/edgeinfer-go-build go test ./... -run 'Test.*Store|Test.*DeleteSession|Test.*History' -count=1`.

Expected: failure because `CreateRun` still writes active snapshots and successful execution still commits session messages.

- [ ] **Step 3: Delete history-specific Redis methods and keys**

Remove `sessionMessagesKey`, `activeMessagesKey`, `getSessionMessages`, `setActiveMessages`, `replaceSessionMessages`, and `commitRunMessages` from `store_redis.go`. Preserve run metadata, current-run, event stream, expiration, and delete-session scan behavior.

- [ ] **Step 4: Stop history writes in the service**

In `CreateRun`, retain an in-memory input snapshot and pass it to `executeRun`; remove `setActiveMessages`. In cancellation, failure, and success branches remove all `commitRunMessages` calls. Remove `GetMessages` implementation entirely. Keep input snapshots local to the runner so cancellation/error never persists partial output.

- [ ] **Step 5: Update tests and run them**

Delete or rewrite tests that assert Redis history replacement. Run `env GOCACHE=/tmp/edgeinfer-go-build go test ./...`.

Expected: all remaining core tests pass and no history key is created.

- [ ] **Step 6: Commit the storage boundary**

Run `git add service.go store_redis.go store_redis_test.go service_test.go && git commit -m "refactor: remove session history persistence"`.

### Task 3: Build and safely invoke `RunResult` completion hooks

**Files:**
- Modify: `service.go`
- Modify: `runner.go`
- Test: `service_test.go`
- Test: `runner_test.go`

- [ ] **Step 1: Add callback behavior tests**

Cover exactly-once invocation on normal completion, nil callback, no invocation on cancellation, no invocation on agent error, hook errors leaving `RunStatusCompleted`, callback context remaining usable after the request context is cancelled, and panic recovery. Assert `Messages` is a newly allocated `Input + Output` slice and `Usage` matches the final response metadata.

- [ ] **Step 2: Run tests to observe failure**

Run `env GOCACHE=/tmp/edgeinfer-go-build go test ./... -run 'Test.*Completed|Test.*Cancel|Test.*Failure|Test.*Hook' -count=1`.

Expected: callback counters remain zero because the runner has no completion callback path.

- [ ] **Step 3: Thread the callback through `CreateRun` and `executeRun`**

Extend the internal `executeRun` arguments with the input snapshot and `OnRunCompleted`. Do not store the function in Redis. Preserve the original request order and message IDs in the input snapshot.

- [ ] **Step 4: Invoke only after successful completion**

After final assistant aggregation, create:

```go
result := &RunResult{
    Run: run,
    Input: inputMessages,
    Output: state.outputMessages,
    Messages: append(append([]*schema.Message{}, inputMessages...), state.outputMessages...),
    Usage: state.usage,
}
```

Set completed status and clear current-run before invoking the callback. Invoke it with `context.Background()` (the callback may create its own timeout). Wrap invocation with `defer recover`; call the configured `CompletionErrorHandler` for returned errors and panics, including session/run IDs. Never call this path from cancellation/error/deleted-session branches.

- [ ] **Step 5: Run focused and full tests**

Run `env GOCACHE=/tmp/edgeinfer-go-build go test ./... -run 'Test.*Hook|Test.*Completed|Test.*Cancel|Test.*Failure' -count=1` and `env GOCACHE=/tmp/edgeinfer-go-build go test ./...`.

Expected: callback tests pass; all existing event and stream tests remain green.

- [ ] **Step 6: Commit hook execution**

Run `git add service.go runner.go service_test.go runner_test.go && git commit -m "feat: invoke completion hook with run result"`.

### Task 4: Refactor protocol response paths away from `GetMessages`

**Files:**
- Modify: `aisdk/response.go`
- Modify: `openai/response.go`
- Modify: `session_response_test.go`
- Modify: `cmd/server/ai_handler.go`
- Modify: `cmd/server/openai_handler.go`
- Modify: `cmd/server/run_request.go`
- Test: `cmd/server/run_request_test.go`

- [ ] **Step 1: Add compile-focused adapter tests**

Rewrite response tests to assert run-only responses do not require a history argument. Keep stream writer tests unchanged. Add tests that malformed/absent history is not read from the core service.

- [ ] **Step 2: Run adapter tests and observe failure**

Run `env GOCACHE=/tmp/edgeinfer-go-build go test ./aisdk ./openai ./cmd/server -count=1`.

Expected: compilation fails because `NewRunResponse` and handlers still require `messages` and call `GetMessages`.

- [ ] **Step 3: Simplify run responses**

Change `aisdk.NewRunResponse` and `openai.NewRunResponse` to accept only `run *einoai.RunInfo` and return the same run-only envelope, for example `{"run":{"session_id":"...","run_id":"...","status":"running"}}`. Remove the `messages` field and preserve identical JSON between adapters.

- [ ] **Step 4: Update handlers**

Remove `GetMessages` calls from `getAIRun` and `getOpenAIRun`. Keep create, subscribe, cancel, and delete flows. Return run status without claiming that einoai owns session history.

- [ ] **Step 5: Run adapter and repository tests**

Run `env GOCACHE=/tmp/edgeinfer-go-build go test ./aisdk ./openai ./cmd/server -count=1` and `env GOCACHE=/tmp/edgeinfer-go-build go test ./...`.

- [ ] **Step 6: Commit adapter refactor**

Run `git add aisdk/response.go openai/response.go session_response_test.go cmd/server/ai_handler.go cmd/server/openai_handler.go cmd/server/run_request.go cmd/server/run_request_test.go && git commit -m "refactor: remove core history reads from adapters"`.

### Task 5: Wire an application-owned completion hook in `cmd/server`

**Files:**
- Modify: `cmd/server/main.go`
- Modify: `cmd/server/run_request.go`
- Modify: `cmd/server/ai_handler.go`
- Modify: `cmd/server/openai_handler.go`
- Test: `cmd/server/run_request_test.go`

- [ ] **Step 1: Add a test seam for hook wiring**

Make run-request construction accept an `einoai.OnRunCompleted` value and assert it is preserved in the returned `CreateRunRequest`. The test must not require a real Redis or HTTP server.

- [ ] **Step 2: Run the focused server test**

Run `env GOCACHE=/tmp/edgeinfer-go-build go test ./cmd/server -run 'Test.*RunRequest|Test.*Hook' -count=1`.

Expected: failure because the helper currently only accepts session ID, messages, and agent.

- [ ] **Step 3: Add explicit application-owned hook wiring**

Add an `onRunCompleted` method on `app` (or an injected function field) that is the documented integration point for writing `RunResult.Messages` to the application repository. The sample implementation may log the run/session IDs and message count, but must not call einoai history APIs or Redis message keys. Pass this hook through both AI SDK and OpenAI run creation paths.

- [ ] **Step 4: Run server tests and full tests**

Run `env GOCACHE=/tmp/edgeinfer-go-build go test ./cmd/server -count=1` and `env GOCACHE=/tmp/edgeinfer-go-build go test ./...`.

- [ ] **Step 5: Commit server wiring**

Run `git add cmd/server/main.go cmd/server/run_request.go cmd/server/ai_handler.go cmd/server/openai_handler.go cmd/server/run_request_test.go && git commit -m "refactor: wire application completion hook in server"`.

### Task 6: Rewrite documentation and API examples

**Files:**
- Modify: `README.md`
- Modify: `aisdk/README.md`
- Modify: `openai/README.md`
- Modify: `docs/api.md`

- [ ] **Step 1: Replace history ownership claims**

Document that einoai stores only run metadata/events, that `OnCompleted` receives the final result only on normal completion, that cancelled/failed runs do not invoke it, and that hook errors do not change status.

- [ ] **Step 2: Update code examples**

Replace `GetMessages` examples with an application repository lookup. Show:

```go
run, err := svc.CreateRun(ctx, einoai.CreateRunRequest{
    SessionID: sessionID,
    Messages: messages,
    Agent: agent,
    OnCompleted: func(ctx context.Context, result *einoai.RunResult) error {
        return historyRepo.Replace(ctx, result.Run.SessionID, result.Messages)
    },
})
```

State that production applications should add their own timeout/idempotency policy around `historyRepo.Replace`.

- [ ] **Step 3: Remove stale endpoint descriptions**

Update session GET/delete descriptions so they do not imply core-owned history deletion or retrieval. Preserve stream and cancellation endpoint documentation.

- [ ] **Step 4: Validate docs**

Run `rg -n "GetMessages|session history|active_messages|sessionMessages|Redis.*history|历史.*Redis" README.md aisdk/README.md openai/README.md docs/api.md` and remove every stale core-history claim. Run `git diff --check`.

- [ ] **Step 5: Commit documentation**

Run `git add README.md aisdk/README.md openai/README.md docs/api.md && git commit -m "docs: describe application-owned message history"`.

### Task 7: Final verification and compatibility audit

**Files:**
- Modify: any files identified by test/compiler failures only

- [ ] **Step 1: Run formatting and static checks**

Run `gofmt -w run.go service.go runner.go store_redis.go aisdk/response.go openai/response.go cmd/server/*.go` followed by `git diff --check`.

- [ ] **Step 2: Run all tests**

Run `env GOCACHE=/tmp/edgeinfer-go-build go test ./...`.

Expected: PASS for core, protocol adapters, and `cmd/server`.

- [ ] **Step 3: Audit public API references**

Run `rg -n "GetMessages|commitRunMessages|setActiveMessages|getActiveMessages|sessionMessagesKey|activeMessagesKey" . --glob '*.go' --glob '*.md'`.

Expected: no production references remain; only migration notes may mention removed names.

- [ ] **Step 4: Inspect final diff and status**

Run `git diff HEAD~6..HEAD --stat`, `git status --short`, and `git log -7 --oneline`.

Confirm each commit is focused, no unrelated user changes were modified, and the final worktree is clean except for intentional user files.

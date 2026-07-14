# Handler Agent Run Request Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every example-server handler create Eino runs with only session ID, messages, and the configured agent, without request-derived run metadata.

**Architecture:** Add one focused constructor in `cmd/server` that owns the handler-to-core request boundary. Route all four AI SDK/OpenAI run creation paths through it while leaving the public core metadata API and protocol-level OpenAI model handling unchanged.

**Tech Stack:** Go 1.25, CloudWeGo Eino ADK/schema, Gin example server, Go testing.

---

## File Structure

- Create `cmd/server/run_request.go`: construct handler-originated `einoai.CreateRunRequest` values.
- Create `cmd/server/run_request_test.go`: verify the request contains only session ID, messages, and agent.
- Modify `cmd/server/ai_handler.go`: use the shared constructor in both AI SDK run paths.
- Modify `cmd/server/openai_handler.go`: use the shared constructor in both OpenAI run paths while retaining protocol-level `req.Model` usage.
- Modify `README.md`, `aisdk/README.md`, and `openai/README.md`: remove request metadata from example handler snippets.

### Task 1: Define and Adopt the Handler Run Request Boundary

**Files:**
- Create: `cmd/server/run_request.go`
- Create: `cmd/server/run_request_test.go`
- Modify: `cmd/server/ai_handler.go`
- Modify: `cmd/server/openai_handler.go`

- [ ] **Step 1: Write the failing constructor test**

```go
package main

import (
    "context"
    "testing"

    "github.com/cloudwego/eino/adk"
    "github.com/cloudwego/eino/schema"
)

type runRequestTestAgent struct{}

func (*runRequestTestAgent) Name(context.Context) string { return "test" }
func (*runRequestTestAgent) Description(context.Context) string { return "test agent" }
func (*runRequestTestAgent) Run(
    context.Context,
    *adk.AgentInput,
    ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
    return nil
}

func TestNewAgentRunRequestContainsOnlyAgentInputs(t *testing.T) {
    agent := &runRequestTestAgent{}
    messages := []*schema.Message{{Role: schema.User, Content: "hello"}}

    got := newAgentRunRequest("session_1", messages, agent)

    if got.SessionID != "session_1" {
        t.Fatalf("unexpected session ID: %q", got.SessionID)
    }
    if len(got.Messages) != 1 || got.Messages[0] != messages[0] {
        t.Fatalf("messages were not preserved: %#v", got.Messages)
    }
    if got.Agent != agent {
        t.Fatalf("agent was not preserved: %#v", got.Agent)
    }
    if got.Metadata != nil {
        t.Fatalf("handler metadata must be nil: %#v", got.Metadata)
    }
}
```

- [ ] **Step 2: Run the test and verify RED**

Run: `env GOCACHE=/tmp/edgeinfer-go-build go test ./cmd/server -run TestNewAgentRunRequestContainsOnlyAgentInputs -count=1`

Expected: FAIL because `newAgentRunRequest` is undefined.

- [ ] **Step 3: Implement the minimal constructor**

```go
package main

import (
    "github.com/cloudwego/eino/adk"
    "github.com/cloudwego/eino/schema"
    "github.com/xu756/einoai"
)

func newAgentRunRequest(sessionID string, messages []*schema.Message, agent adk.Agent) einoai.CreateRunRequest {
    return einoai.CreateRunRequest{
        SessionID: sessionID,
        Messages:  messages,
        Agent:     agent,
    }
}
```

- [ ] **Step 4: Route all four handler paths through the constructor**

Use these exact calls:

```go
run, err := a.svc.CreateRun(c.Request.Context(), newAgentRunRequest(
    "usechat-completions",
    messages,
    agent,
))
```

```go
run, err := a.svc.CreateRun(c.Request.Context(), newAgentRunRequest(
    sessionID,
    messages,
    agent,
))
```

```go
run, err := a.svc.CreateRun(c.Request.Context(), newAgentRunRequest(
    openai.ResolveSessionID(req, c.GetHeader("X-Session-ID"), c.Query("sessionId")),
    messages,
    agent,
))
```

Use the session-ID form for `createOpenAIRun` as well. Delete every handler `Metadata` map; do not change `req.Model` passed to OpenAI stream/collector functions or session-ID resolution.

- [ ] **Step 5: Run focused and package tests**

Run: `env GOCACHE=/tmp/edgeinfer-go-build go test ./cmd/server -count=1`

Expected: PASS.

- [ ] **Step 6: Commit the handler boundary**

```bash
git add cmd/server/run_request.go cmd/server/run_request_test.go cmd/server/ai_handler.go cmd/server/openai_handler.go
git commit -m "refactor: isolate agent run request inputs"
```

### Task 2: Align Documentation and Verify the Repository

**Files:**
- Modify: `README.md`
- Modify: `aisdk/README.md`
- Modify: `openai/README.md`

- [ ] **Step 1: Update example run creation snippets**

In each example, remove the complete `Metadata` block so the shown request contains exactly:

```go
einoai.CreateRunRequest{
    SessionID: sessionID,
    Messages:  messages,
    Agent:     agent,
}
```

For the OpenAI direct-completions example, keep `openai.ResolveSessionID(...)` as the `SessionID` value.

- [ ] **Step 2: Document the responsibility boundary**

Add this statement near the handler examples:

```markdown
The example server does not copy request `model`, protocol, or AI SDK `params` into run metadata. The Eino model is selected only by the configured agent. OpenAI `model` remains a wire-protocol field used in response chunks and default session ID resolution.
```

- [ ] **Step 3: Format and statically analyze**

Run: `gofmt -w cmd/server/run_request.go cmd/server/run_request_test.go cmd/server/ai_handler.go cmd/server/openai_handler.go && env GOCACHE=/tmp/edgeinfer-go-build go vet ./...`

Expected: exit code 0 with no diagnostics.

- [ ] **Step 4: Run the complete test suite without cache**

Run: `env GOCACHE=/tmp/edgeinfer-go-build go test ./... -count=1`

Expected: all packages report `ok` with zero failures.

- [ ] **Step 5: Check the final diff and commit documentation**

Run: `git diff --check && git status --short && git diff --stat`

Expected: no whitespace errors; only the planned documentation files remain uncommitted after the code commit.

```bash
git add README.md aisdk/README.md openai/README.md
git commit -m "docs: clarify agent model configuration"
```

- [ ] **Step 6: Verify the final committed state**

Run: `env GOCACHE=/tmp/edgeinfer-go-build go vet ./... && env GOCACHE=/tmp/edgeinfer-go-build go test ./... -count=1 && git status --short`

Expected: vet and tests exit 0; worktree is clean.

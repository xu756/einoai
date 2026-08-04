package main

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/xu756/einoai"
)

type runRequestTestAgent struct{}

func (*runRequestTestAgent) Name(context.Context) string {
	return "test"
}

func (*runRequestTestAgent) Description(context.Context) string {
	return "test agent"
}

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
	hook := func(context.Context, *einoai.RunResult) error { return nil }

	got := newAgentRunRequest("session_1", messages, agent, hook)

	if got.SessionID != "session_1" {
		t.Fatalf("unexpected session ID: %q", got.SessionID)
	}
	if len(got.Messages) != 1 || got.Messages[0] != messages[0] {
		t.Fatalf("messages were not preserved: %#v", got.Messages)
	}
	if got.Agent != agent {
		t.Fatalf("agent was not preserved: %#v", got.Agent)
	}
	if got.OnCompleted == nil {
		t.Fatal("completion hook was not preserved")
	}
	if got.Metadata != nil {
		t.Fatalf("handler metadata must be nil: %#v", got.Metadata)
	}
}

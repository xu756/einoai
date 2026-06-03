package main

import (
	"context"
	"testing"
)

func TestAgentManagerCancelSessionRunCancelsCurrentRun(t *testing.T) {
	store, cleanup := newTestRunStore(t)
	defer cleanup()

	ctx := context.Background()
	if err := store.InitRun(ctx, "session-1", "run-1", "hello"); err != nil {
		t.Fatal(err)
	}

	manager := &AgentManager{runStore: store}
	runCtx, cancel := context.WithCancel(ctx)
	manager.registerRunCancel("run-1", cancel)

	run, ok, err := manager.CancelSessionRun(ctx, "session-1", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected run to exist")
	}
	if run.Status != RunStatusCanceling {
		t.Fatalf("expected canceling status, got %s", run.Status)
	}

	select {
	case <-runCtx.Done():
	default:
		t.Fatal("expected run context to be canceled")
	}

	stored, err := store.GetRun(ctx, "session-1", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != RunStatusCanceling {
		t.Fatalf("expected stored canceling status, got %s", stored.Status)
	}
}

func TestAgentManagerCancelSessionRunReturnsFalseForMissingRun(t *testing.T) {
	store, cleanup := newTestRunStore(t)
	defer cleanup()

	manager := &AgentManager{runStore: store}

	run, ok, err := manager.CancelSessionRun(context.Background(), "session-1", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected missing run")
	}
	if run != nil {
		t.Fatalf("expected nil run, got %#v", run)
	}
}

func TestAgentManagerCancelSessionRunReturnsFalseWhenRunIDDoesNotMatch(t *testing.T) {
	store, cleanup := newTestRunStore(t)
	defer cleanup()

	ctx := context.Background()
	if err := store.InitRun(ctx, "session-1", "run-1", "hello"); err != nil {
		t.Fatal(err)
	}

	manager := &AgentManager{runStore: store}

	run, ok, err := manager.CancelSessionRun(ctx, "session-1", "run-other")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected mismatched run to be ignored")
	}
	if run != nil {
		t.Fatalf("expected nil run, got %#v", run)
	}

	stored, err := store.GetCurrentRun(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.RunID != "run-1" || stored.Status != RunStatusRunning {
		t.Fatalf("expected current run to remain running, got %#v", stored)
	}
}

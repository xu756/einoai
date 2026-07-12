package aisdk

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/xu756/einoai"
)

func TestRunResponseUsesUnifiedSessionFormat(t *testing.T) {
	run := &einoai.RunInfo{SessionID: "s1", RunID: "r1", Status: einoai.RunStatusCompleted}
	history := []*schema.Message{{Role: schema.Assistant, ReasoningContent: "think", Content: "answer"}}
	got, err := NewRunResponse(run, history)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"parts":[{"type":"reasoning"`)) {
		t.Fatalf("unified parts missing: %s", body)
	}
}

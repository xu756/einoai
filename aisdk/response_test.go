package aisdk

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/xu756/einoai"
)

func TestRunResponseContainsRunOnlyPayload(t *testing.T) {
	run := &einoai.RunInfo{SessionID: "s1", RunID: "r1", Status: einoai.RunStatusCompleted}
	got := NewRunResponse(run)
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"run"`)) || bytes.Contains(body, []byte(`"messages"`)) {
		t.Fatalf("unexpected run response: %s", body)
	}
}

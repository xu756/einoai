package einoai_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/xu756/einoai"
	"github.com/xu756/einoai/aisdk"
	"github.com/xu756/einoai/openai"
)

func TestRunResponsesAreEqual(t *testing.T) {
	run := &einoai.RunInfo{SessionID: "s1", RunID: "r1", Status: einoai.RunStatusCompleted}
	history := []*schema.Message{{Role: schema.Assistant, ReasoningContent: "think", Content: "answer"}}
	aiResponse, err := aisdk.NewRunResponse(run, history)
	if err != nil {
		t.Fatal(err)
	}
	openAIResponse, err := openai.NewRunResponse(run, history)
	if err != nil {
		t.Fatal(err)
	}
	aiJSON, err := json.Marshal(aiResponse)
	if err != nil {
		t.Fatal(err)
	}
	openAIJSON, err := json.Marshal(openAIResponse)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(aiJSON, openAIJSON) {
		t.Fatalf("responses differ:\nAI SDK: %s\nOpenAI: %s", aiJSON, openAIJSON)
	}
}

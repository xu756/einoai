package einoai_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/xu756/einoai"
	"github.com/xu756/einoai/aisdk"
	"github.com/xu756/einoai/openai"
)

func TestRunResponsesAreEqual(t *testing.T) {
	run := &einoai.RunInfo{SessionID: "s1", RunID: "r1", Status: einoai.RunStatusCompleted}
	aiResponse := aisdk.NewRunResponse(run)
	openAIResponse := openai.NewRunResponse(run)
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

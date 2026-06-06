package aisdk

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xu756/einoai"

	"github.com/gin-gonic/gin"
)

func TestWriteToolCallWritesStandardToolInputEvents(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	state := newStreamState()

	writeToolCall(c, state, "1-0", einoai.ToolCallData{
		ID:    "call_00_P6ma2c1021vGwXNjT4gp7549",
		Name:  "get_weather",
		Index: 0,
	})
	writeToolCall(c, state, "1-1", einoai.ToolCallData{
		ID:        "call_00_P6ma2c1021vGwXNjT4gp7549",
		Name:      "get_weather",
		Arguments: `{"location":"北京"}`,
		Index:     0,
	})
	writePendingToolsAvailable(c, state, "1-2")

	body := rec.Body.String()
	if strings.Count(body, `"type":"tool-input-available"`) != 1 {
		t.Fatalf("expected one tool-input-available event, got:\n%s", body)
	}
	if !strings.Contains(body, `"toolCallId":"call_00_P6ma2c1021vGwXNjT4gp7549"`) {
		t.Fatalf("expected original tool call id, got:\n%s", body)
	}
	if !strings.Contains(body, `"toolName":"get_weather"`) {
		t.Fatalf("expected original tool name, got:\n%s", body)
	}
	if strings.Contains(body, `"toolCallId":"tool_call_0"`) || strings.Contains(body, `"toolName":"tool"`) {
		t.Fatalf("unexpected fallback tool identity, got:\n%s", body)
	}
	if !strings.Contains(body, `"input":{"location":"北京"}`) {
		t.Fatalf("expected parsed tool input, got:\n%s", body)
	}
}

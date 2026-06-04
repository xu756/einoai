package aisdk

import (
	"net/http"

	"github.com/xu756/einoai"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/gin-gonic/gin"
)

// WriteError writes a protocol-friendly error response.
func WriteError(c *gin.Context, err error) {
	if err == nil {
		return
	}
	if c.Writer.Written() {
		writePart(c, "", map[string]any{"type": "error", "errorText": err.Error()})
		writeDone(c)
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
}

// WriteCreateRunResponse writes a create-run response.
func WriteCreateRunResponse(c *gin.Context, run *einoai.RunInfo) {
	c.JSON(http.StatusAccepted, gin.H{
		"sessionId": run.SessionID,
		"run_id":    run.RunID,
		"status":    run.Status,
	})
}

// WriteRunResponse writes a run metadata response.
func WriteRunResponse(c *gin.Context, run *einoai.RunInfo) {
	c.JSON(http.StatusOK, gin.H{"run": run})
}

// WriteCancelResponse writes the cancel result.
func WriteCancelResponse(c *gin.Context, err error) {
	if err != nil {
		WriteError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"ok": true})
}

// HandleCreateRun is a composable convenience wrapper.
func HandleCreateRun(c *gin.Context, svc einoai.Service, sessionID string, messages []*schema.Message, agent adk.Agent) {
	run, err := svc.CreateRun(c.Request.Context(), einoai.CreateRunRequest{
		SessionID: sessionID,
		Messages:  messages,
		Agent:     agent,
	})
	if err != nil {
		WriteError(c, err)
		return
	}
	WriteCreateRunResponse(c, run)
}

// HandleGetRun is a composable convenience wrapper.
func HandleGetRun(c *gin.Context, svc einoai.Service, sessionID string) {
	run, err := svc.GetRun(c.Request.Context(), sessionID)
	if err != nil {
		WriteError(c, err)
		return
	}
	WriteRunResponse(c, run)
}

// HandleCancelRun is a composable convenience wrapper.
func HandleCancelRun(c *gin.Context, svc einoai.Service, sessionID string) {
	WriteCancelResponse(c, svc.CancelRun(c.Request.Context(), sessionID))
}

// HandleSubscribeEvents is a composable convenience wrapper.
func HandleSubscribeEvents(c *gin.Context, svc einoai.Service, sessionID string, runID string) {
	stream, err := svc.SubscribeEvents(c.Request.Context(), einoai.SubscribeRequest{
		SessionID:    sessionID,
		AfterEventID: GetLastEventID(c),
	})
	if err != nil {
		WriteError(c, err)
		return
	}
	defer stream.Close()
	WriteEventStream(c, stream)
}

// HandleCompletions runs a direct completion using the same run/event pipeline.
func HandleCompletions(c *gin.Context, svc einoai.Service, sessionID string, agent adk.Agent) {
	req, err := BindCompletionsRequest(c)
	if err != nil {
		WriteError(c, err)
		return
	}
	messages, err := ToSchemaMessages(req)
	if err != nil {
		WriteError(c, err)
		return
	}
	run, err := svc.CreateRun(c.Request.Context(), einoai.CreateRunRequest{
		SessionID: sessionID,
		Messages:  messages,
		Agent:     agent,
	})
	if err != nil {
		WriteError(c, err)
		return
	}
	stream, err := svc.SubscribeEvents(c.Request.Context(), einoai.SubscribeRequest{
		SessionID: run.SessionID,
	})
	if err != nil {
		WriteError(c, err)
		return
	}
	defer stream.Close()
	WriteEventStream(c, stream)
}

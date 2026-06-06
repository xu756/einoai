package openai

import (
	"net/http"

	"github.com/xu756/einoai"

	"github.com/cloudwego/eino/adk"
	"github.com/gin-gonic/gin"
)

// WriteError writes an OpenAI-compatible JSON error.
func WriteError(c *gin.Context, err error) {
	if err == nil {
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{
		"error": gin.H{
			"message": err.Error(),
			"type":    "invalid_request_error",
		},
	})
}

// WriteStreamError writes an OpenAI-compatible SSE error and terminates.
func WriteStreamError(c *gin.Context, err error) {
	if err == nil {
		return
	}
	writeErrorData(c, err.Error())
	writeDone(c)
}

// HandleChatCompletions is a composable convenience wrapper.
func HandleChatCompletions(c *gin.Context, svc einoai.Service, sessionID string, agent adk.Agent) {
	req, err := BindChatCompletionsRequest(c)
	if err != nil {
		WriteError(c, err)
		return
	}
	messages, err := ToSchemaMessages(req)
	if err != nil {
		WriteError(c, err)
		return
	}
	if sessionID == "" {
		sessionID = ResolveSessionID(c, req)
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
		RunID:     run.RunID,
	})
	if err != nil {
		WriteError(c, err)
		return
	}
	defer stream.Close()
	if req.Stream {
		WriteChatCompletionStream(c, req, stream)
		return
	}
	body, err := CollectChatCompletion(c.Request.Context(), req, stream)
	if err != nil {
		WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, body)
}

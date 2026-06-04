package main

import (
	"net/http"

	enioai "enio-ai/enioai"
	protoopenai "enio-ai/enioai/openai"

	"github.com/gin-gonic/gin"
)

func (a *app) registerOpenAI(r gin.IRouter) {
	r.POST("/chat/completions", a.openAICompletions)
	r.POST("/sessions/:sessionId", a.createOpenAIRun)
	r.GET("/sessions/:sessionId", a.getOpenAIRun)
	r.POST("/sessions/:sessionId/runs/:runId", a.subscribeOpenAIEvents)
	r.POST("/sessions/:sessionId/cancel", a.cancelOpenAIRun)
}

func (a *app) openAICompletions(c *gin.Context) {
	req, err := protoopenai.BindChatCompletionsRequest(c)
	if err != nil {
		protoopenai.WriteError(c, err)
		return
	}
	messages, err := protoopenai.ToSchemaMessages(req)
	if err != nil {
		protoopenai.WriteError(c, err)
		return
	}
	agent, err := a.resolveAgent(c.Request.Context())
	if err != nil {
		protoopenai.WriteError(c, err)
		return
	}

	run, err := a.svc.CreateRun(c.Request.Context(), enioai.CreateRunRequest{
		SessionID: protoopenai.ResolveSessionID(c, req),
		Messages:  messages,
		Agent:     agent,
		Metadata: map[string]any{
			"protocol": "openai",
			"model":    req.Model,
		},
	})
	if err != nil {
		protoopenai.WriteError(c, err)
		return
	}
	stream, err := a.svc.SubscribeEvents(c.Request.Context(), enioai.SubscribeRequest{
		SessionID: run.SessionID,
	})
	if err != nil {
		protoopenai.WriteError(c, err)
		return
	}
	defer stream.Close()

	if req.Stream {
		protoopenai.WriteChatCompletionStream(c, req, stream)
		return
	}
	body, err := protoopenai.CollectChatCompletion(c.Request.Context(), req, stream)
	if err != nil {
		protoopenai.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, body)
}

func (a *app) createOpenAIRun(c *gin.Context) {
	sessionID := c.Param("sessionId")

	req, err := protoopenai.BindChatCompletionsRequest(c)
	if err != nil {
		protoopenai.WriteError(c, err)
		return
	}
	messages, err := protoopenai.ToSchemaMessages(req)
	if err != nil {
		protoopenai.WriteError(c, err)
		return
	}
	agent, err := a.resolveAgent(c.Request.Context())
	if err != nil {
		protoopenai.WriteError(c, err)
		return
	}

	run, err := a.svc.CreateRun(c.Request.Context(), enioai.CreateRunRequest{
		SessionID: sessionID,
		Messages:  messages,
		Agent:     agent,
		Metadata: map[string]any{
			"protocol": "openai",
			"model":    req.Model,
		},
	})
	if err != nil {
		protoopenai.WriteError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{
		"sessionId": run.SessionID,
		"runId":     run.RunID,
		"status":    run.Status,
	})
}

func (a *app) getOpenAIRun(c *gin.Context) {
	run, err := a.svc.GetRun(c.Request.Context(), c.Param("sessionId"))
	if err != nil {
		protoopenai.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"run": run})
}

func (a *app) subscribeOpenAIEvents(c *gin.Context) {
	sessionID := c.Param("sessionId")

	stream, err := a.svc.SubscribeEvents(c.Request.Context(), enioai.SubscribeRequest{
		SessionID:    sessionID,
		AfterEventID: openAILastEventID(c),
	})
	if err != nil {
		protoopenai.WriteError(c, err)
		return
	}
	defer stream.Close()

	req := protoopenai.ChatCompletionsRequest{
		Model:  c.Query("model"),
		Stream: true,
	}
	protoopenai.WriteChatCompletionStream(c, req, stream)
}

func (a *app) cancelOpenAIRun(c *gin.Context) {
	if err := a.svc.CancelRun(c.Request.Context(), c.Param("sessionId")); err != nil {
		protoopenai.WriteError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"ok": true})
}

func openAILastEventID(c *gin.Context) string {
	if v := c.Query("after"); v != "" {
		return v
	}
	if v := c.Query("lastEventId"); v != "" {
		return v
	}
	if v := c.GetHeader("Last-Event-ID"); v != "" {
		return v
	}
	return ""
}

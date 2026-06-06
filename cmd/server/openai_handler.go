package main

import (
	"net/http"

	"github.com/xu756/einoai"
	"github.com/xu756/einoai/openai"

	"github.com/gin-gonic/gin"
)

func (a *app) registerOpenAI(r gin.IRouter) {
	r.POST("/chat/completions", a.openAICompletions)
	r.POST("/sessions/:sessionId", a.createOpenAIRun)
	r.GET("/sessions/:sessionId", a.getOpenAIRun)
	r.POST("/sessions/:sessionId/runs/:run_id", a.subscribeOpenAIEvents)
	r.POST("/sessions/:sessionId/runs/:run_id/cancel", a.cancelOpenAIRun)
}

func (a *app) openAICompletions(c *gin.Context) {
	req, err := openai.BindChatCompletionsRequest(c)
	if err != nil {
		openai.WriteError(c, err)
		return
	}
	messages, err := openai.ToSchemaMessages(req)
	if err != nil {
		openai.WriteError(c, err)
		return
	}
	agent, err := a.resolveAgent(c.Request.Context())
	if err != nil {
		openai.WriteError(c, err)
		return
	}

	run, err := a.svc.CreateRun(c.Request.Context(), einoai.CreateRunRequest{
		SessionID: openai.ResolveSessionID(c, req),
		Messages:  messages,
		Agent:     agent,
		Metadata: map[string]any{
			"protocol": "openai",
			"model":    req.Model,
		},
	})
	if err != nil {
		openai.WriteError(c, err)
		return
	}
	stream, err := a.svc.SubscribeEvents(c.Request.Context(), einoai.SubscribeRequest{
		SessionID: run.SessionID,
		RunID:     run.RunID,
	})
	if err != nil {
		openai.WriteError(c, err)
		return
	}
	defer stream.Close()

	if req.Stream {
		openai.WriteChatCompletionStream(c, req, stream)
		return
	}
	body, err := openai.CollectChatCompletion(c.Request.Context(), req, stream)
	if err != nil {
		openai.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, body)
}

func (a *app) createOpenAIRun(c *gin.Context) {
	sessionID := c.Param("sessionId")

	req, err := openai.BindChatCompletionsRequest(c)
	if err != nil {
		openai.WriteError(c, err)
		return
	}
	messages, err := openai.ToSchemaMessages(req)
	if err != nil {
		openai.WriteError(c, err)
		return
	}
	agent, err := a.resolveAgent(c.Request.Context())
	if err != nil {
		openai.WriteError(c, err)
		return
	}

	run, err := a.svc.CreateRun(c.Request.Context(), einoai.CreateRunRequest{
		SessionID: sessionID,
		Messages:  messages,
		Agent:     agent,
		Metadata: map[string]any{
			"protocol": "openai",
			"model":    req.Model,
		},
	})
	if err != nil {
		openai.WriteError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{
		"sessionId": run.SessionID,
		"run_id":    run.RunID,
		"status":    run.Status,
	})
}

func (a *app) getOpenAIRun(c *gin.Context) {
	run, err := a.svc.GetRun(c.Request.Context(), c.Param("sessionId"))
	if err != nil {
		openai.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"run": run})
}

func (a *app) subscribeOpenAIEvents(c *gin.Context) {
	sessionID := c.Param("sessionId")

	stream, err := a.svc.SubscribeEvents(c.Request.Context(), einoai.SubscribeRequest{
		SessionID: sessionID,
		RunID:     c.Param("run_id"),
	})
	if err != nil {
		openai.WriteError(c, err)
		return
	}
	defer stream.Close()

	req := openai.ChatCompletionsRequest{
		Model:  c.Query("model"),
		Stream: true,
	}
	openai.WriteChatCompletionStream(c, req, stream)
}

func (a *app) cancelOpenAIRun(c *gin.Context) {
	if err := a.svc.CancelRun(c.Request.Context(), c.Param("sessionId"), c.Param("run_id")); err != nil {
		openai.WriteError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"ok": true})
}

package main

import (
	"net/http"
	"strconv"

	"github.com/xu756/einoai"
	"github.com/xu756/einoai/openai"

	"github.com/gin-gonic/gin"
)

func (a *app) registerOpenAI(r gin.IRouter) {
	r.POST("/chat/completions", a.openAICompletions)
	r.POST("/sessions/:sessionId", a.createOpenAIRun)
	r.GET("/sessions/:sessionId", a.getOpenAIRun)
	r.DELETE("/sessions/:sessionId", a.deleteOpenAISession)
	r.POST("/sessions/:sessionId/runs/:run_id", a.subscribeOpenAIEvents)
	r.POST("/sessions/:sessionId/runs/:run_id/cancel", a.cancelOpenAIRun)
}

func (a *app) openAICompletions(c *gin.Context) {
	req, err := openai.DecodeChatCompletionsRequest(c.Request.Body)
	if err != nil {
		writeOpenAIError(c, err)
		return
	}
	messages, err := openai.ToSchemaMessages(req)
	if err != nil {
		writeOpenAIError(c, err)
		return
	}
	agent, err := a.resolveAgent(c.Request.Context())
	if err != nil {
		writeOpenAIError(c, err)
		return
	}

	run, err := a.svc.CreateRun(c.Request.Context(), newAgentRunRequest(
		openai.ResolveSessionID(req, c.GetHeader("X-Session-ID"), c.Query("sessionId")),
		messages,
		agent,
		a.onRunCompleted,
	))
	if err != nil {
		writeOpenAIError(c, err)
		return
	}
	stream, err := a.svc.SubscribeEvents(c.Request.Context(), einoai.SubscribeRequest{
		SessionID: run.SessionID,
		RunID:     run.RunID,
	})
	if err != nil {
		writeOpenAIError(c, err)
		return
	}
	defer func() {
		_ = stream.Close()
	}()

	if req.Stream {
		openai.SetChatCompletionStreamHeaders(c.Writer.Header())
		_ = openai.WriteChatCompletionStreamTo(c.Request.Context(), c.Writer, c.Writer.Flush, req, stream)
		return
	}
	body, err := openai.CollectChatCompletion(c.Request.Context(), req, stream)
	if err != nil {
		writeOpenAIError(c, err)
		return
	}
	c.JSON(http.StatusOK, body)
}

func (a *app) createOpenAIRun(c *gin.Context) {
	sessionID := c.Param("sessionId")

	req, err := openai.DecodeChatCompletionsRequest(c.Request.Body)
	if err != nil {
		writeOpenAIError(c, err)
		return
	}
	messages, err := openai.ToSchemaMessages(req)
	if err != nil {
		writeOpenAIError(c, err)
		return
	}
	agent, err := a.resolveAgent(c.Request.Context())
	if err != nil {
		writeOpenAIError(c, err)
		return
	}

	run, err := a.svc.CreateRun(c.Request.Context(), newAgentRunRequest(
		sessionID,
		messages,
		agent,
		a.onRunCompleted,
	))
	if err != nil {
		writeOpenAIError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, openai.NewCreateRunResponse(run))
}

func (a *app) getOpenAIRun(c *gin.Context) {
	sessionID := c.Param("sessionId")
	run, err := a.svc.GetRun(c.Request.Context(), sessionID)
	if err != nil {
		writeOpenAIError(c, err)
		return
	}
	c.JSON(http.StatusOK, openai.NewRunResponse(run))
}

func (a *app) subscribeOpenAIEvents(c *gin.Context) {
	sessionID := c.Param("sessionId")

	stream, err := a.svc.SubscribeEvents(c.Request.Context(), einoai.SubscribeRequest{
		SessionID: sessionID,
		RunID:     c.Param("run_id"),
	})
	if err != nil {
		writeOpenAIError(c, err)
		return
	}
	defer func() {
		_ = stream.Close()
	}()

	req := openAISubscribeRequest(c)
	openai.SetChatCompletionStreamHeaders(c.Writer.Header())
	_ = openai.WriteChatCompletionStreamTo(c.Request.Context(), c.Writer, c.Writer.Flush, req, stream)
}

func openAISubscribeRequest(c *gin.Context) openai.ChatCompletionsRequest {
	includeUsage, _ := strconv.ParseBool(c.Query("include_usage"))
	request := openai.ChatCompletionsRequest{
		Model:  c.Query("model"),
		Stream: true,
	}
	if includeUsage {
		request.StreamOptions = &openai.StreamOptions{IncludeUsage: true}
	}
	return request
}

func (a *app) deleteOpenAISession(c *gin.Context) {
	if err := a.svc.DeleteSession(c.Request.Context(), c.Param("sessionId")); err != nil {
		writeOpenAIError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, openai.NewDeleteSessionResponse())
}

func (a *app) cancelOpenAIRun(c *gin.Context) {
	if err := a.svc.CancelRun(c.Request.Context(), c.Param("sessionId"), c.Param("run_id")); err != nil {
		writeOpenAIError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, openai.NewCancelResponse())
}

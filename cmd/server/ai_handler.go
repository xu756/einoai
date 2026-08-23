package main

import (
	"net/http"

	"github.com/xu756/einoai"
	"github.com/xu756/einoai/aisdk"

	"github.com/gin-gonic/gin"
)

func (a *app) registerAISDK(r gin.IRouter) {
	r.POST("/completions", a.aiCompletions)
	r.POST("/sessions/:sessionId", a.createAIRun)
	r.GET("/sessions/:sessionId", a.getAIRun)
	r.DELETE("/sessions/:sessionId", a.deleteAISession)
	r.GET("/sessions/:sessionId/runs/:run_id", a.getAIRunByID)
	r.POST("/sessions/:sessionId/runs/:run_id", a.subscribeAIEvents)
	r.POST("/sessions/:sessionId/runs/:run_id/cancel", a.cancelAIRun)
}

func (a *app) aiCompletions(c *gin.Context) {
	req, err := aisdk.DecodeCompletionsRequest(c.Request.Body)
	if err != nil {
		writeAIError(c, err)
		return
	}
	messages, err := aisdk.ToSchemaMessages(req)
	if err != nil {
		writeAIError(c, err)
		return
	}
	agent, err := a.resolveAgent(c.Request.Context())
	if err != nil {
		writeAIError(c, err)
		return
	}
	run, err := a.svc.CreateRun(c.Request.Context(), newAgentRunRequest(
		temporarySessionID("usechat"),
		messages,
		agent,
		a.onRunCompleted,
	))
	if err != nil {
		writeAIError(c, err)
		return
	}
	stream, err := a.svc.SubscribeEvents(c.Request.Context(), einoai.SubscribeRequest{
		SessionID: run.SessionID,
		RunID:     run.RunID,
	})
	if err != nil {
		writeAIError(c, err)
		return
	}
	defer func() {
		_ = stream.Close()
	}()
	aisdk.SetEventStreamHeaders(c.Writer.Header())
	_, _ = aisdk.WriteEventStreamTo(c.Request.Context(), c.Writer, c.Writer.Flush, stream)
}

func (a *app) createAIRun(c *gin.Context) {
	sessionID := c.Param("sessionId")

	req, err := aisdk.DecodeCreateRunRequest(c.Request.Body)
	if err != nil {
		writeAIError(c, err)
		return
	}
	messages, err := aisdk.ToSchemaMessages(req)
	if err != nil {
		writeAIError(c, err)
		return
	}
	agent, err := a.resolveAgent(c.Request.Context())
	if err != nil {
		writeAIError(c, err)
		return
	}

	run, err := a.svc.CreateRun(c.Request.Context(), newAgentRunRequest(
		sessionID,
		messages,
		agent,
		a.onRunCompleted,
	))
	if err != nil {
		writeAIError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, aisdk.NewCreateRunResponse(run))
}

func (a *app) getAIRun(c *gin.Context) {
	sessionID := c.Param("sessionId")

	run, err := a.svc.GetRun(c.Request.Context(), sessionID)
	if err != nil {
		writeAIError(c, err)
		return
	}
	c.JSON(http.StatusOK, aisdk.NewRunResponse(run))
}

func (a *app) getAIRunByID(c *gin.Context) {
	run, err := a.svc.GetRunByID(c.Request.Context(), c.Param("sessionId"), c.Param("run_id"))
	if err != nil {
		writeAIError(c, err)
		return
	}
	c.JSON(http.StatusOK, aisdk.NewRunResponse(run))
}

func (a *app) subscribeAIEvents(c *gin.Context) {
	sessionID := c.Param("sessionId")

	stream, err := a.svc.SubscribeEvents(c.Request.Context(), einoai.SubscribeRequest{
		SessionID: sessionID,
		RunID:     c.Param("run_id"),
	})
	if err != nil {
		writeAIError(c, err)
		return
	}
	defer func() {
		_ = stream.Close()
	}()
	aisdk.SetEventStreamHeaders(c.Writer.Header())
	_, _ = aisdk.WriteEventStreamTo(c.Request.Context(), c.Writer, c.Writer.Flush, stream)
}

func (a *app) deleteAISession(c *gin.Context) {
	if err := a.svc.DeleteSession(c.Request.Context(), c.Param("sessionId")); err != nil {
		writeAIError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, aisdk.NewDeleteSessionResponse())
}

func (a *app) cancelAIRun(c *gin.Context) {
	if err := a.svc.CancelRun(c.Request.Context(), c.Param("sessionId"), c.Param("run_id")); err != nil {
		writeAIError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, aisdk.NewCancelResponse())
}

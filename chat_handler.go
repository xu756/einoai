package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

type Handler struct {
	AgentManager *AgentManager
}

func NewHandler(ctx context.Context, rdb *redis.Client) *Handler {
	agentManager, err := NewAgentManager(ctx, rdb)
	if err != nil {
		panic(err)
	}
	return &Handler{
		AgentManager: agentManager,
	}
}

type CreateRunRequest struct {
	Message string    `json:"message" binding:"required"`
	Agent   AgentKind `json:"agent,omitempty"` // "chat" or "deep"
}

func (h *Handler) ChatRouter(r *gin.RouterGroup) {
	chatGroup := r.Group("/chat")
	chatGroup.POST("/sessions/:sessionId", h.CreateRun)
	chatGroup.GET("/sessions/:sessionId", h.GetSessionRun)
	chatGroup.POST("/sessions/:sessionId/runs/:runId", h.RunEvents)
	chatGroup.POST("/sessions/:sessionId/cancel/:runId", h.CancelSessionRun)
}

func (h *Handler) CreateRun(c *gin.Context) {
	if h.AgentManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent manager is not initialized"})
		return
	}

	sessionID := c.Param("sessionId")

	var req CreateRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Agent == "" {
		req.Agent = AgentKindChat
	}

	runID, err := h.AgentManager.StartRun(c.Request.Context(), sessionID, req.Message, req.Agent)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"sessionId": sessionID,
		"runId":     runID,
		"status":    RunStatusRunning,
	})
}

func (h *Handler) GetSessionRun(c *gin.Context) {
	if h.AgentManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent manager is not initialized"})
		return
	}

	sessionID := c.Param("sessionId")

	run, err := h.AgentManager.runStore.GetCurrentRun(c.Request.Context(), sessionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"sessionId": sessionID,
		"run":       run,
	})
}

func (h *Handler) CancelSessionRun(c *gin.Context) {
	if h.AgentManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent manager is not initialized"})
		return
	}

	sessionID := c.Param("sessionId")
	runID := c.Param("runId")

	run, ok, err := h.AgentManager.CancelSessionRun(c.Request.Context(), sessionID, runID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !ok {
		c.JSON(http.StatusOK, gin.H{
			"sessionId": sessionID,
			"run":       nil,
		})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"sessionId": sessionID,
		"run":       run,
	})
}

func (h *Handler) RunEvents(c *gin.Context) {
	if h.AgentManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent manager is not initialized"})
		return
	}

	sessionID := c.Param("sessionId")
	runID := c.Param("runId")

	c.Writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	c.Writer.Header().Set("Cache-Control", "no-cache, no-transform")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")

	lastID := c.Query("after")
	if lastID == "" {
		lastID = c.Query("lastEventId")
	}
	if lastID == "" {
		lastID = c.GetHeader("Last-Event-ID")
	}
	if lastID == "" {
		lastID = "0-0"
	}

	ctx := c.Request.Context()

	run, err := h.AgentManager.runStore.GetCurrentRun(ctx, sessionID)
	if err != nil {
		errObj := map[string]any{
			"error": map[string]any{
				"message": err.Error(),
				"type":    "sse_read_error",
			},
		}
		b, _ := json.Marshal(errObj)
		writeSSEDataWithID(c, "", string(b))
		writeSSEDataWithID(c, "", "[DONE]")
		return
	}
	if run == nil {
		writeSSEDataWithID(c, "", "[DONE]")
		return
	}
	if run.RunID != runID || isTerminalRunStatus(run.Status) {
		writeSSEDataWithID(c, "", "[DONE]")
		return
	}

	for {
		events, err := h.AgentManager.runStore.ReadAfter(
			ctx,
			sessionID,
			run.RunID,
			lastID,
			15*time.Second,
			100,
		)

		if ctx.Err() != nil {
			return
		}

		if err != nil {
			errObj := map[string]any{
				"error": map[string]any{
					"message": err.Error(),
					"type":    "sse_read_error",
				},
			}
			b, _ := json.Marshal(errObj)
			writeSSEDataWithID(c, "", string(b))
			return
		}

		if len(events) == 0 {
			// heartbeat，防止 nginx / browser 长时间无数据断开
			_, _ = fmt.Fprint(c.Writer, ": ping\n\n")
			c.Writer.Flush()
			continue
		}

		for _, ev := range events {
			writeSSEDataWithID(c, ev.ID, ev.Data)
			lastID = ev.ID

			if ev.Data == "[DONE]" {
				return
			}
		}
	}
}

func writeSSEDataWithID(c *gin.Context, id string, data string) {
	if id != "" {
		_, _ = fmt.Fprintf(c.Writer, "id: %s\n", id)
	}

	// 保持 OpenAI-compatible：核心仍然是 data: {...} / data: [DONE]
	_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", data)
	c.Writer.Flush()
}

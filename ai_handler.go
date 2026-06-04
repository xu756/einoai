package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/gin-gonic/gin"
)

// AI SDK useChat 风格的 4 个路由，与 OpenAI 协议路由一一对应：
//
//	POST /usechat/sessions/:sessionId      → 创建 run（写入 Redis current_run，后台跑 agent）
//	GET  /usechat/sessions/:sessionId      → 查询当前 session 是否在跑 run
//	POST /usechat/sessions/:sessionId/runs/:runId  → SSE 订阅该 run 的 AISDK Data Stream Protocol 事件
//	POST /usechat/sessions/:sessionId/cancel/:runId → 取消指定 run
//	POST /usechat/completions            → 独立接口，无需 session

func (h *Handler) AIChatRouter(r *gin.RouterGroup) {
	aiGroup := r.Group("/usechat")
	aiGroup.POST("/sessions/:sessionId", h.CreateAIRun)
	aiGroup.GET("/sessions/:sessionId", h.GetAISessionRun)
	aiGroup.POST("/sessions/:sessionId/runs/:runId", h.RunAIEvents)
	aiGroup.POST("/sessions/:sessionId/cancel/:runId", h.CancelAISessionRun)
	aiGroup.POST("/completions", h.UseChatCompletions)
}

// UseChatCompletions 独立 AI SDK 流式接口，无需 session。
func (h *Handler) UseChatCompletions(c *gin.Context) {
	if h.AgentManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent manager is not initialized"})
		return
	}

	req, ok := bindUseChatRequest(c)
	if !ok {
		return
	}

	agentKind := AgentKindChat
	if p, ok := req.Params["type"].(string); ok && p == "deep" {
		agentKind = AgentKindDeep
	}

	messages := extractUseChatMessages(req)

	ctx := c.Request.Context()

	var ag adk.Agent
	var err error

	switch agentKind {
	case AgentKindDeep:
		ag, err = h.AgentManager.NewDeepAgent(ctx)
	default:
		ag, err = h.AgentManager.NewChatModelAgent(ctx)
	}

	if err != nil {
		h.writeAIDoneWithError(c, err)
		return
	}

	runner := h.AgentManager.NewRunner(ctx, ag)
	iter := runner.Run(ctx, messages)

	streamAISDKDataProtocol(c, iter)
}

// CreateAIRun 创建 AI run，复用已有的 UseChatRequest 解析和 agent 创建逻辑。
func (h *Handler) CreateAIRun(c *gin.Context) {
	if h.AgentManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent manager is not initialized"})
		return
	}

	sessionID := c.Param("sessionId")

	req, ok := bindUseChatRequest(c)
	if !ok {
		return
	}

	agentKind := AgentKindChat
	if p, ok := req.Params["type"].(string); ok && p == "deep" {
		agentKind = AgentKindDeep
	}

	messages := extractUseChatMessages(req)

	runID, err := h.AgentManager.StartAIRun(c.Request.Context(), sessionID, messages, agentKind)
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

// GetAISessionRun 查询当前 session 是否有一个 active AI run。
func (h *Handler) GetAISessionRun(c *gin.Context) {
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

// RunAIEvents SSE 订阅指定 run 的 AISDK Data Stream Protocol 事件流。
// 从 Redis Stream 读取事件，支持断点续读。
func (h *Handler) RunAIEvents(c *gin.Context) {
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
	c.Writer.Header().Set("x-vercel-ai-ui-message-stream", "v1")

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

	run, err := h.AgentManager.runStore.GetRunForEvents(ctx, sessionID, runID)
	if err != nil {
		h.writeAIDoneWithError(c, err)
		return
	}
	if run == nil {
		h.writeAIDone(c)
		return
	}

	// 从 Redis Stream 持续读取 AISDK 事件
	h.streamFromRedis(c, ctx, sessionID, runID, lastID)
}

func (h *Handler) streamFromRedis(c *gin.Context, ctx context.Context, sessionID, runID, lastID string) {
	state := newUseChatStreamState(runID)
	if lastID == "0-0" || lastID == "" {
		writePart(c, nil, map[string]any{"type": "start", "messageId": state.messageID})
		writePart(c, nil, map[string]any{"type": "start-step"})
		state.stepStarted = true
	} else {
		state.stepStarted = true
		if err := h.replayAISDKState(ctx, sessionID, runID, lastID, state); err != nil {
			h.writeAIDoneWithError(c, err)
			return
		}
	}

	var lastUsage *schema.TokenUsage
	var lastFinishReason string

	for {
		events, err := h.AgentManager.runStore.ReadAIStream(ctx, sessionID, runID, lastID, 15*time.Second)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			h.writeAIDoneWithError(c, err)
			return
		}
		if len(events) == 0 {
			run, runErr := h.AgentManager.runStore.GetRun(ctx, sessionID, runID)
			if runErr != nil {
				h.writeAIDoneWithError(c, runErr)
				return
			}
			if run == nil || isTerminalRunStatus(run.Status) {
				h.writeAIDone(c)
				return
			}
			_, _ = fmt.Fprint(c.Writer, ": ping\n\n")
			c.Writer.Flush()
			continue
		}

		var lastEvID string
		var done bool
		var errText string

		for _, ev := range events {
			var se StreamEvent
			_ = json.Unmarshal([]byte(ev.Data), &se)
			if se.Type == StreamEventMessage && se.Message != nil {
				if se.Message.ResponseMeta != nil {
					if se.Message.ResponseMeta.Usage != nil {
						lastUsage = se.Message.ResponseMeta.Usage
					}
					if se.Message.ResponseMeta.FinishReason != "" {
						lastFinishReason = se.Message.ResponseMeta.FinishReason
					}
				}
				writeEinoMsgAsAISDKPartsWithID(c, nil, state, ev.ID, se.Message)
			} else if se.Type == StreamEventError {
				errText = se.Error
				done = true
			} else if se.Type == StreamEventDone {
				done = true
			}
			lastEvID = ev.ID
		}

		if errText != "" {
			writePartWithID(c, nil, lastEvID, map[string]any{"type": "error", "errorText": errText})
		}
		if done {
			finishOpenBlocksWithID(c, nil, lastEvID, state)
			writePartWithID(c, nil, lastEvID, map[string]any{"type": "finish-step"})
			writePartWithID(c, nil, lastEvID, createAISDKFinishEvent(lastFinishReason, lastUsage))
			h.writeAIDone(c)
			return
		}
		if lastEvID != "" {
			lastID = lastEvID
		}
	}
}

func (h *Handler) replayAISDKState(ctx context.Context, sessionID, runID, lastID string, state *useChatStreamState) error {
	events, err := h.AgentManager.runStore.ReadRange(ctx, sessionID, runID, "0-0", lastID)
	if err != nil {
		return err
	}
	for _, ev := range events {
		var se StreamEvent
		if err := json.Unmarshal([]byte(ev.Data), &se); err != nil {
			continue
		}
		if se.Type == StreamEventMessage && se.Message != nil {
			writeEinoMsgAsAISDKParts(nil, nil, state, se.Message)
		}
	}
	return nil
}

// CancelAISessionRun 取消指定 run，只对当前 session 的 current run 生效。
func (h *Handler) CancelAISessionRun(c *gin.Context) {
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

func (h *Handler) writeAIDone(c *gin.Context) {
	_, _ = c.Writer.Write([]byte("data: [DONE]\n\n"))
	c.Writer.Flush()
}

func (h *Handler) writeAIDoneWithError(c *gin.Context, err error) {
	errObj := map[string]any{
		"error": map[string]any{
			"message": err.Error(),
			"type":    "server_error",
		},
	}
	b, _ := json.Marshal(errObj)
	_, _ = c.Writer.Write([]byte("data: " + string(b) + "\n\n"))
	_, _ = c.Writer.Write([]byte("data: [DONE]\n\n"))
	c.Writer.Flush()
}

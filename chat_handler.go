package main

import (
	"context"
	"net/http"

	"github.com/cloudwego/eino/adk"
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

type ChatRequest struct {
	Message string `json:"message" binding:"required"`
}

func (h *Handler) ChatRouter(r *gin.RouterGroup) {
	chatGroup := r.Group("/chat")
	chatGroup.POST("/stream", h.ChatStream)
	chatGroup.POST("/deep/stream", h.DeepChatStream)
	// AI SDK useChat / DefaultChatTransport 专用
	chatGroup.POST("/usechat", h.ChatUseChatStream)
	chatGroup.POST("/deep/usechat", h.DeepChatUseChatStream)
}

func (h *Handler) ChatStream(c *gin.Context) {
	if h.AgentManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent manager is not initialized"})
		return
	}

	// var req ChatRequest
	// if err := c.ShouldBindJSON(&req); err != nil {
	// 	c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	// 	return
	// }

	ctx := c.Request.Context()
	ag, err := h.AgentManager.NewChatModelAgent(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	runner := h.AgentManager.NewRunner(ctx, ag)
	iter := runner.Query(ctx, "这个是开发agent测试消息 你可以做什么 有那些功能 有那些智能体 工具 智能体里面有那些工具 帮我调用测试一下", adk.AgentRunOption{})
	streamOpenAICompatible(c, iter, "GPT-4")
}

func (h *Handler) DeepChatStream(c *gin.Context) {
	if h.AgentManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent manager is not initialized"})
		return
	}

	var req ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	ag, err := h.AgentManager.NewDeepAgent(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	runner := h.AgentManager.NewRunner(ctx, ag)
	iter := runner.Query(ctx, req.Message)
	streamOpenAICompatible(c, iter, "GPT-4")
}

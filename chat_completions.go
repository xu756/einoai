package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/gin-gonic/gin"
)

type ChatCompletionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}

type ChatCompletionRequest struct {
	Model       string                  `json:"model"`
	Messages    []ChatCompletionMessage `json:"messages"`
	Stream      bool                    `json:"stream"`
	Temperature float64                 `json:"temperature,omitempty"`
	MaxTokens   int                     `json:"max_tokens,omitempty"`
}

func (h *Handler) ChatCompletions(c *gin.Context) {
	var req ChatCompletionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if len(req.Messages) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "messages is required and cannot be empty"})
		return
	}

	userMessage := req.Messages[len(req.Messages)-1].Content
	modelName := req.Model
	if modelName == "" {
		modelName = os.Getenv("MODEL_NAME")
		if modelName == "" {
			modelName = "gpt-4"
		}
	}

	if req.Stream {
		h.streamChatCompletion(c, userMessage, modelName)
		return
	}
	h.nonStreamChatCompletion(c, userMessage, modelName)
}

func (h *Handler) nonStreamChatCompletion(c *gin.Context, message, modelName string) {
	ctx := c.Request.Context()
	agent, err := h.AgentManager.NewChatModelAgent(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	runner := h.AgentManager.NewRunner(ctx, agent)
	iter := runner.Query(ctx, message)

	id := "chatcmpl-" + fmt.Sprintf("%d", time.Now().UnixNano())
	created := time.Now().Unix()

	var fullContent string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event == nil || event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		mv := event.Output.MessageOutput
		if mv.Message != nil {
			fullContent = mv.Message.Content
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   modelName,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": fullContent,
				},
				"finish_reason": "stop",
			},
		},
	})
}

func (h *Handler) streamChatCompletion(c *gin.Context, message, modelName string) {
	c.Writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	c.Writer.Header().Set("Cache-Control", "no-cache, no-transform")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")

	ctx := c.Request.Context()
	agent, err := h.AgentManager.NewChatModelAgent(ctx)
	if err != nil {
		h.writeCompletionsSSEError(c, err)
		h.writeCompletionsSSEDone(c)
		return
	}
	runner := h.AgentManager.NewRunner(ctx, agent)
	iter := runner.Query(ctx, message)

	h.streamCompletionsToClient(ctx, c, iter, modelName)
}

func (h *Handler) streamCompletionsToClient(
	ctx context.Context,
	c *gin.Context,
	iter *adk.AsyncIterator[*adk.AgentEvent],
	modelName string,
) {
	id := "chatcmpl-" + fmt.Sprintf("%d", time.Now().UnixNano())
	created := time.Now().Unix()

	for {
		if ctx.Err() != nil {
			return
		}

		event, ok := iter.Next()
		if !ok {
			h.writeCompletionsSSEChunk(c, id, created, modelName, "", "", nil)
			h.writeCompletionsSSEDone(c)
			return
		}
		if event == nil {
			continue
		}
		if event.Err != nil {
			h.writeCompletionsSSEError(c, event.Err)
			h.writeCompletionsSSEDone(c)
			return
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}

		mv := event.Output.MessageOutput

		if mv.IsStreaming && mv.MessageStream != nil {
			for {
				if ctx.Err() != nil {
					return
				}
				msg, err := mv.MessageStream.Recv()
				if err != nil {
					if err == io.EOF {
						break
					}
					h.writeCompletionsSSEError(c, err)
					h.writeCompletionsSSEDone(c)
					return
				}
				if msg == nil {
					continue
				}
				h.writeCompletionsSSEChunk(c, id, created, modelName, msg.Content, msg.ReasoningContent, nil)
			}
			continue
		}

		if mv.Message != nil {
			h.writeCompletionsSSEChunk(c, id, created, modelName, mv.Message.Content, mv.Message.ReasoningContent, nil)
		}
	}
}

func (h *Handler) writeCompletionsSSEChunk(c *gin.Context, id string, created int64, model, content, reasoning string, finishReason any) {
	delta := map[string]any{}
	if content != "" {
		delta["content"] = content
	}
	if reasoning != "" {
		delta["reasoning_content"] = reasoning
	}

	chunk := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]any{
			{
				"index":         0,
				"delta":         delta,
				"finish_reason": finishReason,
			},
		},
	}
	b, _ := json.Marshal(chunk)
	_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", string(b))
	c.Writer.Flush()
}

func (h *Handler) writeCompletionsSSEDone(c *gin.Context) {
	_, _ = fmt.Fprint(c.Writer, "data: [DONE]\n\n")
	c.Writer.Flush()
}

func (h *Handler) writeCompletionsSSEError(c *gin.Context, err error) {
	errObj := map[string]any{
		"error": map[string]any{
			"message": err.Error(),
			"type":    "server_error",
		},
	}
	b, _ := json.Marshal(errObj)
	_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", string(b))
	c.Writer.Flush()
}

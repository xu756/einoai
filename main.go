package main

import (
	"context"
	"net/http"
	"os"

	ccb "github.com/cloudwego/eino-ext/callbacks/cozeloop"
	"github.com/cloudwego/eino/callbacks"
	"github.com/coze-dev/cozeloop-go"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
)

func main() {
	godotenv.Load()

	ctx := context.Background()
	cozeClient, err := cozeloop.NewClient()
	if err != nil {
		panic(err)
	}
	defer cozeClient.Close(ctx)

	handler := ccb.NewLoopHandler(cozeClient)
	callbacks.AppendGlobalHandlers(handler)
	// Create a Gin router with default middleware (logger and recovery)
	engine := gin.Default()

	// Define a simple GET endpoint
	engine.GET("/ping", func(c *gin.Context) {
		// Return JSON response
		c.JSON(http.StatusOK, gin.H{
			"message": "pong",
		})
	})
	api := engine.Group("/api")

	client := redis.NewClient(&redis.Options{
		Addr:     os.Getenv("REDIS_ADDR"),
		Password: os.Getenv("REDIS_PASSWORD"),
		DB:       1,
		MaintNotificationsConfig: &maintnotifications.Config{
			Mode: maintnotifications.ModeDisabled,
		},
	})
	h := NewHandler(context.Background(), client)
	// chat
	h.ChatRouter(api)
	h.AIChatRouter(api)

	// Start server on port 8080 (default)
	// Server will listen on 0.0.0.0:8080 (localhost:8080 on Windows)
	engine.Run()
}

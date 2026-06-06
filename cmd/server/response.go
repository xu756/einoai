package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func writeAIError(c *gin.Context, err error) {
	if err == nil {
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
}

func writeOpenAIError(c *gin.Context, err error) {
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

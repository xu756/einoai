package main

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xu756/einoai"
)

func writeAIError(c *gin.Context, err error) {
	if err == nil {
		return
	}
	c.JSON(serviceErrorStatus(err), gin.H{"error": err.Error()})
}

func writeOpenAIError(c *gin.Context, err error) {
	if err == nil {
		return
	}
	c.JSON(serviceErrorStatus(err), gin.H{
		"error": gin.H{
			"message": err.Error(),
			"type":    "invalid_request_error",
		},
	})
}

func serviceErrorStatus(err error) int {
	switch {
	case errors.Is(err, einoai.ErrRunNotFound):
		return http.StatusNotFound
	case errors.Is(err, einoai.ErrRunActive):
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}

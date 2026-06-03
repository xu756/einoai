package main

import "github.com/gin-gonic/gin"

func (h *Handler) UseChatRouter(r *gin.RouterGroup) {
	chatGroup := r.Group("/ai")
	chatGroup.POST("/usechat", h.ChatUseChatStream)

}

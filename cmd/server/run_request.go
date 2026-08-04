package main

import (
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/xu756/einoai"
)

func newAgentRunRequest(sessionID string, messages []*schema.Message, agent adk.Agent, onCompleted einoai.OnRunCompleted) einoai.CreateRunRequest {
	return einoai.CreateRunRequest{
		SessionID:   sessionID,
		Messages:    messages,
		Agent:       agent,
		OnCompleted: onCompleted,
	}
}

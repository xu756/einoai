package main

import (
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/xu756/einoai"
)

func newAgentRunRequest(sessionID string, messages []*schema.Message, agent adk.Agent) einoai.CreateRunRequest {
	return einoai.CreateRunRequest{
		SessionID: sessionID,
		Messages:  messages,
		Agent:     agent,
	}
}

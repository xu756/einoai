package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestOpenAISubscribeRequestIncludesUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/?model=gpt-4o&include_usage=true", nil)
	request := openAISubscribeRequest(context)
	if request.Model != "gpt-4o" {
		t.Fatalf("model was not propagated: %#v", request)
	}
	if request.StreamOptions == nil || !request.StreamOptions.IncludeUsage {
		t.Fatalf("include_usage was not propagated: %#v", request)
	}
}

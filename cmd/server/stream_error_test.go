package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/xu756/einoai"
)

type streamErrorService struct {
	subscribeErr error
}

func (*streamErrorService) CreateRun(context.Context, einoai.CreateRunRequest) (*einoai.RunInfo, error) {
	return nil, errors.New("unexpected CreateRun")
}
func (*streamErrorService) GetRun(context.Context, string) (*einoai.RunInfo, error) { return nil, nil }
func (*streamErrorService) GetRunByID(context.Context, string, string) (*einoai.RunInfo, error) {
	return nil, nil
}
func (*streamErrorService) DeleteSession(context.Context, string) error     { return nil }
func (*streamErrorService) CancelRun(context.Context, string, string) error { return nil }
func (s *streamErrorService) SubscribeEvents(context.Context, einoai.SubscribeRequest) (einoai.EventStream, error) {
	return nil, s.subscribeErr
}

func TestStreamingEndpointsReturnProtocolErrorsAsSSE(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := &app{svc: &streamErrorService{subscribeErr: errors.New("subscribe failed")}}
	engine := gin.New()
	a.registerAISDK(engine.Group("/api/usechat"))
	a.registerOpenAI(engine.Group("/api/v1"))

	tests := []struct {
		name        string
		path        string
		body        string
		errorText   string
		marker      string
		extraHeader string
	}{
		{name: "OpenAI completion decode", path: "/api/v1/chat/completions", body: "{", errorText: "unexpected EOF", marker: `"error"`},
		{name: "AI SDK completion decode", path: "/api/usechat/completions", body: "{", errorText: "unexpected EOF", marker: `"type":"error"`, extraHeader: "v1"},
		{name: "OpenAI subscription", path: "/api/v1/sessions/s1/runs/r1", errorText: "subscribe failed", marker: `"error"`},
		{name: "AI SDK subscription", path: "/api/usechat/sessions/s1/runs/r1", errorText: "subscribe failed", marker: `"type":"error"`, extraHeader: "v1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			engine.ServeHTTP(recorder, request)
			if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
				t.Fatalf("expected SSE content type, got %q: %s", contentType, recorder.Body.String())
			}
			body := recorder.Body.String()
			if !strings.Contains(body, "data: ") || !strings.Contains(body, test.marker) || !strings.Contains(body, test.errorText) || !strings.HasSuffix(body, "data: [DONE]\n\n") {
				t.Fatalf("unexpected SSE error body: %s", body)
			}
			if test.extraHeader != "" && recorder.Header().Get("x-vercel-ai-ui-message-stream") != test.extraHeader {
				t.Fatalf("missing AI SDK stream header: %#v", recorder.Header())
			}
		})
	}
}

func TestCreateRunDecodeErrorsRemainHTTPJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := &app{}
	engine := gin.New()
	a.registerAISDK(engine.Group("/api/usechat"))
	a.registerOpenAI(engine.Group("/api/v1"))

	for _, path := range []string{"/api/usechat/sessions/s1", "/api/v1/sessions/s1"} {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader("{")))
		if recorder.Code != http.StatusBadRequest || !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("non-stream endpoint %s must return HTTP JSON, got %d %q: %s", path, recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), "data: ") {
			t.Fatalf("non-stream endpoint returned SSE: %s", recorder.Body.String())
		}
	}
}

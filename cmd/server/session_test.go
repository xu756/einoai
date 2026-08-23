package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSimpleServerDoesNotExposeApplicationHistoryRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := &app{}
	engine := gin.New()
	a.registerAISDK(engine.Group("/api/usechat"))
	a.registerOpenAI(engine.Group("/api/v1"))

	for _, path := range []string{
		"/api/usechat/sessions/session_1/messages",
		"/api/v1/sessions/session_1/messages",
	} {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("history route %s must not be exposed, got %d: %s", path, recorder.Code, recorder.Body.String())
		}
	}
}

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func newTestChatRouter(store *RunStore) *gin.Engine {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	api := router.Group("/api")
	handler := &Handler{
		AgentManager: &AgentManager{runStore: store},
	}
	handler.ChatRouter(api)

	return router
}

func TestGetSessionRunReturnsNullWhenSessionHasNoRun(t *testing.T) {
	store, cleanup := newTestRunStore(t)
	defer cleanup()

	router := newTestChatRouter(store)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/session-1", nil)

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		SessionID string   `json:"sessionId"`
		Run       *RunMeta `json:"run"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.SessionID != "session-1" || body.Run != nil {
		t.Fatalf("unexpected response: %#v", body)
	}
}

func TestGetSessionRunReturnsCurrentRun(t *testing.T) {
	store, cleanup := newTestRunStore(t)
	defer cleanup()

	ctx := context.Background()
	if err := store.InitRun(ctx, "session-1", "run-1", "hello"); err != nil {
		t.Fatal(err)
	}

	router := newTestChatRouter(store)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/session-1", nil)

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		SessionID string   `json:"sessionId"`
		Run       *RunMeta `json:"run"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.SessionID != "session-1" || body.Run == nil || body.Run.RunID != "run-1" {
		t.Fatalf("unexpected response: %#v", body)
	}
}

func TestSessionRunStreamReturnsDoneWhenRunIsNotCurrent(t *testing.T) {
	store, cleanup := newTestRunStore(t)
	defer cleanup()

	router := newTestChatRouter(store)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/chat/sessions/session-1/runs/run-1", nil)

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "data: [DONE]\n\n" {
		t.Fatalf("expected only done SSE event, got %q", rec.Body.String())
	}
}

func TestSessionRunStreamReadsCurrentRunEvents(t *testing.T) {
	store, cleanup := newTestRunStore(t)
	defer cleanup()

	ctx := context.Background()
	if err := store.InitRun(ctx, "session-1", "run-1", "hello"); err != nil {
		t.Fatal(err)
	}
	eventID, err := store.Append(ctx, "session-1", "run-1", `{"choices":[{"delta":{"content":"hi"}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, "session-1", "run-1", "[DONE]"); err != nil {
		t.Fatal(err)
	}

	router := newTestChatRouter(store)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/chat/sessions/session-1/runs/run-1", nil)

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "id: "+eventID+"\n") {
		t.Fatalf("expected event id %s, got %q", eventID, body)
	}
	if !strings.Contains(body, `data: {"choices":[{"delta":{"content":"hi"}}]}`) {
		t.Fatalf("expected OpenAI-compatible chunk, got %q", body)
	}
	if !strings.Contains(body, "data: [DONE]\n\n") {
		t.Fatalf("expected done SSE event, got %q", body)
	}
}

func TestSessionRunStreamReturnsDoneWhenRunIDDoesNotMatchCurrentRun(t *testing.T) {
	store, cleanup := newTestRunStore(t)
	defer cleanup()

	ctx := context.Background()
	if err := store.InitRun(ctx, "session-1", "run-1", "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, "session-1", "run-1", `{"choices":[{"delta":{"content":"old"}}]}`); err != nil {
		t.Fatal(err)
	}

	router := newTestChatRouter(store)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/chat/sessions/session-1/runs/run-other", nil)

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "data: [DONE]\n\n" {
		t.Fatalf("expected only done SSE event, got %q", rec.Body.String())
	}
}

func TestSessionRunStreamReturnsOnlyDoneAfterCurrentRunFinished(t *testing.T) {
	store, cleanup := newTestRunStore(t)
	defer cleanup()

	ctx := context.Background()
	if err := store.InitRun(ctx, "session-1", "run-1", "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, "session-1", "run-1", `{"choices":[{"delta":{"content":"old"}}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, "session-1", "run-1", "[DONE]"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunStatus(ctx, "session-1", "run-1", RunStatusDone); err != nil {
		t.Fatal(err)
	}

	router := newTestChatRouter(store)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/chat/sessions/session-1/runs/run-1", nil)

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "data: [DONE]\n\n" {
		t.Fatalf("expected only done SSE event, got %q", rec.Body.String())
	}
}

func TestCreateRunEndpointUsesSessionID(t *testing.T) {
	store, cleanup := newTestRunStore(t)
	defer cleanup()

	router := newTestChatRouter(store)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/chat/sessions/session-1",
		strings.NewReader(`{"message":"hello"}`),
	)
	req.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		SessionID string    `json:"sessionId"`
		RunID     string    `json:"runId"`
		Status    RunStatus `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.SessionID != "session-1" || body.RunID == "" || body.Status != RunStatusRunning {
		t.Fatalf("unexpected response: %#v", body)
	}

	run, err := store.GetRun(ctxWithTestTimeout(t), "session-1", body.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run == nil || run.Message != "hello" {
		t.Fatalf("expected stored run, got %#v", run)
	}
}

func TestCancelSessionRunEndpointCancelsCurrentRun(t *testing.T) {
	store, cleanup := newTestRunStore(t)
	defer cleanup()

	ctx := context.Background()
	if err := store.InitRun(ctx, "session-1", "run-1", "hello"); err != nil {
		t.Fatal(err)
	}

	router := newTestChatRouter(store)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/chat/sessions/session-1/cancel/run-1", nil)

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Run *RunMeta `json:"run"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Run == nil || body.Run.Status != RunStatusCanceled {
		t.Fatalf("expected canceled run, got %#v", body.Run)
	}
}

func TestCancelSessionRunEndpointReturnsNullWhenNoCurrentRun(t *testing.T) {
	store, cleanup := newTestRunStore(t)
	defer cleanup()

	router := newTestChatRouter(store)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/chat/sessions/session-1/cancel/run-1", nil)

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		SessionID string   `json:"sessionId"`
		Run       *RunMeta `json:"run"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.SessionID != "session-1" || body.Run != nil {
		t.Fatalf("unexpected response: %#v", body)
	}
}

func ctxWithTestTimeout(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

func TestOldRunIDRoutesAreRemoved(t *testing.T) {
	store, cleanup := newTestRunStore(t)
	defer cleanup()

	router := newTestChatRouter(store)
	for _, tc := range []struct {
		method string
		path   string
		body   io.Reader
	}{
		{method: http.MethodPost, path: "/api/chat/sessions/session-1/messages", body: strings.NewReader(`{"message":"hello"}`)},
		{method: http.MethodPost, path: "/api/chat/sessions/session-1/runs"},
		{method: http.MethodGet, path: "/api/chat/sessions/session-1/runs/run-1/events"},
		{method: http.MethodPost, path: "/api/chat/sessions/session-1/cancel"},
		{method: http.MethodPost, path: "/api/chat/runs/run-1/cancel"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, tc.body)
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s: expected 404, got %d", tc.method, tc.path, rec.Code)
		}
	}
}

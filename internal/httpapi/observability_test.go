package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestObserveAddsRequestIDAndRecoversPanics(t *testing.T) {
	server := &Server{}
	handler := server.observe(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/panic", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", recorder.Code)
	}
	requestID := recorder.Header().Get("X-Request-ID")
	if len(requestID) != 16 {
		t.Fatalf("request id = %q", requestID)
	}
	if !strings.Contains(recorder.Body.String(), requestID) || strings.Contains(recorder.Body.String(), "boom") {
		t.Fatalf("panic response leaked details or omitted request id: %s", recorder.Body.String())
	}
}

func TestObservePreservesNormalResponse(t *testing.T) {
	server := &Server{}
	handler := server.observe(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("ok"))
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/work", nil))
	if recorder.Code != http.StatusAccepted || recorder.Body.String() != "ok" {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("X-Request-ID") == "" {
		t.Fatal("request id is missing")
	}
}

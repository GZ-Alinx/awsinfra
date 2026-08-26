package httpapi

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompressResponseCompressesJSONAndPreservesBody(t *testing.T) {
	handler := compressResponse(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "2048")
		_, _ = io.WriteString(w, `{"payload":"`+strings.Repeat("data", 256)+`"}`)
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	request.Header.Set("Accept-Encoding", "br, gzip")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("content encoding = %q", recorder.Header().Get("Content-Encoding"))
	}
	if recorder.Header().Get("Content-Length") != "" {
		t.Fatalf("compressed response retained content length %q", recorder.Header().Get("Content-Length"))
	}
	reader, err := gzip.NewReader(recorder.Body)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), strings.Repeat("data", 32)) {
		t.Fatalf("decompressed response was corrupted: %q", payload)
	}
}

func TestCompressResponseSkipsDisabledRangeAndBinaryResponses(t *testing.T) {
	tests := []struct {
		name     string
		encoding string
		rangeSet bool
		content  string
	}{
		{name: "quality disabled", encoding: "gzip;q=0", content: "application/json"},
		{name: "range request", encoding: "gzip", rangeSet: true, content: "application/json"},
		{name: "binary response", encoding: "gzip", content: "application/octet-stream"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := compressResponse(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", test.content)
				_, _ = io.WriteString(w, strings.Repeat("payload", 32))
			}))
			request := httptest.NewRequest(http.MethodGet, "/asset", nil)
			request.Header.Set("Accept-Encoding", test.encoding)
			if test.rangeSet {
				request.Header.Set("Range", "bytes=0-9")
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Header().Get("Content-Encoding") != "" {
				t.Fatalf("unexpected compression for %s", test.name)
			}
		})
	}
}

func TestCompressResponseLeavesSmallJSONUncompressed(t *testing.T) {
	handler := compressResponse(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Header().Get("Content-Encoding") != "" {
		t.Fatal("small JSON response should not be compressed")
	}
	if recorder.Body.String() != `{"status":"ok"}` {
		t.Fatalf("small JSON response changed: %q", recorder.Body.String())
	}
}

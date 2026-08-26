package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
	"time"
)

const slowRequestThreshold = 2 * time.Second

type observedResponseWriter struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (w *observedResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *observedResponseWriter) Write(payload []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	count, err := w.ResponseWriter.Write(payload)
	w.bytes += count
	return count, err
}

// Unwrap lets net/http.ResponseController retain optional capabilities such as
// flushing and hijacking when a future endpoint needs them.
func (w *observedResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := newRequestID()
		w.Header().Set("X-Request-ID", requestID)
		observed := &observedResponseWriter{ResponseWriter: w, status: http.StatusOK}
		started := time.Now()
		defer func() {
			duration := time.Since(started)
			if recovered := recover(); recovered != nil {
				log.Printf("request panic id=%s method=%s path=%s panic=%s stack=%s",
					requestID, r.Method, safeLogField(r.URL.Path), safeLogField(fmt.Sprint(recovered)), safeLogField(string(debug.Stack()))) // #nosec G706 -- all attacker-controlled values are stripped and bounded.
				if !observed.wroteHeader {
					writeJSON(observed, http.StatusInternalServerError, map[string]string{
						"error":      "平台处理请求时发生内部异常，请稍后重试；如问题持续，请向管理员提供请求编号",
						"request_id": requestID,
					})
				}
				return
			}
			if duration >= slowRequestThreshold || observed.status >= http.StatusInternalServerError {
				log.Printf("request completed id=%s method=%s path=%s status=%d bytes=%d duration=%s",
					requestID, r.Method, safeLogField(r.URL.Path), observed.status, observed.bytes, duration.Round(time.Millisecond)) // #nosec G706 -- all attacker-controlled values are stripped and bounded.
			}
		}()
		next.ServeHTTP(observed, r)
	})
}

func newRequestID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}

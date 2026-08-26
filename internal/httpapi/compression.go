package httpapi

import (
	"compress/gzip"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

const minimumCompressionBytes = 1024

// compressResponse reduces large environment, CI/CD and static UI payloads.
// It deliberately skips range responses and binary content so FileServer
// semantics and downloadable artifacts remain unchanged.
func compressResponse(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || r.Header.Get("Range") != "" || r.Header.Get("Upgrade") != "" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		if !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		compressed := &gzipResponseWriter{ResponseWriter: w}
		defer compressed.Close()
		next.ServeHTTP(compressed, r)
	})
}

type gzipResponseWriter struct {
	http.ResponseWriter
	writer      *gzip.Writer
	wroteHeader bool
	sentHeader  bool
	compressing bool
	status      int
	pending     []byte
}

func (w *gzipResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status
	if !statusHasBody(status) || !compressibleContentType(w.Header().Get("Content-Type")) || w.Header().Get("Content-Encoding") != "" {
		w.startPlain()
		return
	}
	if length, err := strconv.Atoi(w.Header().Get("Content-Length")); err == nil {
		if length < minimumCompressionBytes {
			w.startPlain()
		} else {
			w.startCompressed()
		}
	}
}

func (w *gzipResponseWriter) Write(payload []byte) (int, error) {
	if !w.wroteHeader {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", http.DetectContentType(payload))
		}
		w.WriteHeader(http.StatusOK)
	}
	if !w.sentHeader {
		w.pending = append(w.pending, payload...)
		if len(w.pending) < minimumCompressionBytes {
			return len(payload), nil
		}
		w.startCompressed()
		pending := w.pending
		w.pending = nil
		if _, err := w.writer.Write(pending); err != nil {
			return 0, err
		}
		return len(payload), nil
	}
	if w.compressing {
		return w.writer.Write(payload)
	}
	return w.ResponseWriter.Write(payload)
}

func (w *gzipResponseWriter) Close() {
	if w.wroteHeader && !w.sentHeader {
		w.startPlain()
		if len(w.pending) > 0 {
			_, _ = w.ResponseWriter.Write(w.pending)
			w.pending = nil
		}
	}
	if w.writer != nil {
		_ = w.writer.Close()
	}
}

func (w *gzipResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *gzipResponseWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if !w.sentHeader {
		if compressibleContentType(w.Header().Get("Content-Type")) {
			w.startCompressed()
		} else {
			w.startPlain()
		}
	}
	if len(w.pending) > 0 {
		if w.compressing {
			_, _ = w.writer.Write(w.pending)
		} else {
			_, _ = w.ResponseWriter.Write(w.pending)
		}
		w.pending = nil
	}
	if w.writer != nil {
		_ = w.writer.Flush()
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *gzipResponseWriter) startPlain() {
	if w.sentHeader {
		return
	}
	w.sentHeader = true
	w.ResponseWriter.WriteHeader(w.status)
}

func (w *gzipResponseWriter) startCompressed() {
	if w.sentHeader {
		return
	}
	w.sentHeader = true
	w.compressing = true
	w.Header().Del("Content-Length")
	w.Header().Set("Content-Encoding", "gzip")
	w.writer = gzip.NewWriter(w.ResponseWriter)
	w.ResponseWriter.WriteHeader(w.status)
}

func acceptsGzip(value string) bool {
	for _, raw := range strings.Split(value, ",") {
		parts := strings.Split(raw, ";")
		if !strings.EqualFold(strings.TrimSpace(parts[0]), "gzip") {
			continue
		}
		quality := 1.0
		for _, parameter := range parts[1:] {
			keyValue := strings.SplitN(strings.TrimSpace(parameter), "=", 2)
			if len(keyValue) == 2 && strings.EqualFold(keyValue[0], "q") {
				parsed, err := strconv.ParseFloat(keyValue[1], 64)
				if err != nil {
					return false
				}
				quality = parsed
			}
		}
		return quality > 0
	}
	return false
}

func compressibleContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	if strings.HasPrefix(mediaType, "text/") {
		return true
	}
	switch mediaType {
	case "application/json", "application/javascript", "application/x-javascript",
		"application/manifest+json", "application/xml", "application/yaml",
		"application/x-yaml", "image/svg+xml":
		return true
	default:
		return strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml")
	}
}

func statusHasBody(status int) bool {
	return status >= 200 && status != http.StatusNoContent && status != http.StatusNotModified
}

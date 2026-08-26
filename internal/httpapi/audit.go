package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/auditlog"
)

func (s *Server) listAuditEvents(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformPermission(w, r, platformViewAudit) {
		return
	}
	if s.auditStore == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("audit store is unavailable"))
		return
	}

	query := auditlog.Query{
		Username:      strings.TrimSpace(r.URL.Query().Get("username")),
		Method:        strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("method"))),
		Result:        strings.ToLower(strings.TrimSpace(r.URL.Query().Get("result"))),
		Keyword:       strings.TrimSpace(r.URL.Query().Get("keyword")),
		IncludeSystem: strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("include_system")), "true"),
		Page:          parsePositiveInt(r.URL.Query().Get("page"), 1),
		PageSize:      parsePositiveInt(r.URL.Query().Get("page_size"), 20),
	}
	if query.PageSize > 100 {
		query.PageSize = 100
	}
	if query.Method != "" && query.Method != http.MethodPost && query.Method != http.MethodPut &&
		query.Method != http.MethodPatch && query.Method != http.MethodDelete {
		writeError(w, http.StatusBadRequest, errors.New("unsupported audit method filter"))
		return
	}
	if query.Result != "" && query.Result != "success" && query.Result != "failed" {
		writeError(w, http.StatusBadRequest, errors.New("unsupported audit result filter"))
		return
	}
	var err error
	if value := strings.TrimSpace(r.URL.Query().Get("from")); value != "" {
		query.From, err = parseAuditTime(value)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid audit start time"))
			return
		}
	}
	if value := strings.TrimSpace(r.URL.Query().Get("to")); value != "" {
		query.To, err = parseAuditTime(value)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid audit end time"))
			return
		}
	}
	if query.From != nil && query.To != nil && query.From.After(*query.To) {
		writeError(w, http.StatusBadRequest, errors.New("audit start time must not be after end time"))
		return
	}

	result, err := s.auditStore.ListAuditEvents(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func parsePositiveInt(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 1 {
		return fallback
	}
	return parsed
}

func parseAuditTime(value string) (*time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, err
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

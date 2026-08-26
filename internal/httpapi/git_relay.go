package httpapi

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ops-deploy-platform/internal/cicd"
	"ops-deploy-platform/internal/gitlab"
)

const maxGitRelayRequestBytes = 8 << 20

var gitRelayClient = &http.Client{
	Timeout: 10 * time.Minute,
	CheckRedirect: func(request *http.Request, via []*http.Request) error {
		return errors.New("Git relay upstream redirect rejected")
	},
}

func (s *Server) gitRelay(w http.ResponseWriter, r *http.Request) {
	if s.gitlab == nil || s.cicd == nil {
		http.Error(w, "Git relay is unavailable", http.StatusServiceUnavailable)
		return
	}
	project := strings.ToLower(strings.TrimSpace(r.PathValue("project")))
	kind, serviceKey, operation, ok := parseGitRelayPath(r.PathValue("repository"))
	if !ok || (r.Method == http.MethodGet && (operation != "info/refs" || r.URL.Query().Get("service") != "git-upload-pack")) || (r.Method == http.MethodPost && (operation != "git-upload-pack" || r.URL.RawQuery != "")) {
		http.NotFound(w, r)
		return
	}
	username, password, hasBasic := r.BasicAuth()
	if !hasBasic || username == "" || password == "" {
		gitRelayUnauthorized(w)
		return
	}
	authorized, err := s.cicd.AuthorizeGitRelay(r.Context(), project, "gitlab-delivery-read", username, password)
	if err != nil || !authorized {
		password = ""
		gitRelayUnauthorized(w)
		return
	}
	upstreamUsername, upstreamPassword := username, []byte(password)
	var upstreamClone string
	if kind == "source" {
		upstreamClone, upstreamUsername, upstreamPassword, err = s.gitlab.SourceRelayTarget(r.Context(), project, serviceKey)
	} else {
		upstreamClone, err = s.gitlab.RelayCloneTarget(r.Context(), project, kind, serviceKey)
	}
	password = ""
	if err != nil {
		clear(upstreamPassword)
		writeGitRelayError(w, err)
		return
	}
	upstream, err := url.Parse(upstreamClone + "/" + operation)
	if err != nil {
		password = ""
		http.Error(w, "Git relay target is invalid", http.StatusBadGateway)
		return
	}
	upstream.RawQuery = r.URL.RawQuery
	body := http.MaxBytesReader(w, r.Body, maxGitRelayRequestBytes)
	request, err := http.NewRequestWithContext(r.Context(), r.Method, upstream.String(), body)
	if err != nil {
		password = ""
		http.Error(w, "Git relay request is invalid", http.StatusBadRequest)
		return
	}
	request.SetBasicAuth(upstreamUsername, string(upstreamPassword))
	clear(upstreamPassword)
	for _, header := range []string{"Accept", "Content-Type", "Git-Protocol", "User-Agent"} {
		if value := strings.TrimSpace(r.Header.Get(header)); value != "" {
			request.Header.Set(header, value)
		}
	}
	response, err := gitRelayClient.Do(request)
	if err != nil {
		http.Error(w, "Git relay upstream request failed", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	for _, header := range []string{"Content-Type", "Content-Length", "Cache-Control", "ETag", "Last-Modified", "WWW-Authenticate"} {
		if value := response.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func parseGitRelayPath(value string) (kind, serviceKey, operation string, ok bool) {
	parts := strings.Split(strings.Trim(strings.TrimSpace(value), "/"), "/")
	if len(parts) >= 2 && len(parts) <= 3 {
		operation = strings.Join(parts[1:], "/")
		switch parts[0] {
		case "jenkinsfiles.git":
			return "jenkinsfiles", "", operation, true
		case "manifests.git":
			return "manifests", "", operation, true
		}
	}
	if len(parts) >= 3 && len(parts) <= 4 && parts[0] == "source" && strings.HasSuffix(parts[1], ".git") {
		serviceKey = strings.TrimSuffix(parts[1], ".git")
		if serviceKey != "" && !strings.ContainsAny(serviceKey, ".\\") {
			return "source", serviceKey, strings.Join(parts[2:], "/"), true
		}
	}
	return "", "", "", false
}

func gitRelayUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="Git Relay"`)
	http.Error(w, "Git relay credentials are invalid", http.StatusUnauthorized)
}

func writeGitRelayError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gitlab.ErrInvalid):
		http.Error(w, "Git relay repository is invalid", http.StatusBadRequest)
	case errors.Is(err, gitlab.ErrNotFound), errors.Is(err, cicd.ErrNotFound):
		http.Error(w, "Git relay repository was not found", http.StatusNotFound)
	case errors.Is(err, gitlab.ErrConflict):
		http.Error(w, "Git relay repository is not owned by this project", http.StatusConflict)
	default:
		http.Error(w, "Git relay repository lookup failed", http.StatusBadGateway)
	}
}

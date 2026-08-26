package cicd

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type jenkinsClient struct {
	baseURL, username, token string
	http                     *http.Client
}

func newJenkinsClient(baseURL, username, token string) (*jenkinsClient, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 30 * time.Second, Jar: jar}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !strings.EqualFold(request.URL.Host, parsed.Host) {
			return errors.New("Jenkins redirect was rejected")
		}
		return nil
	}
	return &jenkinsClient{baseURL: strings.TrimSuffix(baseURL, "/"), username: username, token: token, http: client}, nil
}

func (c *jenkinsClient) test(ctx context.Context) (string, error) {
	var result struct {
		Version string `json:"version"`
	}
	response, err := c.request(ctx, http.MethodGet, "/api/json", nil, "")
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("decode Jenkins response: %w", err)
	}
	version := response.Header.Get("X-Jenkins")
	if version == "" {
		version = result.Version
	}
	return version, nil
}

func (c *jenkinsClient) upsertJob(ctx context.Context, job Job, credentials map[string]string) error {
	config := pipelineJobXML(job, credentials)
	path := "/job/" + url.PathEscape(job.JenkinsJobName) + "/config.xml"
	response, err := c.request(ctx, http.MethodGet, path, nil, "")
	if err == nil {
		response.Body.Close()
		response, err = c.requestWithCrumb(ctx, http.MethodPost, path, bytes.NewReader(config), "application/xml; charset=utf-8")
	} else if statusError(err, http.StatusNotFound) {
		response, err = c.requestWithCrumb(ctx, http.MethodPost, "/createItem?name="+url.QueryEscape(job.JenkinsJobName), bytes.NewReader(config), "application/xml; charset=utf-8")
	}
	if err != nil {
		return err
	}
	response.Body.Close()
	return c.verifyPipelineJob(ctx, job, credentials)
}

func (c *jenkinsClient) deleteJob(ctx context.Context, name string) (deleted bool, missing bool, err error) {
	path := "/job/" + url.PathEscape(name) + "/doDelete"
	response, err := c.requestWithCrumb(ctx, http.MethodPost, path, nil, "")
	if statusError(err, http.StatusNotFound) {
		return false, true, nil
	}
	if err != nil {
		return false, false, err
	}
	response.Body.Close()
	return true, false, nil
}

// upsertInlineJob is intentionally limited to trusted, platform-owned
// pipelines. User supplied Jenkinsfiles must continue to use upsertJob so the
// source of truth remains the configured Git repository.
func (c *jenkinsClient) upsertInlineJob(ctx context.Context, job Job, script string) error {
	config := inlinePipelineJobXML(job, script)
	path := "/job/" + url.PathEscape(job.JenkinsJobName) + "/config.xml"
	response, err := c.request(ctx, http.MethodGet, path, nil, "")
	if err == nil {
		response.Body.Close()
		response, err = c.requestWithCrumb(ctx, http.MethodPost, path, bytes.NewReader(config), "application/xml; charset=utf-8")
	} else if statusError(err, http.StatusNotFound) {
		response, err = c.requestWithCrumb(ctx, http.MethodPost, "/createItem?name="+url.QueryEscape(job.JenkinsJobName), bytes.NewReader(config), "application/xml; charset=utf-8")
	}
	if err != nil {
		return err
	}
	response.Body.Close()
	return nil
}

func (c *jenkinsClient) verifyPipelineJob(ctx context.Context, job Job, credentials map[string]string) error {
	path := "/job/" + url.PathEscape(job.JenkinsJobName) + "/config.xml"
	response, err := c.request(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return fmt.Errorf("verify Jenkins Job: %w", err)
	}
	defer response.Body.Close()
	inspection := JenkinsJobInspection{Name: job.JenkinsJobName}
	if err := decodeSafeJenkinsConfig(io.LimitReader(response.Body, 8<<20), &inspection); err != nil {
		return fmt.Errorf("verify Jenkins Job config: %w", err)
	}
	checks := []struct {
		name  string
		value string
		items []string
	}{
		{name: "Jenkinsfile repository", value: job.JenkinsfileRepo, items: inspection.SCMURLs},
		{name: "Jenkinsfile path", value: job.JenkinsfilePath, items: inspection.ScriptPaths},
		{name: "Jenkinsfile credential", value: credentials["jenkinsfile"], items: inspection.CredentialIDs},
	}
	for _, check := range checks {
		if check.value != "" && !containsInspectionValue(check.items, check.value) {
			return fmt.Errorf("verify Jenkins Job: %s was not persisted", check.name)
		}
	}
	expectedBranch := "*/" + job.JenkinsfileBranch
	if job.JenkinsfileBranch != "" && !containsInspectionValue(inspection.Branches, expectedBranch) && !containsInspectionValue(inspection.Branches, job.JenkinsfileBranch) {
		return fmt.Errorf("verify Jenkins Job: Jenkinsfile branch was not persisted")
	}
	return nil
}

func containsInspectionValue(items []string, expected string) bool {
	for _, item := range items {
		if strings.TrimSpace(item) == strings.TrimSpace(expected) {
			return true
		}
	}
	return false
}

func (c *jenkinsClient) upsertCredential(ctx context.Context, credential StoredCredential, secret CredentialSecret) error {
	payload, err := credentialXML(credential, secret)
	if err != nil {
		return err
	}
	path := "/credentials/store/system/domain/_/credential/" + url.PathEscape(credential.ExternalID) + "/config.xml"
	response, checkErr := c.request(ctx, http.MethodGet, path, nil, "")
	if checkErr == nil {
		response.Body.Close()
		response, err = c.requestWithCrumb(ctx, http.MethodPost, path, bytes.NewReader(payload), "application/xml; charset=utf-8")
	} else if statusError(checkErr, http.StatusNotFound) {
		response, err = c.requestWithCrumb(ctx, http.MethodPost, "/credentials/store/system/domain/_/createCredentials", bytes.NewReader(payload), "application/xml; charset=utf-8")
	} else {
		err = checkErr
	}
	if err != nil {
		return err
	}
	response.Body.Close()
	return nil
}

func (c *jenkinsClient) inspectCredential(ctx context.Context, externalID string) (CredentialInspection, error) {
	path := "/credentials/store/system/domain/_/credential/" + url.PathEscape(externalID) + "/config.xml"
	response, err := c.request(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return CredentialInspection{}, err
	}
	defer response.Body.Close()
	var payload struct {
		ID       string `xml:"id"`
		Username string `xml:"username"`
		Password string `xml:"password"`
	}
	if err := xml.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return CredentialInspection{}, err
	}
	inspection := CredentialInspection{ExternalID: payload.ID, Username: payload.Username, PasswordPresent: payload.Password != "", PasswordEncrypted: strings.HasPrefix(payload.Password, "{") && strings.HasSuffix(payload.Password, "}")}
	payload.Password = ""
	return inspection, nil
}

func (c *jenkinsClient) trigger(ctx context.Context, name string, parameters map[string]string) (string, error) {
	values := url.Values{}
	for key, value := range parameters {
		values.Set(key, value)
	}
	response, err := c.requestWithCrumb(ctx, http.MethodPost, "/job/"+url.PathEscape(name)+"/buildWithParameters", strings.NewReader(values.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	location := response.Header.Get("Location")
	if location == "" {
		queued, found, lookupErr := c.findQueuedBuild(ctx, name, parameters)
		if lookupErr != nil {
			return "", lookupErr
		}
		if found {
			return queued, nil
		}
		return "", fmt.Errorf("%w: Jenkins accepted the build but did not return or expose a queue item", ErrJenkins)
	}
	queuePath, err := c.referencePath(location)
	if err != nil {
		return "", fmt.Errorf("%w: Jenkins returned an invalid queue location", ErrJenkins)
	}
	return queuePath, nil
}

func (c *jenkinsClient) findQueuedBuild(ctx context.Context, name string, parameters map[string]string) (string, bool, error) {
	response, err := c.request(ctx, http.MethodGet, "/queue/api/json?tree=items[id,url,task[name,url],actions[parameters[name,value]]]", nil, "")
	if err != nil {
		return "", false, err
	}
	defer response.Body.Close()
	var queue struct {
		Items []struct {
			ID   int64  `json:"id"`
			URL  string `json:"url"`
			Task struct {
				Name string `json:"name"`
				URL  string `json:"url"`
			} `json:"task"`
			Actions []struct {
				Parameters []struct {
					Name  string `json:"name"`
					Value any    `json:"value"`
				} `json:"parameters"`
			} `json:"actions"`
		} `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&queue); err != nil {
		return "", false, err
	}
	var candidateID int64
	candidateURL := ""
	for _, item := range queue.Items {
		if item.ID <= candidateID || !queueTaskMatches(item.Task.Name, item.Task.URL, name) || !queueParametersMatch(item.Actions, parameters) {
			continue
		}
		candidateID, candidateURL = item.ID, item.URL
	}
	if candidateID == 0 {
		return "", false, nil
	}
	if candidateURL == "" {
		candidateURL = fmt.Sprintf("/queue/item/%d/", candidateID)
	}
	path, err := c.referencePath(candidateURL)
	if err != nil {
		return "", false, fmt.Errorf("%w: Jenkins returned an invalid queue item", ErrJenkins)
	}
	return path, true, nil
}

func queueTaskMatches(taskName, taskURL, expected string) bool {
	if strings.EqualFold(strings.TrimSpace(taskName), strings.TrimSpace(expected)) {
		return true
	}
	parsed, err := url.Parse(strings.TrimSpace(taskURL))
	if err != nil {
		return false
	}
	return strings.Contains(parsed.Path, "/job/"+url.PathEscape(expected)+"/")
}

func queueParametersMatch(actions []struct {
	Parameters []struct {
		Name  string `json:"name"`
		Value any    `json:"value"`
	} `json:"parameters"`
}, expected map[string]string) bool {
	actual := map[string]string{}
	for _, action := range actions {
		for _, parameter := range action.Parameters {
			actual[parameter.Name] = fmt.Sprint(parameter.Value)
		}
	}
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return len(expected) > 0
}

func (c *jenkinsClient) refresh(ctx context.Context, name string, build Build) (Build, error) {
	now := time.Now().UTC()
	if build.BuildNumber == 0 {
		queueURL, err := c.referencePath(build.QueueURL)
		if err != nil {
			return build, err
		}
		response, err := c.request(ctx, http.MethodGet, queueURL+"api/json", nil, "")
		if err != nil {
			if statusError(err, http.StatusNotFound) {
				recovered, found, recoverErr := c.recoverExpiredQueueItem(ctx, name, queueURL, build)
				if recoverErr != nil {
					return build, recoverErr
				}
				if found {
					return recovered, nil
				}
			}
			return build, err
		}
		defer response.Body.Close()
		var queue struct {
			Cancelled  bool `json:"cancelled"`
			Executable *struct {
				Number int64  `json:"number"`
				URL    string `json:"url"`
			} `json:"executable"`
			Why string `json:"why"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&queue); err != nil {
			return build, err
		}
		if queue.Cancelled {
			build.Status, build.Result, build.FinishedAt = "canceled", "ABORTED", now
		} else if queue.Executable != nil {
			buildURL, pathErr := c.referencePath(queue.Executable.URL)
			if pathErr != nil {
				return build, fmt.Errorf("%w: Jenkins returned an invalid build location", ErrJenkins)
			}
			build.BuildNumber, build.BuildURL, build.Status, build.StartedAt, build.Progress = queue.Executable.Number, buildURL, "running", now, 10
		}
		build.UpdatedAt = now
		return build, nil
	}
	response, err := c.request(ctx, http.MethodGet, "/job/"+url.PathEscape(name)+"/"+strconv.FormatInt(build.BuildNumber, 10)+"/api/json", nil, "")
	if err != nil {
		return build, err
	}
	defer response.Body.Close()
	var state struct {
		Building  bool   `json:"building"`
		Result    string `json:"result"`
		URL       string `json:"url"`
		Timestamp int64  `json:"timestamp"`
		Duration  int64  `json:"duration"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&state); err != nil {
		return build, err
	}
	if state.URL != "" {
		buildURL, pathErr := c.referencePath(state.URL)
		if pathErr != nil {
			return build, fmt.Errorf("%w: Jenkins returned an invalid build location", ErrJenkins)
		}
		build.BuildURL = buildURL
	}
	build.Result, build.UpdatedAt = state.Result, now
	if state.Building {
		build.Status = "running"
		if build.Progress < 15 {
			build.Progress = 15
		}
	} else {
		build.Status = mapResult(state.Result)
		build.Progress = 100
		build.FinishedAt = now
		if state.Timestamp > 0 {
			build.StartedAt = time.UnixMilli(state.Timestamp).UTC()
			build.FinishedAt = time.UnixMilli(state.Timestamp + state.Duration).UTC()
		}
	}
	return build, nil
}

func (c *jenkinsClient) recoverExpiredQueueItem(ctx context.Context, name, queueURL string, build Build) (Build, bool, error) {
	queueID, ok := queueItemID(queueURL)
	if !ok {
		return build, false, nil
	}
	path := "/job/" + url.PathEscape(name) + "/api/json?tree=builds[number,url,queueId]{0,100}"
	response, err := c.request(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return build, false, err
	}
	defer response.Body.Close()
	var history struct {
		Builds []struct {
			Number  int64  `json:"number"`
			URL     string `json:"url"`
			QueueID int64  `json:"queueId"`
		} `json:"builds"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&history); err != nil {
		return build, false, err
	}
	for _, candidate := range history.Builds {
		if candidate.QueueID != queueID || candidate.Number <= 0 {
			continue
		}
		build.BuildNumber = candidate.Number
		if candidate.URL != "" {
			buildURL, pathErr := c.referencePath(candidate.URL)
			if pathErr != nil {
				return build, false, fmt.Errorf("%w: Jenkins returned an invalid recovered build location", ErrJenkins)
			}
			build.BuildURL = buildURL
		}
		build.Status, build.Progress = "running", max(build.Progress, 10)
		recovered, refreshErr := c.refresh(ctx, name, build)
		return recovered, true, refreshErr
	}
	return build, false, nil
}

func queueItemID(reference string) (int64, bool) {
	parsed, err := url.Parse(strings.TrimSpace(reference))
	if err != nil {
		return 0, false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for index := 0; index+2 < len(parts); index++ {
		if parts[index] != "queue" || parts[index+1] != "item" {
			continue
		}
		value, parseErr := strconv.ParseInt(parts[index+2], 10, 64)
		return value, parseErr == nil && value > 0
	}
	return 0, false
}

func (c *jenkinsClient) logs(ctx context.Context, name string, number, offset int64) (LogChunk, error) {
	requestedOffset := max(offset, 0)
	// Jenkins ConsoleNote metadata is embedded as hidden ANSI/base64 spans. Read
	// a small overlap so an arbitrary progressive offset cannot start in the
	// middle of one and expose the encoded annotation in the platform UI.
	requestOffset := max(requestedOffset-4096, 0)
	path := fmt.Sprintf("/job/%s/%d/logText/progressiveText?start=%d", url.PathEscape(name), number, requestOffset)
	response, err := c.request(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return LogChunk{}, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return LogChunk{}, err
	}
	next, _ := strconv.ParseInt(response.Header.Get("X-Text-Size"), 10, 64)
	return LogChunk{Text: sanitizeJenkinsLog(payload, requestOffset, requestedOffset), NextOffset: next, More: strings.EqualFold(response.Header.Get("X-More-Data"), "true")}, nil
}

func sanitizeJenkinsLog(payload []byte, baseOffset, requestedOffset int64) string {
	const consoleNotePrefix = "\x1b[8mha:"
	const consoleNoteSuffix = "\x1b[0m"
	var output bytes.Buffer
	for index := 0; index < len(payload); {
		if bytes.HasPrefix(payload[index:], []byte(consoleNotePrefix)) {
			end := bytes.Index(payload[index+len(consoleNotePrefix):], []byte(consoleNoteSuffix))
			if end < 0 {
				break
			}
			index += len(consoleNotePrefix) + end + len(consoleNoteSuffix)
			continue
		}
		if payload[index] == 0x1b && index+1 < len(payload) && payload[index+1] == '[' {
			index += 2
			for index < len(payload) {
				value := payload[index]
				index++
				if value >= 0x40 && value <= 0x7e {
					break
				}
			}
			continue
		}
		if baseOffset+int64(index) >= requestedOffset {
			value := payload[index]
			if value >= 0x20 || value == '\n' || value == '\r' || value == '\t' {
				output.WriteByte(value)
			}
		}
		index++
	}
	return output.String()
}

func (c *jenkinsClient) fullLogs(ctx context.Context, name string, number int64) (string, error) {
	path := fmt.Sprintf("/job/%s/%d/consoleText", url.PathEscape(name), number)
	response, err := c.request(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	return string(payload), err
}

func (c *jenkinsClient) stageProgress(ctx context.Context, name string, number int64, buildStatus string) ([]BuildStage, int, string, error) {
	path := fmt.Sprintf("/job/%s/%d/wfapi/describe", url.PathEscape(name), number)
	response, err := c.request(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return nil, 0, "", err
	}
	defer response.Body.Close()
	var result struct {
		Stages []struct {
			Name           string `json:"name"`
			Status         string `json:"status"`
			DurationMillis int64  `json:"durationMillis"`
		} `json:"stages"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&result); err != nil {
		return nil, 0, "", err
	}
	stages := make([]BuildStage, 0, len(result.Stages))
	completed, current := 0.0, ""
	for _, source := range result.Stages {
		status := normalizeStageStatus(source.Status)
		service, stageName := splitStageService(source.Name)
		stage := BuildStage{Name: stageName, Service: service, Kind: stageKind(stageName), Status: status, DurationMillis: source.DurationMillis}
		stages = append(stages, stage)
		switch status {
		case "succeeded", "failed", "canceled", "skipped":
			completed++
		case "running":
			completed += 0.35
			if current == "" {
				current = source.Name
			}
		}
	}
	progress := 10
	if len(stages) > 0 {
		progress = int(completed * 100 / float64(len(stages)))
	}
	if buildStatus == "running" && progress >= 100 {
		progress = 95
	}
	if buildStatus == "succeeded" {
		progress = 100
	}
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	return stages, progress, current, nil
}

func normalizeStageStatus(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "SUCCESS":
		return "succeeded"
	case "IN_PROGRESS", "PAUSED_PENDING_INPUT":
		return "running"
	case "FAILED", "UNSTABLE":
		return "failed"
	case "ABORTED":
		return "canceled"
	case "NOT_EXECUTED", "SKIPPED":
		return "skipped"
	default:
		return "pending"
	}
}

func splitStageService(value string) (string, string) {
	parts := strings.SplitN(strings.TrimSpace(value), " / ", 2)
	if len(parts) != 2 || !keyPattern.MatchString(strings.ToLower(parts[0])) {
		return "", strings.TrimSpace(value)
	}
	return strings.ToLower(parts[0]), strings.TrimSpace(parts[1])
}

func stageKind(value string) string {
	lower := strings.ToLower(value)
	switch {
	case strings.Contains(lower, "部署"), strings.Contains(lower, "deploy"), strings.Contains(lower, "发布"), strings.Contains(lower, "rollout"):
		return "deploy"
	case strings.Contains(lower, "镜像"), strings.Contains(lower, "image"):
		return "image"
	case strings.Contains(lower, "测试"), strings.Contains(lower, "test"):
		return "test"
	case strings.Contains(lower, "检出"), strings.Contains(lower, "checkout"):
		return "checkout"
	default:
		return "build"
	}
}

func (c *jenkinsClient) cancel(ctx context.Context, build Build) error {
	path := ""
	if build.BuildNumber > 0 {
		path = buildPath(build.BuildURL) + "stop"
	} else {
		queue, err := c.referencePath(build.QueueURL)
		if err != nil {
			return err
		}
		path = strings.TrimSuffix(queue, "/") + "/cancelItem"
	}
	response, err := c.requestWithCrumb(ctx, http.MethodPost, path, nil, "")
	if err != nil {
		return err
	}
	response.Body.Close()
	return nil
}

func (c *jenkinsClient) requestWithCrumb(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	crumbField, crumb := "", ""
	if response, err := c.request(ctx, http.MethodGet, "/crumbIssuer/api/json", nil, ""); err == nil {
		var result struct {
			CrumbRequestField string `json:"crumbRequestField"`
			Crumb             string `json:"crumb"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result)
		response.Body.Close()
		crumbField, crumb = result.CrumbRequestField, result.Crumb
	}
	return c.requestHeaders(ctx, method, path, body, contentType, crumbField, crumb)
}

func (c *jenkinsClient) request(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	return c.requestHeaders(ctx, method, path, body, contentType, "", "")
}

func (c *jenkinsClient) requestHeaders(ctx context.Context, method, path string, body io.Reader, contentType, crumbField, crumb string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth(c.username, c.token)
	request.Header.Set("Accept", "application/json")
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if crumbField != "" {
		request.Header.Set(crumbField, crumb)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: connect to Jenkins: %v", ErrJenkins, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		defer response.Body.Close()
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 128<<10))
		message := safeJenkinsErrorMessage(payload, response.Status)
		return nil, &jenkinsHTTPError{Status: response.StatusCode, Message: message}
	}
	return response, nil
}

// referencePath deliberately stores only the Jenkins-relative path. Managed
// EKS connections use an ephemeral local port-forward, so persisting the
// absolute 127.0.0.1 URL makes queued/running builds unreadable after a tunnel
// reconnect or platform restart. Requests are always sent to c.baseURL; an
// upstream URL can therefore never redirect the client to another host.
func (c *jenkinsClient) referencePath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw != "" && !strings.HasPrefix(raw, "/") && !strings.Contains(raw, "://") {
		raw = "/" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if parsed.IsAbs() && parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("Jenkins returned an unsupported URL scheme")
	}
	if parsed.User != nil {
		return "", errors.New("Jenkins returned a URL containing user information")
	}
	path := parsed.EscapedPath()
	if path == "" || !strings.HasPrefix(path, "/") {
		return "", errors.New("Jenkins returned an invalid path")
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	if parsed.RawQuery != "" {
		path += "?" + parsed.RawQuery + "&"
	}
	return path, nil
}

type jenkinsHTTPError struct {
	Status  int
	Message string
}

func (e *jenkinsHTTPError) Error() string {
	return fmt.Sprintf("%v: HTTP %d: %s", ErrJenkins, e.Status, e.Message)
}

func (e *jenkinsHTTPError) Unwrap() error { return ErrJenkins }

var (
	jenkinsErrorBlockPattern = regexp.MustCompile(`(?is)<(?:h1|h2|h3|pre|p|div)[^>]*>(.*?)</(?:h1|h2|h3|pre|p|div)>`)
	jenkinsHTMLTagPattern    = regexp.MustCompile(`(?s)<[^>]+>`)
)

// safeJenkinsErrorMessage turns Jenkins' large HTML error page into a short,
// useful diagnostic and never returns scripts, styles, crumbs, or a complete
// upstream document to the API caller or audit log.
func safeJenkinsErrorMessage(payload []byte, fallback string) string {
	raw := strings.TrimSpace(string(payload))
	if raw == "" {
		return fallback
	}
	if strings.HasPrefix(raw, "{") {
		var body struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		if json.Unmarshal(payload, &body) == nil {
			if value := strings.TrimSpace(body.Message); value != "" {
				return limit(value, 2000)
			}
			if value := strings.TrimSpace(body.Error); value != "" {
				return limit(value, 2000)
			}
		}
	}
	if !strings.Contains(strings.ToLower(raw), "<html") {
		return limit(strings.Join(strings.Fields(raw), " "), 2000)
	}
	parts := make([]string, 0, 6)
	seen := map[string]bool{}
	for _, match := range jenkinsErrorBlockPattern.FindAllStringSubmatch(raw, 24) {
		value := html.UnescapeString(jenkinsHTMLTagPattern.ReplaceAllString(match[1], " "))
		value = strings.Join(strings.Fields(value), " ")
		lower := strings.ToLower(value)
		if value == "" || seen[value] || strings.Contains(lower, "search") || strings.Contains(lower, "jenkins version") {
			continue
		}
		if strings.Contains(lower, "error") || strings.Contains(lower, "exception") || strings.Contains(lower, "failed") || strings.Contains(lower, "parameter") || strings.Contains(lower, "invalid") || strings.Contains(lower, "illegal") || strings.Contains(lower, "choice") || strings.Contains(lower, "permission") {
			parts = append(parts, value)
			seen[value] = true
		}
		if len(parts) >= 6 {
			break
		}
	}
	if len(parts) == 0 {
		return fallback
	}
	return limit(strings.Join(parts, " | "), 2000)
}
func statusError(err error, status int) bool {
	var target *jenkinsHTTPError
	return errors.As(err, &target) && target.Status == status
}

func pipelineJobXML(job Job, credentials map[string]string) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.1" encoding="UTF-8"?>
<flow-definition plugin="workflow-job"><actions/><description>%s</description><keepDependencies>false</keepDependencies><properties><hudson.model.ParametersDefinitionProperty><parameterDefinitions>%s</parameterDefinitions></hudson.model.ParametersDefinitionProperty></properties><definition class="org.jenkinsci.plugins.workflow.cps.CpsScmFlowDefinition" plugin="workflow-cps"><scm class="hudson.plugins.git.GitSCM" plugin="git"><configVersion>2</configVersion><userRemoteConfigs><hudson.plugins.git.UserRemoteConfig><url>%s</url><credentialsId>%s</credentialsId></hudson.plugins.git.UserRemoteConfig></userRemoteConfigs><branches><hudson.plugins.git.BranchSpec><name>*/%s</name></hudson.plugins.git.BranchSpec></branches><doGenerateSubmoduleConfigurations>false</doGenerateSubmoduleConfigurations><submoduleCfg class="empty-list"/><extensions/></scm><scriptPath>%s</scriptPath><lightweight>true</lightweight></definition><triggers/><disabled>%t</disabled></flow-definition>`, xmlEscape(job.DisplayName), parameterDefinitions(job, credentials), xmlEscape(job.JenkinsfileRepo), xmlEscape(credentials["jenkinsfile"]), xmlEscape(job.JenkinsfileBranch), xmlEscape(job.JenkinsfilePath), !job.Enabled))
}

func inlinePipelineJobXML(job Job, script string) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.1" encoding="UTF-8"?>
<flow-definition plugin="workflow-job"><actions/><description>%s</description><keepDependencies>false</keepDependencies><properties><hudson.model.ParametersDefinitionProperty><parameterDefinitions>%s</parameterDefinitions></hudson.model.ParametersDefinitionProperty></properties><definition class="org.jenkinsci.plugins.workflow.cps.CpsFlowDefinition" plugin="workflow-cps"><script>%s</script><sandbox>true</sandbox></definition><triggers/><disabled>%t</disabled></flow-definition>`, xmlEscape(job.DisplayName), parameterDefinitions(job, map[string]string{}), xmlEscape(script), !job.Enabled))
}

func parameterDefinitions(job Job, credentials map[string]string) string {
	if job.JenkinsfileMode == "generated" || job.CompactParameters {
		var choices strings.Builder
		for _, service := range job.ServiceKeys {
			fmt.Fprintf(&choices, `<string>%s</string>`, xmlEscape(service))
		}
		return fmt.Sprintf(`<hudson.model.ChoiceParameterDefinition><name>TARGET_SERVICES</name><description>%s</description><choices class="java.util.Arrays$ArrayList"><a class="string-array">%s</a></choices></hudson.model.ChoiceParameterDefinition><hudson.model.StringParameterDefinition><name>GIT_BRANCH</name><description>%s</description><defaultValue></defaultValue><trim>true</trim></hudson.model.StringParameterDefinition>`, xmlEscape("构建服务；Jenkins 每次只能选择一个服务"), choices.String(), xmlEscape("业务源码分支；留空使用所选服务登记的默认分支"))
	}
	values := buildParameters(job, BuildInput{Environment: "dev"})
	values["JENKINSFILE_CREDENTIAL_ID"] = credentials["jenkinsfile"]
	values["MANIFEST_CREDENTIAL_ID"] = credentials["manifest"]
	for _, parameter := range job.ParameterDefinitions {
		delete(values, parameter.Name)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sortStrings(keys)
	var b strings.Builder
	for _, parameter := range job.ParameterDefinitions {
		b.WriteString(parameterDefinitionXML(parameter))
	}
	for _, key := range keys {
		fmt.Fprintf(&b, `<hudson.model.StringParameterDefinition><name>%s</name><description></description><defaultValue>%s</defaultValue><trim>true</trim></hudson.model.StringParameterDefinition>`, xmlEscape(key), xmlEscape(values[key]))
	}
	return b.String()
}

func parameterDefinitionXML(parameter ParameterDefinition) string {
	name, description, value := xmlEscape(parameter.Name), xmlEscape(parameter.Description), xmlEscape(parameter.DefaultValue)
	switch parameter.Type {
	case "choice":
		var choices strings.Builder
		for _, choice := range parameter.Choices {
			fmt.Fprintf(&choices, `<string>%s</string>`, xmlEscape(choice))
		}
		return fmt.Sprintf(`<hudson.model.ChoiceParameterDefinition><name>%s</name><description>%s</description><choices class="java.util.Arrays$ArrayList"><a class="string-array">%s</a></choices></hudson.model.ChoiceParameterDefinition>`, name, description, choices.String())
	case "boolean":
		return fmt.Sprintf(`<hudson.model.BooleanParameterDefinition><name>%s</name><description>%s</description><defaultValue>%t</defaultValue></hudson.model.BooleanParameterDefinition>`, name, description, parameter.DefaultValue == "true")
	case "number":
		if description == "" {
			description = "数字参数"
		}
		return fmt.Sprintf(`<hudson.model.StringParameterDefinition><name>%s</name><description>%s</description><defaultValue>%s</defaultValue><trim>true</trim></hudson.model.StringParameterDefinition>`, name, description, value)
	default:
		return fmt.Sprintf(`<hudson.model.StringParameterDefinition><name>%s</name><description>%s</description><defaultValue>%s</defaultValue><trim>true</trim></hudson.model.StringParameterDefinition>`, name, description, value)
	}
}

func credentialXML(credential StoredCredential, secret CredentialSecret) ([]byte, error) {
	description, id := xmlEscape(credential.Description), xmlEscape(credential.ExternalID)
	switch credential.Kind {
	case "username_password", "gitlab_token":
		return []byte(fmt.Sprintf(`<com.cloudbees.plugins.credentials.impl.UsernamePasswordCredentialsImpl><scope>GLOBAL</scope><id>%s</id><description>%s</description><username>%s</username><password>%s</password></com.cloudbees.plugins.credentials.impl.UsernamePasswordCredentialsImpl>`, id, description, xmlEscape(secret.Username), xmlEscape(secret.Password))), nil
	case "secret_text":
		return []byte(fmt.Sprintf(`<org.jenkinsci.plugins.plaincredentials.impl.StringCredentialsImpl><scope>GLOBAL</scope><id>%s</id><description>%s</description><secret>%s</secret></org.jenkinsci.plugins.plaincredentials.impl.StringCredentialsImpl>`, id, description, xmlEscape(secret.SecretText))), nil
	case "ssh_private_key":
		return []byte(fmt.Sprintf(`<com.cloudbees.jenkins.plugins.sshcredentials.impl.BasicSSHUserPrivateKey><scope>GLOBAL</scope><id>%s</id><description>%s</description><username>%s</username><privateKeySource class="com.cloudbees.jenkins.plugins.sshcredentials.impl.BasicSSHUserPrivateKey$DirectEntryPrivateKeySource"><privateKey>%s</privateKey></privateKeySource><passphrase>%s</passphrase></com.cloudbees.jenkins.plugins.sshcredentials.impl.BasicSSHUserPrivateKey>`, id, description, xmlEscape(secret.Username), xmlEscape(secret.PrivateKey), xmlEscape(secret.Passphrase))), nil
	default:
		return nil, ErrInvalid
	}
}

func xmlEscape(value string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}
func mapResult(result string) string {
	switch strings.ToUpper(result) {
	case "SUCCESS":
		return "succeeded"
	case "ABORTED", "NOT_BUILT":
		return "canceled"
	case "FAILURE", "UNSTABLE":
		return "failed"
	default:
		return "unknown"
	}
}
func buildPath(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(parsed.EscapedPath(), "/") + "/"
}
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

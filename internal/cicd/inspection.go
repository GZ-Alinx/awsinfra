package cicd

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ValidateGitCredential checks the encrypted platform copy of a Jenkins Git
// credential against Git smart HTTP. It returns only protocol metadata and
// never returns, logs, or stores the decrypted username/password.
func (s *Service) ValidateGitCredential(ctx context.Context, project, credentialKey, repositoryURL string) (GitCredentialValidation, error) {
	record, err := s.store.GetCICDCredential(ctx, strings.TrimSpace(project), strings.ToLower(strings.TrimSpace(credentialKey)))
	if err != nil {
		return GitCredentialValidation{}, err
	}
	result := GitCredentialValidation{CredentialKey: record.Key, ExternalID: record.ExternalID, RepositoryURL: strings.TrimSpace(repositoryURL)}
	if record.Kind != "gitlab_token" && record.Kind != "username_password" {
		return result, fmt.Errorf("%w: credential is not an HTTPS Git credential", ErrInvalid)
	}
	payload, err := s.decrypt(credentialAAD(record.ProjectKey, record.Key), record.SecretCipher)
	if err != nil {
		return result, err
	}
	var secret CredentialSecret
	if err := json.Unmarshal(payload, &secret); err != nil {
		clear(payload)
		return result, err
	}
	clear(payload)
	defer func() { secret.Username, secret.Password = "", "" }()
	return validateGitHTTP(ctx, result, secret.Username, secret.Password)
}

// ValidateGitRepository checks whether a repository can be cloned without a
// credential. It is used by the build preflight so public repositories remain
// supported while private repositories fail before Jenkins is queued.
func (s *Service) ValidateGitRepository(ctx context.Context, repositoryURL string) (GitCredentialValidation, error) {
	result := GitCredentialValidation{RepositoryURL: strings.TrimSpace(repositoryURL)}
	return validateGitHTTP(ctx, result, "", "")
}

func validateGitHTTP(ctx context.Context, result GitCredentialValidation, username, password string) (GitCredentialValidation, error) {
	parsed, err := url.Parse(strings.TrimSuffix(result.RepositoryURL, "/") + "/info/refs?service=git-upload-pack")
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil {
		return result, fmt.Errorf("%w: repository URL is invalid", ErrInvalid)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return result, err
	}
	if username != "" || password != "" {
		request.SetBasicAuth(username, password)
	}
	request.Header.Set("Accept", "application/x-git-upload-pack-advertisement")
	originHost, originScheme := parsed.Host, parsed.Scheme
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !strings.EqualFold(next.URL.Host, originHost) || !strings.EqualFold(next.URL.Scheme, originScheme) {
			return fmt.Errorf("cross-origin Git redirect rejected")
		}
		return nil
	}}
	response, err := client.Do(request)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return result, err
	}
	result.HTTPStatus = response.StatusCode
	result.ContentType = response.Header.Get("Content-Type")
	result.SmartHTTP = response.StatusCode == http.StatusOK && strings.Contains(strings.ToLower(result.ContentType), "application/x-git-upload-pack-advertisement") && bytes.Contains(body, []byte("# service=git-upload-pack"))
	return result, nil
}

// JenkinsJobInspection is a deliberately secret-free view of an existing
// Jenkins job. It is suitable for importing manually managed jobs into the
// platform without exposing repository passwords or credential contents.
type JenkinsJobInspection struct {
	Name              string   `json:"name"`
	Class             string   `json:"class"`
	Color             string   `json:"color"`
	SCMURLs           []string `json:"scm_urls,omitempty"`
	CredentialIDs     []string `json:"credential_ids,omitempty"`
	ScriptPaths       []string `json:"script_paths,omitempty"`
	Branches          []string `json:"branches,omitempty"`
	ParameterNames    []string `json:"parameter_names,omitempty"`
	HasInlinePipeline bool     `json:"has_inline_pipeline,omitempty"`
}

// InspectJenkinsJobs reads only job metadata and SCM configuration from an
// already configured Jenkins connection. It never reads credential secrets,
// build logs or inline Pipeline source.
func (s *Service) InspectJenkinsJobs(ctx context.Context, project, connection string) ([]JenkinsJobInspection, error) {
	_, client, err := s.client(ctx, strings.TrimSpace(project), strings.TrimSpace(connection))
	if err != nil {
		return nil, err
	}
	return inspectJenkinsItems(ctx, client, "/")
}

func inspectJenkinsItems(ctx context.Context, client *jenkinsClient, basePath string) ([]JenkinsJobInspection, error) {
	response, err := client.request(ctx, http.MethodGet, strings.TrimSuffix(basePath, "/")+"/api/json?tree=jobs[name,url,color,_class]", nil, "")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var listing struct {
		Jobs []struct {
			Name  string `json:"name"`
			URL   string `json:"url"`
			Color string `json:"color"`
			Class string `json:"_class"`
		} `json:"jobs"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&listing); err != nil {
		return nil, err
	}
	result := make([]JenkinsJobInspection, 0, len(listing.Jobs))
	for _, job := range listing.Jobs {
		// Jenkins commonly advertises its configured cluster URL even when the
		// platform reaches it through a loopback-only EKS tunnel. Build the API
		// path from the server-provided job name so requests stay on the already
		// authenticated client origin and cannot follow a cross-host URL.
		path := strings.TrimSuffix(basePath, "/") + "/job/" + url.PathEscape(job.Name) + "/"
		if strings.Contains(strings.ToLower(job.Class), "folder") {
			children, err := inspectJenkinsItems(ctx, client, path)
			if err != nil {
				return nil, err
			}
			for index := range children {
				children[index].Name = job.Name + "/" + children[index].Name
			}
			result = append(result, children...)
			continue
		}
		item := JenkinsJobInspection{Name: job.Name, Class: job.Class, Color: job.Color}
		configResponse, err := client.request(ctx, http.MethodGet, path+"config.xml", nil, "")
		if err != nil {
			return nil, fmt.Errorf("read %s config: %w", job.Name, err)
		}
		if err := decodeSafeJenkinsConfig(io.LimitReader(configResponse.Body, 8<<20), &item); err != nil {
			configResponse.Body.Close()
			return nil, fmt.Errorf("decode %s config: %w", job.Name, err)
		}
		configResponse.Body.Close()
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func decodeSafeJenkinsConfig(reader io.Reader, item *JenkinsJobInspection) error {
	payload, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	// Jenkins may serialize job configuration as XML 1.1 while Go's XML
	// decoder intentionally accepts XML 1.0 only. The job fields inspected here
	// use the shared XML subset, so normalizing only the declaration is safe.
	payload = bytes.Replace(payload, []byte(`version='1.1'`), []byte(`version='1.0'`), 1)
	payload = bytes.Replace(payload, []byte(`version="1.1"`), []byte(`version="1.0"`), 1)
	decoder := xml.NewDecoder(bytes.NewReader(payload))
	stack := make([]string, 0, 16)
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch value := token.(type) {
		case xml.StartElement:
			stack = append(stack, value.Name.Local)
			if value.Name.Local == "definition" {
				for _, attribute := range value.Attr {
					if attribute.Name.Local == "class" && strings.Contains(attribute.Value, "CpsFlowDefinition") && !strings.Contains(attribute.Value, "CpsScmFlowDefinition") {
						item.HasInlinePipeline = true
					}
				}
			}
			if value.Name.Local != "url" && value.Name.Local != "credentialsId" && value.Name.Local != "scriptPath" && value.Name.Local != "name" {
				continue
			}
			var text string
			if err := decoder.DecodeElement(&text, &value); err != nil {
				return err
			}
			text = strings.TrimSpace(text)
			path := strings.Join(stack, "/")
			switch value.Name.Local {
			case "url":
				if strings.Contains(path, "userRemoteConfig") {
					item.SCMURLs = appendUniqueInspection(item.SCMURLs, text)
				}
			case "credentialsId":
				item.CredentialIDs = appendUniqueInspection(item.CredentialIDs, text)
			case "scriptPath":
				item.ScriptPaths = appendUniqueInspection(item.ScriptPaths, text)
			case "name":
				if strings.Contains(path, "ParameterDefinition") {
					item.ParameterNames = appendUniqueInspection(item.ParameterNames, text)
				} else if strings.Contains(path, "BranchSpec") {
					item.Branches = appendUniqueInspection(item.Branches, text)
				}
			}
			stack = stack[:len(stack)-1]
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
}

func appendUniqueInspection(items []string, value string) []string {
	if value == "" {
		return items
	}
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}

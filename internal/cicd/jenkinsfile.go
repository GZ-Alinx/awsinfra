package cicd

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

const maxJenkinsfileBytes = 1 << 20

var (
	jenkinsStagePattern       = regexp.MustCompile(`(?m)stage\s*\(\s*['"]([^'"\r\n]+)['"]\s*\)`)
	jenkinsCallPattern        = regexp.MustCompile(`(?s)(choice|string|booleanParam)\s*\((.*?)\)`)
	jenkinsNameArgument       = regexp.MustCompile(`(?s)\bname\s*:\s*['"]([A-Za-z_][A-Za-z0-9_]*)['"]`)
	jenkinsChoicesArgument    = regexp.MustCompile(`(?s)\bchoices\s*:\s*\[(.*?)\]`)
	jenkinsDefaultArgument    = regexp.MustCompile(`(?s)\bdefaultValue\s*:\s*(?:['"]([^'"]*)['"]|(true|false))`)
	jenkinsDescriptionArg     = regexp.MustCompile(`(?s)\bdescription\s*:\s*['"]([^'"]*)['"]`)
	jenkinsQuotedValue        = regexp.MustCompile(`['"]([^'"\r\n]+)['"]`)
	jenkinsEnvironmentValue   = regexp.MustCompile(`(?m)^\s*([A-Z][A-Z0-9_]*)\s*=\s*['"]([^'"\r\n]+)['"]\s*$`)
	jenkinsEnvironmentDynamic = regexp.MustCompile(`(?m)^\s*env\.([A-Z][A-Z0-9_]*)\s*=\s*['"]([^'"\r\n]+)['"]\s*$`)
	jenkinsRepositoryField    = regexp.MustCompile(`(?m)\brepo\s*:\s*['"](https://[^'"\s]+)['"]`)
	jenkinsCredentialLiteral  = regexp.MustCompile(`(?s)credentialsId\s*:\s*['"]([A-Za-z0-9][A-Za-z0-9_.-]{0,127})['"]|credentials\s*\(\s*['"]([A-Za-z0-9][A-Za-z0-9_.-]{0,127})['"]\s*\)`)
	jenkinsGoRuntime          = regexp.MustCompile(`(?i)\bgolang:([0-9]+(?:\.[0-9]+){1,2})`)
	jenkinsJavaRuntime        = regexp.MustCompile(`(?i)(?:jdk|java)[-_:]?([0-9]{1,2})(?:\D|$)`)
	jenkinsLineComment        = regexp.MustCompile(`(?m)^\s*//.*$`)
)

// AnalyzeJenkinsfile parses common declarative-pipeline structures without
// executing Groovy. It deliberately returns only allow-listed configuration
// values and names of suspected secret variables.
func (s *Service) AnalyzeJenkinsfile(content string) (JenkinsfileAnalysis, error) {
	return analyzeJenkinsfile(content)
}

func analyzeJenkinsfile(content string) (JenkinsfileAnalysis, error) {
	result := JenkinsfileAnalysis{Settings: map[string]string{}}
	if strings.TrimSpace(content) == "" || len(content) > maxJenkinsfileBytes || strings.IndexByte(content, 0) >= 0 {
		return result, fmt.Errorf("%w: Jenkinsfile 内容为空、过大或包含非法字符", ErrInvalid)
	}
	// Remove only full-line comments. A broad block-comment regexp is unsafe
	// here because valid Groovy strings commonly contain patterns such as /*.
	cleaned := jenkinsLineComment.ReplaceAllString(content, "")
	if !strings.Contains(cleaned, "pipeline") || !strings.Contains(cleaned, "stages") {
		return result, fmt.Errorf("%w: 未识别到 declarative pipeline/stages 结构", ErrInvalid)
	}

	result.Stages = uniqueMatches(jenkinsStagePattern, cleaned, 1, 64)
	result.Parameters = parseJenkinsParameters(cleaned)
	for _, parameter := range result.Parameters {
		if parameter.Type == "choice" && isServiceParameter(parameter.Name) {
			result.ServiceParameter = parameter.Name
			result.Services = append([]string(nil), parameter.Choices...)
			break
		}
	}

	constants := parseJenkinsConstants(cleaned)
	for name, value := range constants {
		if isSensitiveJenkinsName(name) {
			result.SensitiveVariables = append(result.SensitiveVariables, name)
			continue
		}
		if isSafeJenkinsSetting(name) {
			result.Settings[name] = value
		}
	}
	sort.Strings(result.SensitiveVariables)

	result.Repositories = detectJenkinsRepositories(cleaned, constants)
	result.CredentialReferences = detectJenkinsCredentials(cleaned, constants, result.SensitiveVariables)
	result.Language, result.RuntimeVersion = detectJenkinsLanguage(cleaned)
	result.Suggestion = buildJenkinsSuggestion(result)
	result.Warnings = buildJenkinsWarnings(cleaned, result)
	return result, nil
}

func parseJenkinsParameters(content string) []ParameterDefinition {
	result := []ParameterDefinition{}
	seen := map[string]bool{}
	for _, match := range jenkinsCallPattern.FindAllStringSubmatch(content, 128) {
		kind, body := match[1], match[2]
		nameMatch := jenkinsNameArgument.FindStringSubmatch(body)
		if len(nameMatch) < 2 || seen[nameMatch[1]] {
			continue
		}
		parameter := ParameterDefinition{Name: nameMatch[1], Type: "string"}
		if description := jenkinsDescriptionArg.FindStringSubmatch(body); len(description) > 1 {
			parameter.Description = limit(description[1], 500)
		}
		switch kind {
		case "choice":
			parameter.Type = "choice"
			if choices := jenkinsChoicesArgument.FindStringSubmatch(body); len(choices) > 1 {
				for _, item := range jenkinsQuotedValue.FindAllStringSubmatch(choices[1], 100) {
					if len(item) > 1 && !contains(parameter.Choices, item[1]) {
						parameter.Choices = append(parameter.Choices, limit(item[1], 255))
					}
				}
			}
			if len(parameter.Choices) == 0 {
				continue
			}
			parameter.DefaultValue = parameter.Choices[0]
		case "booleanParam":
			parameter.Type, parameter.DefaultValue = "boolean", "false"
			if value := jenkinsDefaultArgument.FindStringSubmatch(body); len(value) > 2 && strings.EqualFold(value[2], "true") {
				parameter.DefaultValue = "true"
			}
		default:
			if value := jenkinsDefaultArgument.FindStringSubmatch(body); len(value) > 1 {
				parameter.DefaultValue = value[1]
			}
		}
		parameter.Required = parameter.Type == "choice" || (parameter.Type == "string" && parameter.DefaultValue == "")
		result = append(result, parameter)
		seen[parameter.Name] = true
	}
	return result
}

func parseJenkinsConstants(content string) map[string]string {
	result := map[string]string{}
	for _, pattern := range []*regexp.Regexp{jenkinsEnvironmentValue, jenkinsEnvironmentDynamic} {
		for _, match := range pattern.FindAllStringSubmatch(content, 256) {
			if len(match) > 2 && len(match[2]) <= 4096 {
				result[match[1]] = match[2]
			}
		}
	}
	return result
}

func detectJenkinsRepositories(content string, constants map[string]string) []JenkinsfileRepositoryHint {
	result := []JenkinsfileRepositoryHint{}
	seen := map[string]bool{}
	appendRepository := func(role, raw, branch, path string) {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || seen[raw] {
			return
		}
		seen[raw] = true
		result = append(result, JenkinsfileRepositoryHint{Role: role, URL: raw, Branch: branch, Path: path})
	}
	manifestPath := constants["DEPLOY_YAML"]
	for name, value := range constants {
		upper := strings.ToUpper(name)
		if !strings.HasPrefix(value, "https://") || (!strings.Contains(upper, "GIT") && !strings.Contains(upper, "REPO")) {
			continue
		}
		role, branch, path := "source", "", ""
		if strings.Contains(upper, "DEPLOY") || strings.Contains(upper, "MANIFEST") {
			role, branch, path = "manifest", constants["DEPLOY_BRANCH"], manifestPath
		}
		appendRepository(role, value, branch, path)
	}
	for _, match := range jenkinsRepositoryField.FindAllStringSubmatch(content, 128) {
		if len(match) > 1 {
			appendRepository("source", match[1], "", "")
		}
	}
	return result
}

func detectJenkinsCredentials(content string, constants map[string]string, sensitive []string) []JenkinsfileCredentialReference {
	result := []JenkinsfileCredentialReference{}
	seen := map[string]bool{}
	appendReference := func(variable, externalID string, hardcoded bool) {
		key := variable + "\x00" + externalID
		if seen[key] {
			return
		}
		seen[key] = true
		result = append(result, JenkinsfileCredentialReference{Variable: variable, ExternalID: externalID, SuggestedKind: credentialKindForVariable(variable), Usage: credentialUsage(variable), Hardcoded: hardcoded})
	}
	for name, value := range constants {
		upper := strings.ToUpper(name)
		if strings.HasSuffix(upper, "_CREDENTIAL_ID") || strings.HasSuffix(upper, "_CREDENTIALS_ID") {
			appendReference(name, value, true)
		}
	}
	for _, match := range jenkinsCredentialLiteral.FindAllStringSubmatch(content, 128) {
		externalID := ""
		if len(match) > 1 {
			externalID = match[1]
		}
		if externalID == "" && len(match) > 2 {
			externalID = match[2]
		}
		if externalID != "" {
			appendReference("JENKINS_CREDENTIAL", externalID, true)
		}
	}
	for _, name := range sensitive {
		appendReference(name, "", true)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Variable < result[j].Variable })
	return result
}

func detectJenkinsLanguage(content string) (string, string) {
	if match := jenkinsGoRuntime.FindStringSubmatch(content); len(match) > 1 || strings.Contains(content, "go build") || strings.Contains(content, "go mod ") {
		version := ""
		if len(match) > 1 {
			version = match[1]
		}
		return "go", version
	}
	lower := strings.ToLower(content)
	if strings.Contains(lower, "mvn ") || strings.Contains(lower, "gradle") || strings.Contains(lower, "maven:") {
		version := ""
		if match := jenkinsJavaRuntime.FindStringSubmatch(content); len(match) > 1 {
			version = match[1]
		}
		return "java", version
	}
	return "", ""
}

func buildJenkinsSuggestion(analysis JenkinsfileAnalysis) JenkinsfileJobSuggestion {
	suggestion := JenkinsfileJobSuggestion{Language: analysis.Language, RuntimeVersion: analysis.RuntimeVersion}
	if len(analysis.Services) > 1 {
		suggestion.DisplayName, suggestion.ServiceName = "后端服务构建发布", "backend-services"
	} else if len(analysis.Services) == 1 {
		suggestion.DisplayName, suggestion.ServiceName = analysis.Services[0]+" 构建发布", analysis.Services[0]
	}
	for _, repository := range analysis.Repositories {
		if repository.Role == "manifest" {
			suggestion.ManifestRepo, suggestion.ManifestBranch, suggestion.ManifestPath = repository.URL, repository.Branch, repository.Path
			break
		}
	}
	return suggestion
}

func buildJenkinsWarnings(content string, analysis JenkinsfileAnalysis) []string {
	warnings := []string{}
	if len(analysis.SensitiveVariables) > 0 {
		warnings = append(warnings, fmt.Sprintf("发现 %d 个疑似明文敏感变量；平台未保存或回显其值，请迁移到 Jenkins Secret Text/Username Password 凭据。", len(analysis.SensitiveVariables)))
	}
	for _, reference := range analysis.CredentialReferences {
		if reference.ExternalID != "" && reference.Hardcoded {
			warnings = append(warnings, "Jenkinsfile 使用固定 Credential ID；目标 Jenkins 必须存在同名凭据，或改为读取平台注入的参数。")
			break
		}
	}
	if strings.Contains(content, "agent") && strings.Contains(content, "kubernetes") && strings.Contains(content, "yaml") {
		warnings = append(warnings, "Kubernetes Agent Pod 模板仍由 Jenkinsfile 管理；平台只负责 Jenkins 连接、凭据、Job 参数、触发与日志。")
	}
	if len(analysis.Parameters) == 0 {
		warnings = append(warnings, "未识别到标准 choice/string/booleanParam 参数，请在导入后手动补充构建参数。")
	}
	return uniqueStrings(warnings)
}

func uniqueMatches(pattern *regexp.Regexp, content string, group, max int) []string {
	result := []string{}
	for _, match := range pattern.FindAllStringSubmatch(content, max) {
		if len(match) > group && !contains(result, match[group]) {
			result = append(result, match[group])
		}
	}
	return result
}

func uniqueStrings(values []string) []string {
	result := []string{}
	for _, value := range values {
		if value != "" && !contains(result, value) {
			result = append(result, value)
		}
	}
	return result
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func isServiceParameter(name string) bool {
	return name == "server" || name == "service" || name == "SERVICE_NAME"
}

func isSensitiveJenkinsName(name string) bool {
	upper := strings.ToUpper(name)
	for _, marker := range []string{"PASSWORD", "TOKEN", "SECRET", "WEBHOOK", "PRIVATE_KEY", "ACCESS_KEY"} {
		if strings.Contains(upper, marker) && !strings.HasSuffix(upper, "_CREDENTIAL_ID") && !strings.HasSuffix(upper, "_CREDENTIALS_ID") {
			return true
		}
	}
	return false
}

func isSafeJenkinsSetting(name string) bool {
	upper := strings.ToUpper(name)
	return upper == "NAMESPACE" || upper == "GO_PROXY" || upper == "GO_PRIVATE" || strings.HasSuffix(upper, "_BRANCH") || strings.HasSuffix(upper, "_REGISTRY")
}

func credentialKindForVariable(name string) string {
	upper := strings.ToUpper(name)
	if strings.Contains(upper, "WEBHOOK") || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") {
		return "secret_text"
	}
	if strings.Contains(upper, "GIT") {
		return "gitlab_token"
	}
	return "username_password"
}

func credentialUsage(name string) string {
	upper := strings.ToUpper(name)
	switch {
	case strings.Contains(upper, "GIT"):
		return "Git 仓库"
	case strings.Contains(upper, "ACR") || strings.Contains(upper, "ECR") || strings.Contains(upper, "REGISTRY"):
		return "镜像仓库"
	case strings.Contains(upper, "WEBHOOK") || strings.Contains(upper, "LARK") || strings.Contains(upper, "TG"):
		return "通知通道"
	default:
		return "流水线"
	}
}

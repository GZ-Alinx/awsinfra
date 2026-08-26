package sensitive

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var inlineSecretKeys = map[string]struct{}{
	"password": {}, "passwd": {}, "adminpassword": {}, "rootpassword": {}, "masterpassword": {},
	"token": {}, "accesstoken": {}, "authtoken": {}, "bearertoken": {}, "refreshtoken": {},
	"apikey": {}, "clientsecret": {}, "secretaccesskey": {}, "privatekey": {},
}

var textPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)((?:password|passwd|token|secret|access[_-]?key|private[_-]?key|api[_-]?key)[^:=\n]{0,40}[=:]\s*)("[^"]*"|'[^']*'|[^\s,}]+)`),
	regexp.MustCompile(`(?i)(authorization:\s*(?:bearer\s+)?)[^\s]+`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`),
}

func Key(key string) bool {
	normalized := strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(key)))
	_, ok := inlineSecretKeys[normalized]
	return ok
}

// Sanitize removes inline secrets recursively. Secret references such as
// secret_name and secret_ref are deliberately retained.
func Sanitize(value any) []string {
	paths := make([]string, 0)
	sanitize(value, "", &paths)
	sort.Strings(paths)
	return paths
}

func Has(value any) bool {
	copyPaths := Sanitize(clone(value))
	return len(copyPaths) > 0
}

func sanitize(value any, prefix string, paths *[]string) {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			if Key(key) && nonEmpty(child) {
				delete(current, key)
				*paths = append(*paths, path)
				continue
			}
			sanitize(child, path, paths)
		}
	case []any:
		for index, child := range current {
			sanitize(child, fmt.Sprintf("%s[%d]", prefix, index), paths)
		}
	}
}

func nonEmpty(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case bool:
		return typed
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

func clone(value any) any {
	switch current := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(current))
		for key, child := range current {
			result[key] = clone(child)
		}
		return result
	case []any:
		result := make([]any, len(current))
		for index, child := range current {
			result[index] = clone(child)
		}
		return result
	default:
		return value
	}
}

func RedactText(value string) string {
	for index, pattern := range textPatterns {
		replacement := "[REDACTED]"
		if index < 2 {
			replacement = `${1}[REDACTED]`
		}
		value = pattern.ReplaceAllString(value, replacement)
	}
	return value
}

package controlplane

import (
	"encoding/json"
	"regexp"
	"strings"
)

var embeddedSecretPattern = regexp.MustCompile(`(?i)(bearer\s+)[a-z0-9._~+/=-]+|(sk-(?:proj-)?)[a-z0-9_-]{12,}|((?:api[_-]?key|password|secret|token)\s*[:=]\s*)[^\s,;]+`)

func RedactForLog(message string) string {
	return embeddedSecretPattern.ReplaceAllString(message, `${1}${2}${3}[REDACTED]`)
}

func redactPersistedJSON(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "{}"
	}
	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return string(embeddedSecretPattern.ReplaceAll([]byte(trimmed), []byte(`${1}${2}${3}[REDACTED]`)))
	}
	redactJSONValue(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func redactJSONValue(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if sensitiveJSONKey(key) {
				typed[key] = "[REDACTED]"
				continue
			}
			switch text := child.(type) {
			case string:
				typed[key] = embeddedSecretPattern.ReplaceAllString(text, `${1}${2}${3}[REDACTED]`)
			default:
				redactJSONValue(child)
			}
		}
	case []any:
		for index, child := range typed {
			if text, ok := child.(string); ok {
				typed[index] = embeddedSecretPattern.ReplaceAllString(text, `${1}${2}${3}[REDACTED]`)
				continue
			}
			redactJSONValue(child)
		}
	}
}

func sensitiveJSONKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
	for _, marker := range []string{"password", "secret", "token", "authorization", "api_key", "apikey", "credential"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

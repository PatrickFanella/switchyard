package controlplane

import (
	"os"
	"strings"
)

const (
	CorrelationBackend              = "switchyard.backend"
	CorrelationDeprecatedBackend    = "switchyard.deprecated_backend"
	CorrelationCompatibilityWarning = "switchyard.compatibility_warning"
	DeprecatedBackendAttemptedEvent = "compatibility.deprecated_backend_attempted"
)

// PreAttemptCallback durably records a resolved route immediately before its
// external attempt. Returning an error prevents that attempt.
type PreAttemptCallback func(BackendDecision) error

// BackendDecision is the resolved execution backend, rather than merely its
// configured selector. It is shared by broker and OpenCode execution paths.
type BackendDecision struct {
	Backend              string
	ConfiguredSelector   string
	DeprecatedBackend    string
	CompatibilityWarning string
}

func resolveModelBrokerBackend(model string, ollamaConfigured bool) BackendDecision {
	selector := strings.ToLower(strings.TrimSpace(os.Getenv("MODEL_BROKER_BACKEND")))
	if selector == "llama-line" {
		return BackendDecision{Backend: "llama-line", ConfiguredSelector: selector, DeprecatedBackend: "llama-line", CompatibilityWarning: "deprecated-external-backend"}
	}
	if selector == "ollama" || (ollamaConfigured && modelLooksLikeOllama(model)) {
		return BackendDecision{Backend: "ollama", ConfiguredSelector: selector}
	}
	return BackendDecision{Backend: "opencode", ConfiguredSelector: selector}
}

func resolveOpenCodeExecutorBackend() BackendDecision {
	selector := strings.ToLower(strings.TrimSpace(os.Getenv("OPENCODE_EXECUTOR_BACKEND")))
	if selector == "ocq" {
		return BackendDecision{Backend: "ocq", ConfiguredSelector: selector, DeprecatedBackend: "ocq", CompatibilityWarning: "deprecated-external-backend"}
	}
	return BackendDecision{Backend: "native", ConfiguredSelector: selector}
}

func effectiveModelBrokerSelector(selector string) string {
	switch strings.ToLower(strings.TrimSpace(selector)) {
	case "llama-line":
		return "llama-line"
	case "ollama":
		return "ollama"
	default:
		return "native"
	}
}

func effectiveOpenCodeExecutorSelector(selector string) string {
	if strings.EqualFold(strings.TrimSpace(selector), "ocq") {
		return "ocq"
	}
	return "native"
}

func modelLooksLikeOllama(model string) bool {
	name := strings.ToLower(strings.TrimSpace(trimModelProviderPrefix(model, "ollama")))
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "ollama/") {
		return true
	}
	for _, prefix := range strings.Split(firstNonEmpty(os.Getenv("MODEL_BROKER_OLLAMA_MODELS"), "llama,qwen,mistral,codellama,nomic-embed,bge,deepseek,gemma,phi,hermes"), ",") {
		if prefix = strings.ToLower(strings.TrimSpace(prefix)); prefix != "" && strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func (d BackendDecision) CorrelationIDs() map[string]string {
	correlation := map[string]string{CorrelationBackend: d.Backend}
	if d.DeprecatedBackend != "" {
		correlation[CorrelationDeprecatedBackend] = d.DeprecatedBackend
		correlation[CorrelationCompatibilityWarning] = d.CompatibilityWarning
	}
	return correlation
}

// resolveModelExecutionRoute resolves the complete generation route once. A
// native broker delegates to the OpenCode executor, whose OCQ compatibility
// selector is part of the externally observable route.
func resolveModelExecutionRoute(model string, ollamaConfigured bool) BackendDecision {
	broker := resolveModelBrokerBackend(model, ollamaConfigured)
	if broker.Backend != "opencode" {
		return broker
	}
	executor := resolveOpenCodeExecutorBackend()
	if executor.Backend == "ocq" {
		executor.ConfiguredSelector = broker.ConfiguredSelector
		return executor
	}
	return broker
}

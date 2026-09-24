package controlplane

import (
	"net/url"
	"os"
	"strings"
)

// CorrelationTargets publishes the operator-approved external destinations the
// dashboard may link correlation IDs to. Only scheme, host, and an optional
// path prefix are exposed; credentials, queries, and fragments are stripped.
// The dashboard treats these as an allowlist: an external URL outside these
// origins is rendered as copyable text instead of a link.
type CorrelationTargets struct {
	// GiteaBaseURL is the sanitized GITEA_BASE_URL used for issue, pull
	// request, CI, and commit links.
	GiteaBaseURL string `json:"gitea_base_url,omitempty"`
	// TraceUIBaseURL is the sanitized SWITCHYARD_TRACE_UI_URL. It may contain a
	// literal {trace_id} placeholder; otherwise the trace ID is appended as a
	// path segment.
	TraceUIBaseURL string `json:"trace_ui_base_url,omitempty"`
}

const traceIDPlaceholder = "{trace_id}"

// NewEnvCorrelationTargets reads the configured Gitea and trace UI bases and
// returns the sanitized targets together with warnings for rejected values.
func NewEnvCorrelationTargets() (CorrelationTargets, []string) {
	var warnings []string
	targets := CorrelationTargets{}
	if raw := strings.TrimSpace(os.Getenv("GITEA_BASE_URL")); raw != "" {
		base, ok := sanitizeCorrelationBaseURL(raw)
		if !ok {
			warnings = append(warnings, "GITEA_BASE_URL is not an absolute http(s) URL; Gitea correlation links are disabled until it is fixed")
		}
		targets.GiteaBaseURL = base
	}
	if raw := strings.TrimSpace(os.Getenv("SWITCHYARD_TRACE_UI_URL")); raw != "" {
		base, ok := sanitizeCorrelationBaseURL(raw)
		if !ok {
			warnings = append(warnings, "SWITCHYARD_TRACE_UI_URL is not an absolute http(s) URL; trace correlation links are disabled until it is fixed")
		}
		targets.TraceUIBaseURL = base
	}
	return targets, warnings
}

// sanitizeCorrelationBaseURL keeps scheme, host, and path of an absolute
// http(s) URL and drops userinfo, query, and fragment. A literal {trace_id}
// placeholder in the path is preserved verbatim.
func sanitizeCorrelationBaseURL(raw string) (string, bool) {
	placeholder := strings.Contains(raw, traceIDPlaceholder)
	parsed, err := url.Parse(strings.ReplaceAll(raw, traceIDPlaceholder, "__trace_id__"))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Hostname() == "" {
		return "", false
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	if placeholder {
		path = strings.ReplaceAll(path, "__trace_id__", traceIDPlaceholder)
	}
	return parsed.Scheme + "://" + strings.ToLower(parsed.Host) + path, true
}

package controlplane

import (
	"os"
	"strings"
)

const OpenPilotAutonomyFull = "full"

type OperatorSafetyStatus struct {
	PublicURL        string   `json:"public_url,omitempty"`
	ReverseProxyAuth bool     `json:"reverse_proxy_auth"`
	OpenPilotMode    string   `json:"open_pilot_autonomy_mode"`
	Warnings         []string `json:"warnings"`
}

func openPilotAutonomyMode() string {
	return strings.ToLower(strings.TrimSpace(os.Getenv("OPEN_PILOT_AUTONOMY_MODE")))
}

func openPilotAutonomyFullEnabled() bool {
	return openPilotAutonomyMode() == OpenPilotAutonomyFull
}

func reverseProxyAuthEnabled() bool {
	return envBool("SWITCHYARD_REVERSE_PROXY_AUTH")
}

func envBool(name string) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return value == "1" || value == "true" || value == "yes" || value == "required"
}

func NewEnvOperatorSafetyStatus() OperatorSafetyStatus {
	status := OperatorSafetyStatus{
		PublicURL:        strings.TrimSpace(os.Getenv("SWITCHYARD_PUBLIC_URL")),
		ReverseProxyAuth: reverseProxyAuthEnabled(),
		OpenPilotMode:    openPilotAutonomyMode(),
		Warnings:         []string{},
	}
	if status.OpenPilotMode == "" {
		status.OpenPilotMode = "manual"
	}
	if openPilotAutonomyFullEnabled() && !status.ReverseProxyAuth {
		status.Warnings = append(status.Warnings, "OPEN_PILOT_AUTONOMY_MODE=full requires reverse-proxy authentication before exposing Switchyard publicly; set SWITCHYARD_REVERSE_PROXY_AUTH=true after configuring the proxy")
	}
	if openPilotAutonomyFullEnabled() && strings.TrimSpace(os.Getenv("GITEA_BASE_URL")) == "" {
		status.Warnings = append(status.Warnings, "OPEN_PILOT_AUTONOMY_MODE=full enables Open Pilot Gitea gates by default (PR creation, merge, issue close, and CI checks); configure GITEA_BASE_URL or explicitly disable the Gitea-backed modes")
	}
	if status.PublicURL != "" && !status.ReverseProxyAuth {
		status.Warnings = append(status.Warnings, "SWITCHYARD_PUBLIC_URL is set but SWITCHYARD_REVERSE_PROXY_AUTH is not enabled")
	}
	return status
}

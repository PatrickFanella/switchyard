package controlplane

import (
	"errors"
	"os"
	"strings"
	"unicode"
)

const reverseProxyTokenMinimumBytes = 32

var publicReverseProxyTokens = map[string]struct{}{
	"replace-with-at-least-32-random-bytes":    {},
	"replace-me-with-at-least-32-random-bytes": {},
}

// ReverseProxyAuthConfig is the validated runtime proxy-auth configuration.
type ReverseProxyAuthConfig struct {
	Enabled bool
	Token   string
}

// ReverseProxyAuthFromEnv rejects unsafe proxy-auth configuration without
// including the configured secret in its error.
func ReverseProxyAuthFromEnv() (ReverseProxyAuthConfig, error) {
	enabled := strings.EqualFold(strings.TrimSpace(os.Getenv("SWITCHYARD_REVERSE_PROXY_AUTH")), "true")
	if !enabled {
		return ReverseProxyAuthConfig{}, nil
	}
	token := os.Getenv("SWITCHYARD_REVERSE_PROXY_TOKEN")
	if err := validateReverseProxyToken(token); err != nil {
		return ReverseProxyAuthConfig{}, err
	}
	return ReverseProxyAuthConfig{Enabled: true, Token: token}, nil
}

func validateReverseProxyToken(token string) error {
	if len([]byte(token)) < reverseProxyTokenMinimumBytes {
		return errors.New("SWITCHYARD_REVERSE_PROXY_TOKEN must be a non-placeholder secret of at least 32 bytes without control characters")
	}
	if _, public := publicReverseProxyTokens[strings.ToLower(token)]; public {
		return errors.New("SWITCHYARD_REVERSE_PROXY_TOKEN must be a non-placeholder secret of at least 32 bytes without control characters")
	}
	for _, character := range token {
		if unicode.IsControl(character) {
			return errors.New("SWITCHYARD_REVERSE_PROXY_TOKEN must be a non-placeholder secret of at least 32 bytes without control characters")
		}
	}
	return nil
}

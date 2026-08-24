package controlplane

import (
	"net/http"
	"strings"
	"testing"
)

func TestReverseProxyAuthFromEnvRejectsUnsafeTokensWithoutLeakingThem(t *testing.T) {
	for _, token := range []string{
		"",
		"short",
		"replace-with-at-least-32-random-bytes",
		"replace-me-with-at-least-32-random-bytes",
		"valid-looking-token-at-least-32-bytes\nsecret",
		"valid-looking-token-at-least-32-bytes\u007fsecret",
	} {
		t.Run(strings.ReplaceAll(token, "\n", "newline"), func(t *testing.T) {
			t.Setenv("SWITCHYARD_REVERSE_PROXY_AUTH", "true")
			t.Setenv("SWITCHYARD_REVERSE_PROXY_TOKEN", token)
			_, err := ReverseProxyAuthFromEnv()
			if err == nil {
				t.Fatal("ReverseProxyAuthFromEnv() error = nil")
			}
			if token != "" && strings.Contains(err.Error(), token) {
				t.Fatal("error leaked proxy token")
			}
		})
	}
}

func TestProxyIdentityRequiresSingleCanonicalASCIIValue(t *testing.T) {
	valid := strings.Repeat("a", 256)
	for _, test := range []struct {
		name   string
		header http.Header
		want   string
	}{
		{name: "username", header: http.Header{"Remote-User": {"user.name+ops@example.test"}}, want: "user.name+ops@example.test"},
		{name: "matching headers", header: http.Header{"Remote-User": {valid}, "X-Forwarded-User": {valid}}, want: valid},
		{name: "leading whitespace", header: http.Header{"Remote-User": {" operator"}}},
		{name: "trailing whitespace", header: http.Header{"Remote-User": {"operator "}}},
		{name: "bidi", header: http.Header{"Remote-User": {"operator\u202eevil"}}},
		{name: "non ASCII", header: http.Header{"Remote-User": {"operátor"}}},
		{name: "too long", header: http.Header{"Remote-User": {valid + "a"}}},
		{name: "duplicate remote", header: http.Header{"Remote-User": {"operator", "attacker"}}},
		{name: "duplicate forwarded", header: http.Header{"X-Forwarded-User": {"operator", "attacker"}}},
		{name: "conflict", header: http.Header{"Remote-User": {"operator"}, "X-Forwarded-User": {"attacker"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := proxyIdentity(test.header)
			if ok != (test.want != "") || got != test.want {
				t.Fatalf("proxyIdentity() = %q, %v; want %q", got, ok, test.want)
			}
		})
	}
}

func TestReverseProxyAuthFromEnvAcceptsValidTokenAndDisabledAuth(t *testing.T) {
	const token = "random-proxy-hop-token-at-least-32-bytes"
	t.Setenv("SWITCHYARD_REVERSE_PROXY_AUTH", "true")
	t.Setenv("SWITCHYARD_REVERSE_PROXY_TOKEN", token)
	config, err := ReverseProxyAuthFromEnv()
	if err != nil || !config.Enabled || config.Token != token {
		t.Fatalf("ReverseProxyAuthFromEnv() = %#v, %v", config, err)
	}
	t.Setenv("SWITCHYARD_REVERSE_PROXY_AUTH", "false")
	t.Setenv("SWITCHYARD_REVERSE_PROXY_TOKEN", "bad\nvalue")
	config, err = ReverseProxyAuthFromEnv()
	if err != nil || config.Enabled {
		t.Fatalf("disabled ReverseProxyAuthFromEnv() = %#v, %v", config, err)
	}
}

package controlplane

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestOpenPilotGitCredentialEnvUsesAskpass(t *testing.T) {
	t.Setenv("GITEA_TOKEN", "secret-token")
	t.Setenv("GITEA_GIT_USERNAME", "git-user")

	env, cleanup, err := openPilotGitCredentialEnv()
	if err != nil {
		t.Fatalf("openPilotGitCredentialEnv() error = %v", err)
	}
	defer cleanup()
	if len(env) == 0 {
		t.Fatal("expected credential environment")
	}

	var askpass string
	for _, item := range env {
		if strings.HasPrefix(item, "GIT_ASKPASS=") {
			askpass = strings.TrimPrefix(item, "GIT_ASKPASS=")
		}
	}
	if askpass == "" {
		t.Fatalf("GIT_ASKPASS missing from env: %v", env)
	}
	if _, err := os.Stat(askpass); err != nil {
		t.Fatalf("askpass script missing: %v", err)
	}

	username := exec.Command(askpass, "Username for https://git.example.test")
	username.Env = env
	usernameOut, err := username.Output()
	if err != nil {
		t.Fatalf("username askpass failed: %v", err)
	}
	if got := strings.TrimSpace(string(usernameOut)); got != "git-user" {
		t.Fatalf("username = %q", got)
	}

	password := exec.Command(askpass, "Password for https://git.example.test")
	password.Env = env
	passwordOut, err := password.Output()
	if err != nil {
		t.Fatalf("password askpass failed: %v", err)
	}
	if got := strings.TrimSpace(string(passwordOut)); got != "secret-token" {
		t.Fatalf("password = %q", got)
	}
}

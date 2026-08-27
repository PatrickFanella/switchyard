package controlplane

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/switchyard/internal/gitworktree"
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

func TestOpenPilotGitCredentialsPushOverVerifiedHTTPS(t *testing.T) {
	const (
		username = "switchyard-test-user"
		token    = "synthetic-open-pilot-token"
		branch   = "credential-regression"
	)
	_, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required")
	}
	gitExecPath := strings.TrimSpace(runGitOutput(t, "", "--exec-path"))
	gitHTTPBackend := filepath.Join(gitExecPath, "git-http-backend")
	if _, err := os.Stat(gitHTTPBackend); err != nil {
		t.Skipf("git http-backend is required: %v", err)
	}

	remoteRoot := t.TempDir()
	remote := filepath.Join(remoteRoot, "owner", "repo.git")
	if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
		t.Fatal(err)
	}
	runWorktreeTestGit(t, remoteRoot, "init", "--bare", remote)
	runWorktreeTestGit(t, remoteRoot, "--git-dir="+remote, "config", "http.receivepack", "true")

	backend := &cgi.Handler{
		Path: gitHTTPBackend,
		Root: "/",
		Env:  []string{"GIT_PROJECT_ROOT=" + remoteRoot, "GIT_HTTP_EXPORT_ALL=1"},
	}
	var unauthenticated, authenticated, rejected atomic.Int32
	var authenticatedBeforeChallenge atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotToken, ok := r.BasicAuth()
		if !ok {
			unauthenticated.Add(1)
			w.Header().Set("WWW-Authenticate", `Basic realm="switchyard-test"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if gotUser != username || gotToken != token {
			rejected.Add(1)
			w.Header().Set("WWW-Authenticate", `Basic realm="switchyard-test"`)
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}
		if unauthenticated.Load() == 0 {
			authenticatedBeforeChallenge.Store(true)
		}
		authenticated.Add(1)
		backend.ServeHTTP(w, r)
	})
	server, caPEM := newGitHTTPSTestServer(t, handler)
	defer server.Close()
	caPath := filepath.Join(t.TempDir(), "git-test-ca.pem")
	if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	repo := initWorktreeTestRepoWithoutOrigin(t)
	worktree := filepath.Join(t.TempDir(), "worktree")
	runWorktreeTestGit(t, repo, "worktree", "add", "-b", branch, worktree, "main")
	origin := server.URL + "/owner/repo.git"
	runWorktreeTestGit(t, repo, "remote", "add", "origin", origin)
	if err := os.WriteFile(filepath.Join(worktree, "credential-test.txt"), []byte("authenticated push\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runWorktreeTestGit(t, worktree, "add", "credential-test.txt")
	runWorktreeTestGit(t, worktree, "commit", "-m", "test authenticated push")
	wantSHA := strings.TrimSpace(runGitOutput(t, worktree, "rev-parse", "HEAD"))

	identity, err := gitworktree.ValidatePath(t.Context(), gitworktree.Expected{
		WorktreePath:  worktree,
		CommonDir:     filepath.Join(repo, ".git"),
		Branch:        branch,
		Repo:          "owner/repo",
		RequireOrigin: true,
		OriginBaseURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer identity.Close()
	t.Setenv("GITEA_TOKEN", token)
	t.Setenv("GITEA_GIT_USERNAME", username)
	credentialEnv, cleanup, err := openPilotGitCredentialEnv()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	askpass := envValue(credentialEnv, "GIT_ASKPASS")
	if info, err := os.Stat(askpass); err != nil {
		t.Fatalf("stat askpass: %v", err)
	} else if info.Mode().Perm() != 0o700 {
		t.Fatalf("askpass mode = %o, want 700", info.Mode().Perm())
	}

	cmd := identity.Command(t.Context(), append(credentialEnv, "GIT_SSL_CAINFO="+caPath), "push", identity.OriginURL, "HEAD:"+branch)
	for name, value := range map[string]string{"argv": strings.Join(cmd.Args, " "), "URL": identity.OriginURL} {
		if strings.Contains(value, username) || strings.Contains(value, token) {
			t.Fatalf("credentials exposed in %s: %q", name, value)
		}
	}
	output, pushErr := cmd.CombinedOutput()
	cleanup()
	if _, err := os.Stat(askpass); !os.IsNotExist(err) {
		t.Fatalf("askpass remains after cleanup: %v", err)
	}
	if strings.Contains(string(output), username) || strings.Contains(string(output), token) {
		t.Fatalf("credentials exposed in Git output: %q", output)
	}
	if pushErr != nil {
		t.Fatalf("authenticated push: %v: %s", pushErr, output)
	}
	if unauthenticated.Load() == 0 || authenticated.Load() == 0 || rejected.Load() != 0 || authenticatedBeforeChallenge.Load() {
		t.Fatalf("auth requests: unauthenticated=%d authenticated=%d rejected=%d authenticated_before_challenge=%t", unauthenticated.Load(), authenticated.Load(), rejected.Load(), authenticatedBeforeChallenge.Load())
	}
	if got := strings.TrimSpace(runGitOutput(t, remoteRoot, "--git-dir="+remote, "rev-parse", "refs/heads/"+branch)); got != wantSHA {
		t.Fatalf("remote ref SHA = %q, want %q", got, wantSHA)
	}
}

func newGitHTTPSTestServer(t *testing.T, handler http.Handler) (*httptest.Server, []byte) {
	t.Helper()
	now := time.Now()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Switchyard Git test CA"},
		NotBefore:    now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: leafKeyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	return server, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}

func envValue(env []string, name string) string {
	prefix := name + "="
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, prefix); ok {
			return value
		}
	}
	return ""
}

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	if dir != "" {
		command.Dir = dir
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

package gitworktree

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type Expected struct {
	WorktreePath     string
	CommonDir        string
	Branch           string
	Repo             string
	RequireOrigin    bool
	OriginBaseURL    string
	AllowLocalOrigin bool
}

type Identity struct {
	GitDir           string
	CommonDir        string
	OriginURL        string
	Worktree         *os.File
	AllowLocalRemote bool
}

func (i Identity) Close() error {
	if i.Worktree == nil {
		return nil
	}
	return i.Worktree.Close()
}

func (i Identity) Command(ctx context.Context, extraEnv []string, args ...string) *exec.Cmd {
	overrides := SafeCommandArgs(i.AllowLocalRemote)
	if i.OriginURL != "" {
		origin := i.OriginURL
		overrides = append(overrides,
			"-c", "credential.helper=",
			"-c", "credential."+origin+".helper=",
			"-c", "core.askPass=",
			"-c", "core.hooksPath=/dev/null",
			"-c", "core.sshCommand=",
			"-c", "http.extraHeader=",
			"-c", "http."+origin+".extraHeader=",
			"-c", "http.proxy=",
			"-c", "http."+origin+".proxy=",
			"-c", "url."+origin+".insteadOf="+origin,
			"-c", "url."+origin+".pushInsteadOf="+origin,
		)
	}
	args = append(overrides, args...)
	command := exec.CommandContext(ctx, "git", args...)
	command.ExtraFiles = []*os.File{i.Worktree}
	command.Env = i.Environment(extraEnv)
	return command
}

// SafeCommandArgs disables local configuration that can execute commands,
// redirect network traffic, supply credentials, or weaken TLS verification.
func SafeCommandArgs(allowLocalRemote bool) []string {
	protocolFile := "never"
	if allowLocalRemote {
		protocolFile = "always"
	}
	return []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=",
		"-c", "core.gitProxy=",
		"-c", "core.askPass=",
		"-c", "core.sshCommand=",
		"-c", "core.pager=cat",
		"-c", "diff.external=",
		"-c", "interactive.diffFilter=",
		"-c", "commit.gpgSign=false",
		"-c", "tag.gpgSign=false",
		"-c", "push.gpgSign=false",
		"-c", "credential.helper=",
		"-c", "http.extraHeader=",
		"-c", "http.proxy=",
		"-c", "http.sslVerify=true",
		"-c", "http.sslCert=",
		"-c", "http.sslKey=",
		"-c", "protocol.allow=never",
		"-c", "protocol.https.allow=always",
		"-c", "protocol.file.allow=" + protocolFile,
		"-c", "protocol.ext.allow=never",
	}
}

func (i Identity) Environment(extra []string) []string {
	env := append(SanitizedEnvironment(nil),
		"GIT_DIR="+i.GitDir,
		"GIT_COMMON_DIR="+i.CommonDir,
		"GIT_WORK_TREE=/proc/self/fd/3",
	)
	return append(env, extra...)
}

func SanitizedEnvironment(extra []string) []string {
	return append(sanitizedGitEnv(), extra...)
}

func ValidatePath(ctx context.Context, expected Expected) (Identity, error) {
	if !filepath.IsAbs(expected.WorktreePath) || filepath.Clean(expected.WorktreePath) != expected.WorktreePath {
		return Identity{}, errors.New("worktree path must be an exact absolute lexical path")
	}
	canonical, err := filepath.EvalSymlinks(expected.WorktreePath)
	if err != nil || canonical != expected.WorktreePath {
		return Identity{}, errors.New("worktree lexical path does not match its canonical path")
	}
	fd, err := openDirectoryWithoutSymlinks(expected.WorktreePath)
	if err != nil {
		return Identity{}, fmt.Errorf("open worktree: %w", err)
	}
	directory := os.NewFile(uintptr(fd), expected.WorktreePath)
	defer directory.Close()
	return ValidateOpened(ctx, directory, expected)
}

func ValidateOpened(ctx context.Context, directory *os.File, expected Expected) (Identity, error) {
	if directory == nil {
		return Identity{}, errors.New("worktree directory is required")
	}
	if !filepath.IsAbs(expected.WorktreePath) || filepath.Clean(expected.WorktreePath) != expected.WorktreePath {
		return Identity{}, errors.New("worktree path must be an exact absolute lexical path")
	}
	heldFD, err := unix.Dup(int(directory.Fd()))
	if err != nil {
		return Identity{}, fmt.Errorf("hold worktree directory: %w", err)
	}
	held := os.NewFile(uintptr(heldFD), expected.WorktreePath)
	valid := false
	defer func() {
		if !valid {
			_ = held.Close()
		}
	}()
	common, err := canonicalDirectory(expected.CommonDir)
	if err != nil {
		return Identity{}, fmt.Errorf("validate Git common directory: %w", err)
	}

	gitFileFD, err := unix.Openat(int(directory.Fd()), ".git", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return Identity{}, errors.New("worktree .git must be a regular non-symlink file")
	}
	gitFile := os.NewFile(uintptr(gitFileFD), ".git")
	defer gitFile.Close()
	info, err := gitFile.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return Identity{}, errors.New("worktree .git must be a regular non-symlink file")
	}
	contents, err := os.ReadFile("/proc/self/fd/" + fmt.Sprint(gitFile.Fd()))
	if err != nil {
		return Identity{}, fmt.Errorf("read worktree .git: %w", err)
	}
	gitdir, err := parseGitdir(string(contents))
	if err != nil {
		return Identity{}, err
	}
	gitdir, err = filepath.EvalSymlinks(gitdir)
	if err != nil {
		return Identity{}, fmt.Errorf("canonicalize linked Git directory: %w", err)
	}
	if err := requireBelow(filepath.Join(common, "worktrees"), gitdir); err != nil {
		return Identity{}, errors.New("linked Git directory is outside the configured common directory")
	}
	if err := requireGitdirBacklink(gitdir, expected.WorktreePath); err != nil {
		return Identity{}, err
	}
	if err := RejectUnsafeLocalConfig(ctx, common); err != nil {
		return Identity{}, err
	}

	actualCommon, err := gitOutput(ctx, "--git-dir="+gitdir, "rev-parse", "--git-common-dir")
	if err != nil {
		return Identity{}, fmt.Errorf("resolve linked Git common directory: %w", err)
	}
	if !filepath.IsAbs(actualCommon) {
		actualCommon = filepath.Join(gitdir, actualCommon)
	}
	actualCommon, err = filepath.EvalSymlinks(actualCommon)
	if err != nil || actualCommon != common {
		return Identity{}, errors.New("linked Git common directory does not match configured common directory")
	}

	registered, err := gitOutput(ctx, "--git-dir="+common, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return Identity{}, fmt.Errorf("list registered Git worktrees: %w", err)
	}
	if !registeredPath(registered, expected.WorktreePath) {
		return Identity{}, errors.New("worktree path is not registered in the configured Git common directory")
	}
	if expected.Branch != "" {
		branch, branchErr := gitOutput(ctx, "--git-dir="+gitdir, "symbolic-ref", "--quiet", "--short", "HEAD")
		if branchErr != nil || branch != expected.Branch {
			return Identity{}, fmt.Errorf("worktree branch %q does not match expected branch %q", branch, expected.Branch)
		}
	}
	originURL := ""
	if expected.RequireOrigin {
		origin, originErr := gitOutput(ctx, "--git-dir="+common, "config", "--get", "remote.origin.url")
		if originErr != nil || (!matchingOrigin(origin, expected.OriginBaseURL, expected.Repo) && !(expected.AllowLocalOrigin && localOrigin(origin))) {
			return Identity{}, errors.New("source origin does not match requested repository")
		}
		originURL = strings.TrimSpace(origin)
	}
	valid = true
	return Identity{GitDir: gitdir, CommonDir: common, OriginURL: originURL, Worktree: held, AllowLocalRemote: expected.AllowLocalOrigin}, nil
}

func localOrigin(origin string) bool {
	value := strings.TrimSpace(origin)
	if value == "" {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if parsed.Scheme == "file" {
		return parsed.Host == "" && filepath.IsAbs(parsed.Path)
	}
	return parsed.Scheme == "" && filepath.IsAbs(value)
}

func requireGitdirBacklink(gitdir, worktreePath string) error {
	fd, err := openDirectoryWithoutSymlinks(gitdir)
	if err != nil {
		return errors.New("open linked Git directory without symlinks")
	}
	defer unix.Close(fd)
	backlinkFD, err := unix.Openat(fd, "gitdir", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return errors.New("linked Git directory backlink must be a regular non-symlink file")
	}
	backlink := os.NewFile(uintptr(backlinkFD), "gitdir")
	defer backlink.Close()
	info, err := backlink.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("linked Git directory backlink must be a regular non-symlink file")
	}
	contents, err := os.ReadFile("/proc/self/fd/" + fmt.Sprint(backlink.Fd()))
	if err != nil {
		return errors.New("read linked Git directory backlink")
	}
	expected := filepath.Join(worktreePath, ".git")
	if string(contents) != expected+"\n" && string(contents) != expected {
		return errors.New("linked Git directory backlink does not match expected worktree")
	}
	return nil
}

func canonicalDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("path must be an exact absolute path")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("path must be a non-symlink directory")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return "", errors.New("path must match its canonical path")
	}
	return canonical, nil
}

func parseGitdir(contents string) (string, error) {
	line := strings.TrimSpace(contents)
	value, ok := strings.CutPrefix(line, "gitdir: ")
	if !ok || value == "" || strings.ContainsAny(value, "\r\n") || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return "", errors.New("worktree .git contains an invalid linked Git directory")
	}
	return value, nil
}

func requireBelow(root, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("path is outside root")
	}
	return nil
}

func registeredPath(output, expected string) bool {
	for _, field := range strings.Split(output, "\x00") {
		if path, ok := strings.CutPrefix(field, "worktree "); ok && path == expected {
			return true
		}
	}
	return false
}

func gitOutput(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append(SafeCommandArgs(false), args...)...)
	command.Env = sanitizedGitEnv()
	output, err := command.CombinedOutput()
	value := strings.TrimSpace(string(output))
	if err != nil {
		if value == "" {
			value = err.Error()
		}
		return "", errors.New(value)
	}
	return value, nil
}

func sanitizedGitEnv() []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "GIT_") {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"GIT_PAGER=cat",
		"PAGER=cat",
	)
}

func openDirectoryWithoutSymlinks(path string) (int, error) {
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

func RejectUnsafeLocalConfig(ctx context.Context, common string) error {
	command := exec.CommandContext(ctx, "git", "--git-dir="+common, "config", "--local", "--name-only", "--get-regexp", ".*")
	command.Env = sanitizedGitEnv()
	output, err := command.CombinedOutput()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 && len(output) == 0 {
			return nil
		}
		return errors.New("unable to inspect local Git configuration safely")
	}
	for _, line := range strings.Split(string(output), "\n") {
		key := strings.ToLower(strings.TrimSpace(line))
		if unsafeLocalConfigKey(key) {
			return errors.New("local Git redirect or credential configuration is not allowed")
		}
	}
	return nil
}

func unsafeLocalConfigKey(key string) bool {
	if key == "core.sshcommand" || key == "core.askpass" || key == "core.hookspath" || key == "core.fsmonitor" || key == "core.gitproxy" || key == "core.worktree" || key == "core.attributesfile" || key == "core.pager" || key == "diff.external" || key == "interactive.difffilter" || key == "http.extraheader" || key == "http.proxy" || key == "http.curloptresolve" || key == "http.followredirects" || key == "http.sslverify" || key == "http.sslcert" || key == "http.sslkey" || key == "http.sslcainfo" || key == "http.sslcapath" || key == "http.sslbackend" {
		return true
	}
	if strings.HasPrefix(key, "credential.") || strings.HasPrefix(key, "url.") || strings.HasPrefix(key, "include.") || strings.HasPrefix(key, "includeif.") || strings.HasPrefix(key, "protocol.") || strings.HasPrefix(key, "gpg.") {
		return true
	}
	if strings.HasPrefix(key, "http.") && (strings.HasSuffix(key, ".extraheader") || strings.HasSuffix(key, ".proxy") || strings.HasSuffix(key, ".curloptresolve") || strings.HasSuffix(key, ".sslverify") || strings.HasSuffix(key, ".sslcert") || strings.HasSuffix(key, ".sslkey") || strings.HasSuffix(key, ".sslcainfo") || strings.HasSuffix(key, ".sslcapath") || strings.HasSuffix(key, ".sslbackend") || strings.HasSuffix(key, ".cookiefile")) {
		return true
	}
	if strings.HasPrefix(key, "remote.") && (strings.HasSuffix(key, ".pushurl") || strings.HasSuffix(key, ".proxy") || strings.HasSuffix(key, ".receivepack") || strings.HasSuffix(key, ".uploadpack") || strings.HasSuffix(key, ".vcs")) {
		return true
	}
	if strings.HasPrefix(key, "filter.") || strings.HasPrefix(key, "merge.") && strings.HasSuffix(key, ".driver") || strings.HasPrefix(key, "diff.") && (strings.HasSuffix(key, ".command") || strings.HasSuffix(key, ".textconv")) {
		return true
	}
	if strings.HasPrefix(key, "submodule.") && strings.HasSuffix(key, ".update") {
		return true
	}
	return false
}

func normalizeRepo(repo string) string {
	parts := strings.Split(strings.TrimSuffix(strings.Trim(strings.TrimSpace(repo), "/"), ".git"), "/")
	if len(parts) < 2 {
		return ""
	}
	return strings.ToLower(parts[len(parts)-2] + "/" + parts[len(parts)-1])
}

func matchingOrigin(origin, baseURL, repo string) bool {
	originURL, originErr := url.Parse(strings.TrimSpace(origin))
	base, baseErr := url.Parse(strings.TrimSpace(baseURL))
	if originErr != nil || baseErr != nil || originURL.Scheme == "" || base.Scheme == "" || originURL.Host == "" || base.Host == "" {
		return false
	}
	if originURL.Scheme != "https" || base.Scheme != "https" || originURL.User != nil || base.User != nil || originURL.RawQuery != "" || base.RawQuery != "" || originURL.Fragment != "" || base.Fragment != "" {
		return false
	}
	expectedRepo := normalizeRepo(repo)
	if expectedRepo == "" {
		return false
	}
	expectedPath := strings.TrimRight(base.EscapedPath(), "/") + "/" + expectedRepo
	originPath := strings.TrimSuffix(strings.TrimRight(originURL.EscapedPath(), "/"), ".git")
	return strings.EqualFold(originURL.Scheme, base.Scheme) && strings.EqualFold(originURL.Host, base.Host) && strings.EqualFold(originPath, expectedPath)
}

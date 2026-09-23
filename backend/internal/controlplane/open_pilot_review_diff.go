package controlplane

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const openPilotReviewDiffLimit = 64 << 10

// Review the committed PR patch, not an agent's account of its edits. Identity
// checks and immutable SHAs keep resumed reviews on the same published candidate.
func openPilotCommittedReviewDiff(ctx context.Context, plan OpenPilotWorktreePlan, pr GiteaPullRequest) (string, error) {
	if !openPilotFullGitSHA(pr.Head.SHA) || !openPilotFullGitSHA(pr.Base.SHA) {
		return "", errors.New("AI review requires full pull request base and head commits")
	}
	identity, err := validateOpenPilotWorktreeIdentity(ctx, plan.Repo, plan.RepoPath, plan.WorktreePath, plan.Branch, false)
	if err != nil {
		return "", fmt.Errorf("validate AI review worktree: %w", err)
	}
	defer identity.Close()
	head, err := identityGitOutput(ctx, identity, nil, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(head) != pr.Head.SHA {
		return "", errors.New("AI review worktree HEAD does not match the published pull request")
	}
	status, err := identityGitOutput(ctx, identity, nil, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(status) != "" {
		return "", errors.New("AI review worktree changed after commit")
	}
	output := &openPilotReviewBuffer{limit: openPilotReviewDiffLimit}
	diagnostic := &openPilotReviewBuffer{limit: 4096}
	command := identity.Command(ctx, nil, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--unified=5", pr.Base.SHA+"..."+pr.Head.SHA, "--")
	command.Stdout = output
	command.Stderr = diagnostic
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("read committed AI review patch: %w", err)
	}
	if output.exceeded {
		return "", errors.New("committed patch exceeds the 64 KiB automatic review limit; operator review is required")
	}
	if !utf8.Valid(output.Bytes()) {
		return "", errors.New("committed patch is not UTF-8; operator review is required")
	}
	patch := output.String()
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "Binary files ") {
			return "", errors.New("binary changes require operator review")
		}
	}
	if strings.TrimSpace(patch) == "" {
		return "", errors.New("committed pull request patch is empty")
	}
	return fmt.Sprintf("Base commit: %s\nHead commit: %s\n\n%s", pr.Base.SHA, pr.Head.SHA, patch), nil
}

type openPilotReviewBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (b *openPilotReviewBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.buffer.Len()
	if n > remaining {
		b.exceeded = true
		p = p[:remaining]
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}

func (b *openPilotReviewBuffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *openPilotReviewBuffer) String() string { return b.buffer.String() }

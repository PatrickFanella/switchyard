package controlplane

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type OpenPilotAssigner struct {
	store    Store
	runner   *Runner
	issues   GiteaIssueReader
	repos    []string
	labels   []string
	assignee string
	now      func() time.Time
}

func NewEnvOpenPilotAssigner(store Store, runner *Runner, issues GiteaIssueReader) *OpenPilotAssigner {
	repos := splitCSV(os.Getenv("OPEN_PILOT_POLL_REPOS"))
	for index := range repos {
		repos[index] = strings.ToLower(strings.TrimSpace(repos[index]))
	}
	return &OpenPilotAssigner{store: store, runner: runner, issues: issues, repos: repos, labels: splitCSV(firstNonEmpty(os.Getenv("OPEN_PILOT_REQUIRED_LABELS"), "ready-for-agent,agent:queued")), assignee: strings.TrimSpace(os.Getenv("OPEN_PILOT_ASSIGNEE")), now: time.Now}
}

func (a *OpenPilotAssigner) Poll(ctx context.Context) (int, error) {
	if a == nil || a.store == nil || a.runner == nil || a.issues == nil || len(a.repos) == 0 {
		return 0, fmt.Errorf("OPEN_PILOT_POLL_REPOS is not configured")
	}
	assigned := 0
	repos := a.repos
	if len(repos) == 1 && repos[0] == "*" {
		if a.runner.openPilotGateError != nil {
			return 0, a.runner.openPilotGateError
		}
		if a.runner.openPilotGate == nil || a.runner.openPilotGate.registry == nil {
			return 0, ErrInvalid
		}
		discovery, ok := a.issues.(GiteaRepositoryLister)
		if !ok {
			return 0, fmt.Errorf("the Gitea issue reader does not support repository discovery")
		}
		var err error
		repos, err = discovery.ListOpenPilotRepositories(ctx)
		if err != nil {
			return 0, err
		}
	}
	for _, repo := range repos {
		if a.runner.openPilotGateError != nil {
			return assigned, a.runner.openPilotGateError
		}
		target, err := a.runner.openPilotGate.ResolvePollRepository(repo)
		if err != nil {
			if len(a.repos) == 1 && a.repos[0] == "*" {
				continue
			}
			return assigned, err
		}
		repo = target.RepositorySlug
		listed, err := a.issues.ListIssues(ctx, repo, a.labels)
		if err != nil {
			return assigned, err
		}
		if a.assignee != "" {
			assignedIssues, err := a.issues.ListAssignedIssues(ctx, repo, a.assignee)
			if err != nil {
				return assigned, err
			}
			listed = append(listed, assignedIssues...)
		}
		seen := map[int]bool{}
		for _, candidate := range listed {
			if seen[candidate.Number] {
				continue
			}
			seen[candidate.Number] = true
			issueNumber := strconv.Itoa(candidate.Number)
			if _, err := a.runner.openPilotGate.Resolve(repo, issueNumber); err != nil {
				continue
			}
			issue, fetchErr := a.issues.FetchIssue(ctx, repo, issueNumber)
			if fetchErr != nil {
				return assigned, fetchErr
			}
			if issue.State != "open" || issue.ID < 1 || !openPilotIssueEligible(issue, a.labels, a.assignee) {
				continue
			}
			inputs := map[string]string{"repo": repo, "issue": issueNumber, "issue_id": strconv.Itoa(issue.ID), "issue_verified_at": a.now().UTC().Format(time.RFC3339Nano), "issue_verified_labels": strings.Join(issue.Labels, ",")}
			if command := openPilotIssueTestCommand(issue.Body); command != "" {
				inputs["test_command"] = command
			}
			prepared, prepareErr := a.runner.prepareRunInput(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), TriggerID: "gitea-open-pilot-poll", Inputs: inputs})
			if prepareErr != nil {
				return assigned, prepareErr
			}
			if _, _, claimErr := a.store.ClaimAndCreateOpenPilotInitialRun(prepared.Inputs["repo"], prepared.Inputs["issue"], prepared); claimErr == nil {
				assigned++
			} else if !errors.Is(claimErr, ErrInvalid) && !errors.Is(claimErr, ErrAlreadyClaimed) && !errors.Is(claimErr, ErrConflict) {
				return assigned, claimErr
			}
		}
	}
	return assigned, nil
}

// Assignment is an opt-in alternative to the existing label-based queue.
func openPilotIssueEligible(issue GiteaIssue, labels []string, assignee string) bool {
	return hasAllLabels(issue.Labels, labels) || openPilotIssueAssignedTo(issue, assignee)
}

func openPilotIssueAssignedTo(issue GiteaIssue, assignee string) bool {
	if strings.TrimSpace(assignee) == "" {
		return false
	}
	for _, user := range issue.Assignees {
		if strings.EqualFold(strings.TrimSpace(user.Login), strings.TrimSpace(assignee)) {
			return true
		}
	}
	return false
}

func hasAllLabels(actual, required []string) bool {
	set := map[string]struct{}{}
	for _, label := range actual {
		set[strings.ToLower(strings.TrimSpace(label))] = struct{}{}
	}
	for _, label := range required {
		if _, ok := set[strings.ToLower(strings.TrimSpace(label))]; !ok {
			return false
		}
	}
	return true
}

func openPilotIssueTestCommand(body string) string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	inside := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			inside = strings.EqualFold(strings.TrimSpace(strings.TrimLeft(trimmed, "#")), "Test Command")
			continue
		}
		if !inside || trimmed == "" || strings.HasPrefix(trimmed, "```") {
			continue
		}
		return strings.TrimPrefix(trimmed, "- ")
	}
	return ""
}

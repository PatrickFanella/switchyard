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
	store  Store
	runner *Runner
	issues GiteaIssueReader
	repos  []string
	labels []string
	now    func() time.Time
}

func NewEnvOpenPilotAssigner(store Store, runner *Runner, issues GiteaIssueReader) *OpenPilotAssigner {
	repos := splitCSV(os.Getenv("OPEN_PILOT_POLL_REPOS"))
	for index := range repos {
		repos[index] = strings.ToLower(strings.TrimSpace(repos[index]))
	}
	return &OpenPilotAssigner{store: store, runner: runner, issues: issues, repos: repos, labels: splitCSV(firstNonEmpty(os.Getenv("OPEN_PILOT_REQUIRED_LABELS"), "ready-for-agent,agent:queued")), now: time.Now}
}

func (a *OpenPilotAssigner) Poll(ctx context.Context) (int, error) {
	if a == nil || a.store == nil || a.runner == nil || a.issues == nil || len(a.repos) == 0 {
		return 0, fmt.Errorf("OPEN_PILOT_POLL_REPOS is not configured")
	}
	assigned := 0
	for _, repo := range a.repos {
		if a.runner.openPilotGateError != nil {
			return assigned, a.runner.openPilotGateError
		}
		target, err := a.runner.openPilotGate.Resolve(repo, "1")
		if err != nil {
			return assigned, err
		}
		repo = target.RepositorySlug
		listed, err := a.issues.ListIssues(ctx, repo, a.labels)
		if err != nil {
			return assigned, err
		}
		for _, candidate := range listed {
			issueNumber := strconv.Itoa(candidate.Number)
			issue, fetchErr := a.issues.FetchIssue(ctx, repo, issueNumber)
			if fetchErr != nil {
				return assigned, fetchErr
			}
			if issue.State != "open" || issue.ID < 1 || !hasAllLabels(issue.Labels, a.labels) {
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

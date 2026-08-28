package controlplane

import (
	"sort"
)

const maxOpenPilotAuditStates = 100

type OpenPilotAuditReference struct {
	Repo  string `json:"repo"`
	Issue string `json:"issue"`
	Phase string `json:"phase"`
	Kind  string `json:"kind"`
}

type OpenPilotCanonicalCollision struct {
	Repo   string `json:"canonical_repo"`
	Issue  string `json:"canonical_issue"`
	States int    `json:"states"`
}

type OpenPilotAuditRepository struct {
	ID                 string   `json:"id"`
	Slug               string   `json:"slug"`
	Aliases            []string `json:"aliases"`
	ExecutorGeneration string   `json:"executor_generation"`
}

type OpenPilotRepositoryAuditReport struct {
	Registry             []OpenPilotAuditRepository    `json:"registry"`
	Collisions           []OpenPilotCanonicalCollision `json:"shadow_canonical_collisions"`
	UnknownRepositories  []OpenPilotAuditReference     `json:"unknown_repositories"`
	UnparseableIssues    []OpenPilotAuditReference     `json:"unparseable_issues"`
	AffinityProblems     []OpenPilotAuditReference     `json:"affinity_null_or_mismatch"`
	ActiveOrResumable    []OpenPilotAuditReference     `json:"active_or_resumable_references"`
	SingletonAdmissionOK bool                          `json:"singleton_admission_safe"`
	Truncated            bool                          `json:"truncated"`
}

func buildOpenPilotRepositoryAudit(gate *OpenPilotAdmissionGate, states []OpenPilotIssueState) OpenPilotRepositoryAuditReport {
	report := OpenPilotRepositoryAuditReport{Registry: []OpenPilotAuditRepository{}, Collisions: []OpenPilotCanonicalCollision{}, UnknownRepositories: []OpenPilotAuditReference{}, UnparseableIssues: []OpenPilotAuditReference{}, AffinityProblems: []OpenPilotAuditReference{}, ActiveOrResumable: []OpenPilotAuditReference{}}
	if gate != nil && gate.registry != nil {
		for _, repository := range gate.registry.Repositories() {
			report.Registry = append(report.Registry, OpenPilotAuditRepository{ID: repository.ID, Slug: repository.Slug, Aliases: append([]string(nil), repository.Aliases...), ExecutorGeneration: repository.ExecutorGeneration})
		}
	}
	if len(states) > maxOpenPilotAuditStates {
		states, report.Truncated = states[:maxOpenPilotAuditStates], true
	}
	counts := map[string]int{}
	canonical := map[string]OpenPilotTarget{}
	for _, state := range states {
		ref := OpenPilotAuditReference{Repo: state.Repo, Issue: state.Issue, Phase: string(state.Phase)}
		target, resolveErr := gate.Resolve(state.Repo, state.Issue)
		if _, err := ParseOpenPilotIssue(state.Issue); err != nil {
			ref.Kind = "unparseable_issue"
			report.UnparseableIssues = append(report.UnparseableIssues, ref)
		} else if resolveErr != nil {
			ref.Kind = "unknown_repository"
			report.UnknownRepositories = append(report.UnknownRepositories, ref)
		}
		if _, err := gate.ValidateState(state); err != nil || state.RepositoryID == "" {
			ref.Kind = "affinity_null_or_mismatch"
			report.AffinityProblems = append(report.AffinityProblems, ref)
		}
		if isOpenPilotActivePhase(state.Phase) || isOpenPilotResumablePhase(state.Phase) {
			ref.Kind = "active_or_resumable"
			report.ActiveOrResumable = append(report.ActiveOrResumable, ref)
		}
		if resolveErr == nil {
			key := target.RepositoryID + "\x00" + target.Issue
			counts[key]++
			canonical[key] = target
		}
	}
	keys := make([]string, 0, len(counts))
	for key, count := range counts {
		if count > 1 {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		target := canonical[key]
		report.Collisions = append(report.Collisions, OpenPilotCanonicalCollision{Repo: target.RepositorySlug, Issue: target.Issue, States: counts[key]})
	}
	report.SingletonAdmissionOK = !report.Truncated && len(report.Collisions) == 0 && len(report.UnknownRepositories) == 0 && len(report.UnparseableIssues) == 0 && len(report.AffinityProblems) == 0
	return report
}

package controlplane

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestOpenPilotPhaseAllowsV1Transitions(t *testing.T) {
	allowed := []struct{ from, to OpenPilotPhase }{
		{OpenPilotPhaseRequested, OpenPilotPhaseValidated},
		{OpenPilotPhaseValidated, OpenPilotPhaseBranchPrepared},
		{OpenPilotPhaseBranchPrepared, OpenPilotPhaseAgentCompleted},
		{OpenPilotPhaseAgentCompleted, OpenPilotPhaseVerified},
		{OpenPilotPhaseVerified, OpenPilotPhasePushed},
		{OpenPilotPhasePushed, OpenPilotPhasePRCreated},
		{OpenPilotPhasePRCreated, OpenPilotPhaseAIReviewed},
		{OpenPilotPhaseAIReviewed, OpenPilotPhaseCIPassed},
		{OpenPilotPhaseCIPassed, OpenPilotPhaseMergeReady},
		{OpenPilotPhaseMergeReady, OpenPilotPhaseMerged},
		{OpenPilotPhaseMerged, OpenPilotPhaseIssueClosed},
		{OpenPilotPhaseRequested, OpenPilotPhaseFailed},
		{OpenPilotPhaseRequested, OpenPilotPhasePaused},
		{OpenPilotPhaseMerged, OpenPilotPhaseFailed},
		{OpenPilotPhaseMergeReady, OpenPilotPhasePaused},
	}
	for _, tt := range allowed {
		if err := ValidateOpenPilotTransition(tt.from, tt.to); err != nil {
			t.Fatalf("%s -> %s rejected: %v", tt.from, tt.to, err)
		}
	}
}

func TestOpenPilotPhaseRejectsSkippingMerge(t *testing.T) {
	err := ValidateOpenPilotTransition(OpenPilotPhasePRCreated, OpenPilotPhaseIssueClosed)
	if err == nil || !strings.Contains(err.Error(), "invalid open pilot transition") {
		t.Fatalf("expected invalid transition, got %v", err)
	}
}

func TestOpenPilotPhaseRejectsActivePhaseSkips(t *testing.T) {
	for _, tt := range []struct{ from, to OpenPilotPhase }{
		{OpenPilotPhaseRequested, OpenPilotPhaseCIPassed},
		{OpenPilotPhaseValidated, OpenPilotPhaseMerged},
		{OpenPilotPhasePRCreated, OpenPilotPhaseMerged},
		{OpenPilotPhaseAIReviewed, OpenPilotPhaseMerged},
	} {
		if err := ValidateOpenPilotTransition(tt.from, tt.to); err == nil {
			t.Fatalf("expected active phase skip %s -> %s to be rejected", tt.from, tt.to)
		}
	}
}

func TestOpenPilotPhaseAllowsResumeTransitionsToActivePhases(t *testing.T) {
	for _, tt := range []struct{ from, to OpenPilotPhase }{
		{OpenPilotPhaseFailed, OpenPilotPhaseRequested},
		{OpenPilotPhasePaused, OpenPilotPhaseAgentCompleted},
	} {
		if err := ValidateOpenPilotTransition(tt.from, tt.to); err != nil {
			t.Fatalf("expected resume transition %s -> %s, got %v", tt.from, tt.to, err)
		}
	}
}

func TestOpenPilotPhaseRejectsInvalidTerminalTransitions(t *testing.T) {
	for _, tt := range []struct{ from, to OpenPilotPhase }{
		{"", OpenPilotPhaseFailed},
		{"", OpenPilotPhasePaused},
		{OpenPilotPhaseFailed, OpenPilotPhaseIssueClosed},
		{OpenPilotPhasePaused, OpenPilotPhaseIssueClosed},
		{OpenPilotPhaseIssueClosed, OpenPilotPhaseRequested},
	} {
		if err := ValidateOpenPilotTransition(tt.from, tt.to); err == nil {
			t.Fatalf("expected rejection for %s -> %s", tt.from, tt.to)
		}
	}
}

func TestMemoryStoreOpenPilotIssueStateTransitionValidation(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	saved, err := store.TransitionOpenPilotIssueState("OWNER/REPO", "123", OpenPilotPhaseRequested)
	if err != nil {
		t.Fatalf("TransitionOpenPilotIssueState() error = %v", err)
	}
	if saved.Phase != OpenPilotPhaseRequested {
		t.Fatalf("saved = %+v", saved)
	}
	if _, err := store.TransitionOpenPilotIssueState("OWNER/REPO", "123", OpenPilotPhaseIssueClosed); err == nil {
		t.Fatal("expected invalid skipped transition to be rejected")
	}
	if _, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "456", Phase: OpenPilotPhase("mystery")}); err == nil {
		t.Fatal("expected unknown phase upsert to be rejected")
	}
}

func TestMemoryStoreOpenPilotIssueStateRejectsDirectAdvancedCreation(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	for _, phase := range []OpenPilotPhase{OpenPilotPhasePushed, OpenPilotPhasePRCreated, OpenPilotPhaseMerged, OpenPilotPhaseIssueClosed} {
		if _, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "direct-" + string(phase), Phase: phase}); err == nil {
			t.Fatalf("expected direct creation of %s to be rejected", phase)
		}
	}
}

func TestMemoryStoreOpenPilotIssueStateUpsertValidation(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	t.Run("default max repair attempts", func(t *testing.T) {
		saved, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "123", Phase: OpenPilotPhaseRequested, MaxRepairAttempts: 0})
		if err != nil {
			t.Fatalf("UpsertOpenPilotIssueState() error = %v", err)
		}
		if saved.MaxRepairAttempts != openPilotDefaultMaxRepairAttempts {
			t.Fatalf("saved.MaxRepairAttempts = %d, want %d", saved.MaxRepairAttempts, openPilotDefaultMaxRepairAttempts)
		}
	})
	t.Run("rejects repair counter overflow", func(t *testing.T) {
		if _, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "124", Phase: OpenPilotPhaseRequested, RepairAttempts: 3, MaxRepairAttempts: 2}); err == nil {
			t.Fatal("expected repair counter overflow to be rejected")
		}
	})
	t.Run("rejects issue closed jump", func(t *testing.T) {
		if _, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "125", Phase: OpenPilotPhaseRequested}); err != nil {
			t.Fatalf("seed upsert error = %v", err)
		}
		if _, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "125", Phase: OpenPilotPhaseIssueClosed}); err == nil {
			t.Fatal("expected requested -> issue_closed upsert to be rejected")
		}
	})
}

func TestMemoryStoreOpenPilotIssueStateCRUDDeepCopy(t *testing.T) {
	now := time.Unix(100, 0)
	store := NewStore(func() time.Time { return now })
	input := OpenPilotIssueState{
		Repo:              "OWNER/REPO",
		Issue:             "123",
		Phase:             OpenPilotPhaseRequested,
		GateStatus:        OpenPilotGateStatus{"ai_review": "pending"},
		ResumeInputs:      map[string]string{"branch": "open-pilot/issue-123"},
		RepairAttempts:    1,
		MaxRepairAttempts: 0,
	}
	saved, err := store.UpsertOpenPilotIssueState(input)
	if err != nil {
		t.Fatalf("UpsertOpenPilotIssueState() error = %v", err)
	}
	if saved.MaxRepairAttempts != openPilotDefaultMaxRepairAttempts {
		t.Fatalf("saved.MaxRepairAttempts = %d, want %d", saved.MaxRepairAttempts, openPilotDefaultMaxRepairAttempts)
	}
	if saved.ID == "" || saved.CreatedAt.IsZero() || saved.UpdatedAt.IsZero() {
		t.Fatalf("saved = %+v", saved)
	}
	input.GateStatus["ai_review"] = "approved"
	input.ResumeInputs["branch"] = "mutated"
	fetched, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if fetched.ID != saved.ID || fetched.GateStatus["ai_review"] != "pending" || fetched.ResumeInputs["branch"] != "open-pilot/issue-123" {
		t.Fatalf("fetched = %+v", fetched)
	}
	fetched.GateStatus["ai_review"] = "changed"
	fetched.ResumeInputs["branch"] = "changed"
	listed := store.ListOpenPilotIssueStates()
	if len(listed) != 1 {
		t.Fatalf("len(listed) = %d", len(listed))
	}
	if listed[0].GateStatus["ai_review"] != "pending" || listed[0].ResumeInputs["branch"] != "open-pilot/issue-123" {
		t.Fatalf("listed = %+v", listed[0])
	}
	listed[0].CreatedAt = time.Unix(200, 0)
	listed[0].Phase = OpenPilotPhaseValidated
	updated, err := store.UpsertOpenPilotIssueState(listed[0])
	if err != nil {
		t.Fatalf("UpsertOpenPilotIssueState() update error = %v", err)
	}
	if updated.ID != saved.ID || updated.Phase != OpenPilotPhaseValidated || !updated.CreatedAt.Equal(saved.CreatedAt) {
		t.Fatalf("updated = %+v", updated)
	}
}

func TestScanOpenPilotIssueStateRowReturnsJSONErrors(t *testing.T) {
	_, err := scanOpenPilotIssueStateRow(fakeOpenPilotStateRow{})
	if err == nil {
		t.Fatal("expected JSON unmarshal error")
	}
}

type fakeOpenPilotStateRow struct{}

func (fakeOpenPilotStateRow) Scan(dest ...any) error {
	*dest[0].(*string) = "opi-1"
	*dest[1].(*string) = "OWNER/REPO"
	*dest[2].(*string) = "123"
	*dest[3].(*OpenPilotPhase) = OpenPilotPhaseRequested
	*dest[4].(*sql.NullString) = sql.NullString{}
	*dest[5].(*sql.NullString) = sql.NullString{}
	*dest[6].(*sql.NullString) = sql.NullString{}
	*dest[7].(*sql.NullString) = sql.NullString{}
	*dest[8].(*sql.NullString) = sql.NullString{}
	*dest[9].(*sql.NullString) = sql.NullString{}
	*dest[10].(*sql.NullInt64) = sql.NullInt64{}
	*dest[11].(*sql.NullInt64) = sql.NullInt64{}
	*dest[12].(*sql.NullString) = sql.NullString{}
	*dest[13].(*bool) = false
	*dest[14].(*[]byte) = []byte(`{"broken":`)
	*dest[15].(*[]byte) = []byte(`{"branch":"open-pilot/issue-123"}`)
	*dest[16].(*int) = 0
	*dest[17].(*int) = 2
	*dest[18].(*sql.NullString) = sql.NullString{}
	*dest[19].(*time.Time) = time.Unix(100, 0).UTC()
	*dest[20].(*time.Time) = time.Unix(101, 0).UTC()
	return nil
}

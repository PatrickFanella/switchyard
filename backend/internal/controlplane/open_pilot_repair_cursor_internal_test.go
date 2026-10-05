package controlplane

import "testing"

func TestInterruptedCIRepairKeepsAgentResumeCursor(t *testing.T) {
	store, parent, _ := seedOpenPilotCIRepairParent(t)
	handoff, err := store.HandleOpenPilotCIFailure(testOpenPilotCIRepairRequest(parent, "worker-1", "verify failed"))
	if err != nil || handoff.ChildRun == nil {
		t.Fatalf("handoff = %+v, %v", handoff, err)
	}
	child := *handoff.ChildRun
	if _, err := store.FinishRun(child.ID, RunTimedOut, "coding deadline exceeded"); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.ReconcileOpenPilotCIRepairTerminal(child.ID, configuredOpenPilotAdmissionGate(t)); err != nil || !changed {
		t.Fatalf("reconciliation = %v, %v", changed, err)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "1")
	if err != nil {
		t.Fatal(err)
	}
	if state.LastStepName != "open-pilot.agent-work" || state.ResumeInputs["retry_from_step"] != "open-pilot.agent-work" || state.ResumeInputs[openPilotCIRepairCountKey] != "1" {
		t.Fatalf("interrupted repair lost its coding cursor or repair budget: %+v", state)
	}
}

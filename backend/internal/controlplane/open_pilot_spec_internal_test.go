package controlplane

import "testing"

func TestOpenPilotIssueSpecAcceptsVerificationVocabulary(t *testing.T) {
	for _, tc := range []struct {
		body  string
		ready bool
	}{
		{"Acceptance criteria\nTest Command: make verify", true},
		{"Acceptance criteria\nVerification: compare links and release records", true},
		{"Acceptance: focused component regressions and full verify pass", true},
		{"Acceptance criteria only", false},
		{"Verification: make verify", false},
		{"Please fix this", false},
	} {
		if got := openPilotIssueSpecReady(tc.body); got != tc.ready {
			t.Errorf("specification %q: ready=%v, want %v", tc.body, got, tc.ready)
		}
	}
}

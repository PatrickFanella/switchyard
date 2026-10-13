package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

func (a *API) guardOpenPilotOverseer(ctx context.Context) error {
	if a.workerPool == nil || a.scheduler == nil {
		return fmt.Errorf("scheduler and worker pool must be enabled")
	}
	workers, scheduler := a.workerPool.Status(), a.scheduler.Status()
	if !workers.Running || !scheduler.Running || workers.Paused || scheduler.Paused {
		return fmt.Errorf("runtime is stopped or intentionally paused")
	}
	if len(NewEnvOperatorSafetyStatus().Warnings) > 0 {
		return fmt.Errorf("operator safety warnings prevent overseer action")
	}
	if a.modelBroker.Status(ctx).Status != "healthy" || a.openCode.Status(ctx).Status != "healthy" {
		return fmt.Errorf("model broker or executor unavailable")
	}
	return nil
}

func (a *API) openPilotOverseerState(w http.ResponseWriter, r *http.Request) {
	state, err := a.overseer.State(r.Context())
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (a *API) configureOpenPilotOverseer(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ExpectedRevision *int64                  `json:"expected_revision"`
		Config           OpenPilotOverseerConfig `json:"config"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	if input.ExpectedRevision == nil {
		writeControlError(w, ErrInvalid)
		return
	}
	state, err := a.overseer.Configure(r.Context(), *input.ExpectedRevision, input.Config, requestActor(r))
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (a *API) checkOpenPilotOverseer(w http.ResponseWriter, r *http.Request) {
	// An operator preview never adds labels, retries, merges, or changes policy.
	if err := a.overseer.Tick(r.Context(), true, true); err != nil {
		writeControlError(w, err)
		return
	}
	state, err := a.overseer.State(r.Context())
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (a *API) releaseOpenPilotOverseerPending(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ExpectedRevision *int64 `json:"expected_revision"`
		ActionID         string `json:"action_id"`
		Reason           string `json:"reason"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	if input.ExpectedRevision == nil {
		writeControlError(w, ErrInvalid)
		return
	}
	state, err := a.overseer.ReleaseReviewedPending(r.Context(), *input.ExpectedRevision, input.ActionID, input.Reason, requestActor(r))
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

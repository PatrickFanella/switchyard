package controlplane

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// Agent-profile routes live only on the control listener. Mutations require
// the admin role (enforced by controlPlaneAuth for every non-read request) and
// every activation or rollback is validated against current consumers and
// written to the durable audit trail with the acting identity.

func (a *API) agentProfiles(w http.ResponseWriter, r *http.Request) {
	identities, err := a.store.ListAgentProfileIdentities()
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": agentProfilesOrEmpty(a.effectiveAgentProfiles().ListProfiles()), "identities": agentProfileIdentitiesOrEmpty(identities)})
}

func (a *API) agentProfile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	profile, identity, err := a.resolveAgentProfile(id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": profile, "identity": identity})
}

func (a *API) agentProfileVersions(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	versions, err := a.store.ListAgentProfileVersions(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			if _, ok := DefaultAgentProfileRegistry().Profile(id); ok {
				writeJSON(w, http.StatusOK, map[string]any{"profile_versions": []AgentProfileVersion{}})
				return
			}
		}
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile_versions": versions})
}

func (a *API) agentProfileAudit(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
	entries, err := a.store.ListAgentProfileAudit(id, limit)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			if _, ok := DefaultAgentProfileRegistry().Profile(id); ok {
				writeJSON(w, http.StatusOK, map[string]any{"audit": []AgentProfileAuditEntry{}})
				return
			}
		}
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit": entries})
}

func (a *API) saveAgentProfileDraft(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input AgentProfileDraftInput
	if err := decoder.Decode(&input); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	version, err := a.store.SaveAgentProfileDraft(id, input, requestActor(r))
	if err != nil {
		writeControlError(w, err)
		return
	}
	identity, err := a.store.AgentProfileIdentity(id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile_version": version, "identity": identity})
}

func (a *API) activateAgentProfile(w http.ResponseWriter, r *http.Request) {
	a.transitionAgentProfile(w, r, false)
}

func (a *API) rollbackAgentProfile(w http.ResponseWriter, r *http.Request) {
	a.transitionAgentProfile(w, r, true)
}

func (a *API) transitionAgentProfile(w http.ResponseWriter, r *http.Request, rollback bool) {
	id := strings.TrimSpace(r.PathValue("id"))
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input AgentProfileActivationInput
	if err := decoder.Decode(&input); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	if input.Version < 1 {
		writeControlError(w, ErrInvalid)
		return
	}
	if len(strings.TrimSpace(input.Reason)) > maxAgentProfileNoteLength {
		writeControlError(w, ErrInvalid)
		return
	}
	target, err := a.store.AgentProfileVersion(id, input.Version)
	if err != nil {
		writeControlError(w, err)
		return
	}
	if err := validateAgentProfileConsumers(a.store, target.AgentProfile); err != nil {
		writeControlError(w, err)
		return
	}
	actor := requestActor(r)
	var version AgentProfileVersion
	if rollback {
		version, err = a.store.RollbackAgentProfile(id, input, actor)
	} else {
		version, err = a.store.ActivateAgentProfileVersion(id, input, actor)
	}
	if err != nil {
		writeControlError(w, err)
		return
	}
	profile, identity, err := a.resolveAgentProfile(id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	body := map[string]any{"profile": profile, "profile_version": version, "identity": identity}
	if rollback {
		body["rolled_back_from_version"] = input.Version
	}
	writeJSON(w, http.StatusOK, body)
}

// resolveAgentProfile returns the effective policy and identity. Profiles that
// exist only as shipped defaults report a synthesized identity with no
// persisted versions so clients can distinguish them from unknown IDs.
func (a *API) resolveAgentProfile(id string) (AgentProfile, AgentProfileIdentity, error) {
	profile, ok := a.effectiveAgentProfiles().Profile(id)
	if !ok {
		return AgentProfile{}, AgentProfileIdentity{}, ErrNotFound
	}
	identity, err := a.store.AgentProfileIdentity(id)
	if errors.Is(err, ErrNotFound) {
		_, static := DefaultAgentProfileRegistry().Profile(id)
		return profile, AgentProfileIdentity{ID: id, StaticDefault: static}, nil
	}
	if err != nil {
		return AgentProfile{}, AgentProfileIdentity{}, err
	}
	return profile, identity, nil
}

func (a *API) effectiveAgentProfiles() AgentProfileRegistry {
	if a.runner != nil {
		return a.runner.effectiveAgentProfiles()
	}
	return NewStoreBackedAgentProfileRegistry(a.store, DefaultAgentProfileRegistry())
}

func agentProfileIdentitiesOrEmpty(items []AgentProfileIdentity) []AgentProfileIdentity {
	if items == nil {
		return []AgentProfileIdentity{}
	}
	return items
}

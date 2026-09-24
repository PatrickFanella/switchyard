package controlplane

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// memoryLockedAgentProfileRegistry resolves profiles while the caller already
// holds s.mu, so store operations can validate references without deadlocking.
type memoryLockedAgentProfileRegistry struct {
	store *MemoryStore
}

func (r memoryLockedAgentProfileRegistry) Profile(id string) (AgentProfile, bool) {
	if version, ok := r.store.activeAgentProfileLocked(id); ok {
		return version.AgentProfile, true
	}
	return DefaultAgentProfileRegistry().Profile(id)
}

func (r memoryLockedAgentProfileRegistry) ListProfiles() []AgentProfile {
	return mergeAgentProfiles(r.store.listActiveAgentProfilesLocked(), DefaultAgentProfileRegistry().ListProfiles())
}

func (s *MemoryStore) activeAgentProfileLocked(id string) (AgentProfileVersion, bool) {
	identity, ok := s.agentProfiles[id]
	if !ok || identity.ActiveVersion == 0 {
		return AgentProfileVersion{}, false
	}
	version, ok := s.agentProfileVersions[id][identity.ActiveVersion]
	if !ok {
		return AgentProfileVersion{}, false
	}
	return cloneAgentProfileVersion(version), true
}

func (s *MemoryStore) listActiveAgentProfilesLocked() []AgentProfileVersion {
	out := make([]AgentProfileVersion, 0, len(s.agentProfiles))
	for id := range s.agentProfiles {
		if version, ok := s.activeAgentProfileLocked(id); ok {
			out = append(out, version)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *MemoryStore) AgentProfileIdentity(id string) (AgentProfileIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	identity, ok := s.agentProfiles[strings.TrimSpace(id)]
	if !ok {
		return AgentProfileIdentity{}, ErrNotFound
	}
	return identity, nil
}

func (s *MemoryStore) ListAgentProfileIdentities() ([]AgentProfileIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]AgentProfileIdentity, 0, len(s.agentProfiles))
	for _, identity := range s.agentProfiles {
		out = append(out, identity)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *MemoryStore) ActiveAgentProfile(id string) (AgentProfileVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	version, ok := s.activeAgentProfileLocked(strings.TrimSpace(id))
	if !ok {
		return AgentProfileVersion{}, ErrNotFound
	}
	return version, nil
}

func (s *MemoryStore) ListActiveAgentProfiles() ([]AgentProfileVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listActiveAgentProfilesLocked(), nil
}

func (s *MemoryStore) AgentProfileVersion(id string, version int) (AgentProfileVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stored, ok := s.agentProfileVersions[strings.TrimSpace(id)][version]
	if !ok {
		return AgentProfileVersion{}, ErrNotFound
	}
	return cloneAgentProfileVersion(stored), nil
}

func (s *MemoryStore) ListAgentProfileVersions(id string) ([]AgentProfileVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	versions, ok := s.agentProfileVersions[strings.TrimSpace(id)]
	if !ok {
		return nil, ErrNotFound
	}
	out := make([]AgentProfileVersion, 0, len(versions))
	for _, version := range versions {
		out = append(out, cloneAgentProfileVersion(version))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

func (s *MemoryStore) SaveAgentProfileDraft(id string, input AgentProfileDraftInput, actor string) (AgentProfileVersion, error) {
	profile, err := normalizeAgentProfileDraft(id, input)
	if err != nil {
		return AgentProfileVersion{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	identity, exists := s.agentProfiles[profile.ID]
	if !exists {
		identity = AgentProfileIdentity{ID: profile.ID, CreatedAt: now}
	}
	if input.ExpectedVersion != identity.LatestVersion {
		return AgentProfileVersion{}, fmt.Errorf("%w: agent profile %q latest version is %d, expected_version was %d", ErrConflict, profile.ID, identity.LatestVersion, input.ExpectedVersion)
	}
	static, hasStatic := DefaultAgentProfileRegistry().Profile(profile.ID)
	identity.StaticDefault = hasStatic
	provenance := &AgentProfileProvenance{Origin: AgentProfileOriginOperator, Actor: actor, Note: strings.TrimSpace(input.Note), DerivedFromVersion: identity.ActiveVersion}
	if hasStatic {
		provenance.StaticContractSHA256 = static.PromptContractSHA256
		if identity.ActiveVersion == 0 {
			provenance.Origin = AgentProfileOriginStaticDefault
		}
	}
	profile.Provenance = provenance
	profile.Status = AgentProfileStatusDraft
	profile.Version = identity.LatestVersion + 1
	if identity.DraftVersion > 0 {
		if previous, ok := s.agentProfileVersions[profile.ID][identity.DraftVersion]; ok && previous.Status == AgentProfileStatusDraft {
			previous.Status = AgentProfileStatusSuperseded
			previous.UpdatedAt = now
			s.agentProfileVersions[profile.ID][identity.DraftVersion] = previous
		}
	}
	version := AgentProfileVersion{AgentProfile: profile, CreatedAt: now, UpdatedAt: now}
	if s.agentProfileVersions[profile.ID] == nil {
		s.agentProfileVersions[profile.ID] = map[int]AgentProfileVersion{}
	}
	s.agentProfileVersions[profile.ID][profile.Version] = cloneAgentProfileVersion(version)
	identity.LatestVersion, identity.DraftVersion, identity.UpdatedAt = profile.Version, profile.Version, now
	s.agentProfiles[profile.ID] = identity
	s.appendAgentProfileAuditLocked(AgentProfileAuditEntry{ProfileID: profile.ID, Version: profile.Version, Action: AgentProfileAuditDraftSaved, Actor: actor, Reason: strings.TrimSpace(input.Note), PromptContractSHA256: profile.PromptContractSHA256, PreviousActiveVersion: identity.ActiveVersion, CreatedAt: now})
	return cloneAgentProfileVersion(version), nil
}

func (s *MemoryStore) ActivateAgentProfileVersion(id string, input AgentProfileActivationInput, actor string) (AgentProfileVersion, error) {
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	identity, ok := s.agentProfiles[id]
	if !ok {
		return AgentProfileVersion{}, ErrNotFound
	}
	target, ok := s.agentProfileVersions[id][input.Version]
	if !ok {
		return AgentProfileVersion{}, ErrNotFound
	}
	if target.Status != AgentProfileStatusDraft {
		return AgentProfileVersion{}, fmt.Errorf("%w: agent profile %q version %d is %s, only a draft can be activated", ErrConflict, id, input.Version, target.Status)
	}
	now := s.now().UTC()
	previousActive := identity.ActiveVersion
	if previousActive > 0 {
		if previous, ok := s.agentProfileVersions[id][previousActive]; ok {
			previous.Status = AgentProfileStatusSuperseded
			previous.UpdatedAt = now
			s.agentProfileVersions[id][previousActive] = previous
		}
	}
	target.Status = AgentProfileStatusActive
	target.UpdatedAt = now
	target.ActivatedAt = &now
	s.agentProfileVersions[id][input.Version] = target
	identity.ActiveVersion = input.Version
	if identity.DraftVersion == input.Version {
		identity.DraftVersion = 0
	}
	identity.UpdatedAt = now
	s.agentProfiles[id] = identity
	s.appendAgentProfileAuditLocked(AgentProfileAuditEntry{ProfileID: id, Version: input.Version, Action: AgentProfileAuditActivated, Actor: actor, Reason: strings.TrimSpace(input.Reason), PromptContractSHA256: target.PromptContractSHA256, PreviousActiveVersion: previousActive, CreatedAt: now})
	return cloneAgentProfileVersion(target), nil
}

func (s *MemoryStore) RollbackAgentProfile(id string, input AgentProfileActivationInput, actor string) (AgentProfileVersion, error) {
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	identity, ok := s.agentProfiles[id]
	if !ok {
		return AgentProfileVersion{}, ErrNotFound
	}
	target, ok := s.agentProfileVersions[id][input.Version]
	if !ok {
		return AgentProfileVersion{}, ErrNotFound
	}
	if err := validateAgentProfileRollbackTarget(identity, target); err != nil {
		return AgentProfileVersion{}, err
	}
	now := s.now().UTC()
	previousActive := identity.ActiveVersion
	if previousActive > 0 {
		if previous, ok := s.agentProfileVersions[id][previousActive]; ok {
			previous.Status = AgentProfileStatusSuperseded
			previous.UpdatedAt = now
			s.agentProfileVersions[id][previousActive] = previous
		}
	}
	restored := rollbackAgentProfileVersion(target, identity.LatestVersion+1, actor, strings.TrimSpace(input.Reason), now)
	s.agentProfileVersions[id][restored.Version] = cloneAgentProfileVersion(restored)
	identity.ActiveVersion, identity.LatestVersion, identity.UpdatedAt = restored.Version, restored.Version, now
	s.agentProfiles[id] = identity
	s.appendAgentProfileAuditLocked(AgentProfileAuditEntry{ProfileID: id, Version: restored.Version, Action: AgentProfileAuditRolledBack, Actor: actor, Reason: strings.TrimSpace(input.Reason), PromptContractSHA256: restored.PromptContractSHA256, PreviousActiveVersion: previousActive, CreatedAt: now})
	return cloneAgentProfileVersion(restored), nil
}

func (s *MemoryStore) ListAgentProfileAudit(id string, limit int) ([]AgentProfileAuditEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, ok := s.agentProfileAudit[strings.TrimSpace(id)]
	if !ok {
		return nil, ErrNotFound
	}
	if limit <= 0 || limit > defaultAgentProfileAuditLimit {
		limit = defaultAgentProfileAuditLimit
	}
	out := make([]AgentProfileAuditEntry, 0, limit)
	for index := len(entries) - 1; index >= 0 && len(out) < limit; index-- {
		out = append(out, entries[index])
	}
	return out, nil
}

func (s *MemoryStore) appendAgentProfileAuditLocked(entry AgentProfileAuditEntry) {
	entry.ID = newID("apa")
	s.agentProfileAudit[entry.ProfileID] = append(s.agentProfileAudit[entry.ProfileID], entry)
}

// validateAgentProfileRollbackTarget only allows restoring a version that was
// active at some point and is not the current active version.
func validateAgentProfileRollbackTarget(identity AgentProfileIdentity, target AgentProfileVersion) error {
	if target.ActivatedAt == nil {
		return fmt.Errorf("%w: agent profile %q version %d was never activated and cannot be a rollback target", ErrInvalid, identity.ID, target.Version)
	}
	if identity.ActiveVersion == target.Version {
		return fmt.Errorf("%w: agent profile %q version %d is already active", ErrConflict, identity.ID, target.Version)
	}
	return nil
}

// rollbackAgentProfileVersion copies an immutable historical body into a new
// active version so the audit trail stays monotonic.
func rollbackAgentProfileVersion(target AgentProfileVersion, version int, actor, reason string, now time.Time) AgentProfileVersion {
	restored := cloneAgentProfileVersion(target)
	restored.Version = version
	restored.Status = AgentProfileStatusActive
	restored.Source = AgentProfileSourcePersisted
	restored.Provenance = &AgentProfileProvenance{Origin: AgentProfileOriginRollback, Actor: actor, Note: reason, RolledBackFromVersion: target.Version}
	if target.Provenance != nil {
		restored.Provenance.StaticContractSHA256 = target.Provenance.StaticContractSHA256
	}
	restored.CreatedAt, restored.UpdatedAt = now, now
	activated := now
	restored.ActivatedAt = &activated
	return restored
}

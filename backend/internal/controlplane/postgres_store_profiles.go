package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const agentProfileIdentityColumns = "id, active_version, draft_version, latest_version, static_default, created_at, updated_at"
const agentProfileVersionColumns = "profile_id, version, status, name, role, default_model, allowed_models, allowed_executors, prompt_contract, prompt_contract_sha256, provenance, created_at, updated_at, activated_at"
const agentProfileAuditColumns = "id, profile_id, version, action, actor, reason, prompt_contract_sha256, previous_active_version, created_at"

func (s *PostgresStore) AgentProfileIdentity(id string) (AgentProfileIdentity, error) {
	identity, err := scanAgentProfileIdentityRow(s.db.QueryRowContext(s.operationContext(), `select `+agentProfileIdentityColumns+` from agent_profiles where id=$1`, strings.TrimSpace(id)))
	return identity, mapScanErr(err)
}

func (s *PostgresStore) ListAgentProfileIdentities() ([]AgentProfileIdentity, error) {
	rows, err := s.db.QueryContext(s.operationContext(), `select `+agentProfileIdentityColumns+` from agent_profiles order by id`)
	if err != nil {
		return nil, mapPgErr(err)
	}
	defer rows.Close()
	out := []AgentProfileIdentity{}
	for rows.Next() {
		identity, err := scanAgentProfileIdentityRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, identity)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ActiveAgentProfile(id string) (AgentProfileVersion, error) {
	version, err := scanAgentProfileVersionRow(s.db.QueryRowContext(s.operationContext(), `select `+agentProfileVersionColumns+` from agent_profile_versions where profile_id=$1 and status='active'`, strings.TrimSpace(id)))
	return version, mapScanErr(err)
}

func (s *PostgresStore) ListActiveAgentProfiles() ([]AgentProfileVersion, error) {
	return s.queryAgentProfileVersions(`select ` + agentProfileVersionColumns + ` from agent_profile_versions where status='active' order by profile_id`)
}

func (s *PostgresStore) AgentProfileVersion(id string, version int) (AgentProfileVersion, error) {
	stored, err := scanAgentProfileVersionRow(s.db.QueryRowContext(s.operationContext(), `select `+agentProfileVersionColumns+` from agent_profile_versions where profile_id=$1 and version=$2`, strings.TrimSpace(id), version))
	return stored, mapScanErr(err)
}

func (s *PostgresStore) ListAgentProfileVersions(id string) ([]AgentProfileVersion, error) {
	out, err := s.queryAgentProfileVersions(`select `+agentProfileVersionColumns+` from agent_profile_versions where profile_id=$1 order by version desc`, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out, nil
}

func (s *PostgresStore) queryAgentProfileVersions(query string, args ...any) ([]AgentProfileVersion, error) {
	rows, err := s.db.QueryContext(s.operationContext(), query, args...)
	if err != nil {
		return nil, mapPgErr(err)
	}
	defer rows.Close()
	out := []AgentProfileVersion{}
	for rows.Next() {
		version, err := scanAgentProfileVersionRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, version)
	}
	return out, rows.Err()
}

func (s *PostgresStore) SaveAgentProfileDraft(id string, input AgentProfileDraftInput, actor string) (AgentProfileVersion, error) {
	profile, err := normalizeAgentProfileDraft(id, input)
	if err != nil {
		return AgentProfileVersion{}, err
	}
	ctx := s.operationContext()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentProfileVersion{}, mapPgErr(err)
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now().UTC()
	identity, err := s.lockAgentProfileIdentityTx(ctx, tx, profile.ID)
	exists := true
	if errors.Is(err, ErrNotFound) {
		exists, err = false, nil
		identity = AgentProfileIdentity{ID: profile.ID, CreatedAt: now}
	}
	if err != nil {
		return AgentProfileVersion{}, err
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
	if !exists {
		if _, err := tx.ExecContext(ctx, `insert into agent_profiles(id, active_version, draft_version, latest_version, static_default, created_at, updated_at) values($1,0,0,0,$2,$3,$3)`, profile.ID, hasStatic, now); err != nil {
			return AgentProfileVersion{}, mapPgErr(err)
		}
	}
	if identity.DraftVersion > 0 {
		if _, err := tx.ExecContext(ctx, `update agent_profile_versions set status='superseded', updated_at=$3 where profile_id=$1 and version=$2 and status='draft'`, profile.ID, identity.DraftVersion, now); err != nil {
			return AgentProfileVersion{}, mapPgErr(err)
		}
	}
	version := AgentProfileVersion{AgentProfile: profile, CreatedAt: now, UpdatedAt: now}
	if err := insertAgentProfileVersionTx(ctx, tx, version); err != nil {
		return AgentProfileVersion{}, err
	}
	if _, err := tx.ExecContext(ctx, `update agent_profiles set draft_version=$2, latest_version=$2, static_default=$3, updated_at=$4 where id=$1`, profile.ID, profile.Version, hasStatic, now); err != nil {
		return AgentProfileVersion{}, mapPgErr(err)
	}
	if err := insertAgentProfileAuditTx(ctx, tx, AgentProfileAuditEntry{ProfileID: profile.ID, Version: profile.Version, Action: AgentProfileAuditDraftSaved, Actor: actor, Reason: strings.TrimSpace(input.Note), PromptContractSHA256: profile.PromptContractSHA256, PreviousActiveVersion: identity.ActiveVersion, CreatedAt: now}); err != nil {
		return AgentProfileVersion{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentProfileVersion{}, mapPgErr(err)
	}
	return version, nil
}

func (s *PostgresStore) ActivateAgentProfileVersion(id string, input AgentProfileActivationInput, actor string) (AgentProfileVersion, error) {
	id = strings.TrimSpace(id)
	ctx := s.operationContext()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentProfileVersion{}, mapPgErr(err)
	}
	defer func() { _ = tx.Rollback() }()
	identity, err := s.lockAgentProfileIdentityTx(ctx, tx, id)
	if err != nil {
		return AgentProfileVersion{}, err
	}
	target, err := scanAgentProfileVersionRow(tx.QueryRowContext(ctx, `select `+agentProfileVersionColumns+` from agent_profile_versions where profile_id=$1 and version=$2`, id, input.Version))
	if err != nil {
		return AgentProfileVersion{}, mapScanErr(err)
	}
	if target.Status != AgentProfileStatusDraft {
		return AgentProfileVersion{}, fmt.Errorf("%w: agent profile %q version %d is %s, only a draft can be activated", ErrConflict, id, input.Version, target.Status)
	}
	now := s.now().UTC()
	previousActive := identity.ActiveVersion
	if previousActive > 0 {
		if _, err := tx.ExecContext(ctx, `update agent_profile_versions set status='superseded', updated_at=$3 where profile_id=$1 and version=$2`, id, previousActive, now); err != nil {
			return AgentProfileVersion{}, mapPgErr(err)
		}
	}
	target, err = scanAgentProfileVersionRow(tx.QueryRowContext(ctx, `update agent_profile_versions set status='active', updated_at=$3, activated_at=$3 where profile_id=$1 and version=$2 returning `+agentProfileVersionColumns, id, input.Version, now))
	if err != nil {
		return AgentProfileVersion{}, mapScanErr(err)
	}
	draft := identity.DraftVersion
	if draft == input.Version {
		draft = 0
	}
	if _, err := tx.ExecContext(ctx, `update agent_profiles set active_version=$2, draft_version=$3, updated_at=$4 where id=$1`, id, input.Version, draft, now); err != nil {
		return AgentProfileVersion{}, mapPgErr(err)
	}
	if err := insertAgentProfileAuditTx(ctx, tx, AgentProfileAuditEntry{ProfileID: id, Version: input.Version, Action: AgentProfileAuditActivated, Actor: actor, Reason: strings.TrimSpace(input.Reason), PromptContractSHA256: target.PromptContractSHA256, PreviousActiveVersion: previousActive, CreatedAt: now}); err != nil {
		return AgentProfileVersion{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentProfileVersion{}, mapPgErr(err)
	}
	return target, nil
}

func (s *PostgresStore) RollbackAgentProfile(id string, input AgentProfileActivationInput, actor string) (AgentProfileVersion, error) {
	id = strings.TrimSpace(id)
	ctx := s.operationContext()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentProfileVersion{}, mapPgErr(err)
	}
	defer func() { _ = tx.Rollback() }()
	identity, err := s.lockAgentProfileIdentityTx(ctx, tx, id)
	if err != nil {
		return AgentProfileVersion{}, err
	}
	target, err := scanAgentProfileVersionRow(tx.QueryRowContext(ctx, `select `+agentProfileVersionColumns+` from agent_profile_versions where profile_id=$1 and version=$2`, id, input.Version))
	if err != nil {
		return AgentProfileVersion{}, mapScanErr(err)
	}
	if err := validateAgentProfileRollbackTarget(identity, target); err != nil {
		return AgentProfileVersion{}, err
	}
	now := s.now().UTC()
	previousActive := identity.ActiveVersion
	if previousActive > 0 {
		if _, err := tx.ExecContext(ctx, `update agent_profile_versions set status='superseded', updated_at=$3 where profile_id=$1 and version=$2`, id, previousActive, now); err != nil {
			return AgentProfileVersion{}, mapPgErr(err)
		}
	}
	restored := rollbackAgentProfileVersion(target, identity.LatestVersion+1, actor, strings.TrimSpace(input.Reason), now)
	if err := insertAgentProfileVersionTx(ctx, tx, restored); err != nil {
		return AgentProfileVersion{}, err
	}
	if _, err := tx.ExecContext(ctx, `update agent_profiles set active_version=$2, latest_version=$2, updated_at=$3 where id=$1`, id, restored.Version, now); err != nil {
		return AgentProfileVersion{}, mapPgErr(err)
	}
	if err := insertAgentProfileAuditTx(ctx, tx, AgentProfileAuditEntry{ProfileID: id, Version: restored.Version, Action: AgentProfileAuditRolledBack, Actor: actor, Reason: strings.TrimSpace(input.Reason), PromptContractSHA256: restored.PromptContractSHA256, PreviousActiveVersion: previousActive, CreatedAt: now}); err != nil {
		return AgentProfileVersion{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentProfileVersion{}, mapPgErr(err)
	}
	return restored, nil
}

func (s *PostgresStore) ListAgentProfileAudit(id string, limit int) ([]AgentProfileAuditEntry, error) {
	if limit <= 0 || limit > defaultAgentProfileAuditLimit {
		limit = defaultAgentProfileAuditLimit
	}
	rows, err := s.db.QueryContext(s.operationContext(), `select `+agentProfileAuditColumns+` from agent_profile_audit where profile_id=$1 order by id desc limit $2`, strings.TrimSpace(id), limit)
	if err != nil {
		return nil, mapPgErr(err)
	}
	defer rows.Close()
	out := []AgentProfileAuditEntry{}
	for rows.Next() {
		var entry AgentProfileAuditEntry
		var numericID int64
		if err := rows.Scan(&numericID, &entry.ProfileID, &entry.Version, &entry.Action, &entry.Actor, &entry.Reason, &entry.PromptContractSHA256, &entry.PreviousActiveVersion, &entry.CreatedAt); err != nil {
			return nil, mapPgErr(err)
		}
		entry.ID = fmt.Sprintf("apa-%d", numericID)
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, mapPgErr(err)
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out, nil
}

func (s *PostgresStore) lockAgentProfileIdentityTx(ctx context.Context, tx *sql.Tx, id string) (AgentProfileIdentity, error) {
	identity, err := scanAgentProfileIdentityRow(tx.QueryRowContext(ctx, `select `+agentProfileIdentityColumns+` from agent_profiles where id=$1 for update`, id))
	return identity, mapScanErr(err)
}

func insertAgentProfileVersionTx(ctx context.Context, tx *sql.Tx, version AgentProfileVersion) error {
	models, err := json.Marshal(stringsOrEmpty(version.AllowedModels))
	if err != nil {
		return err
	}
	executors, err := json.Marshal(stringsOrEmpty(version.AllowedExecutors))
	if err != nil {
		return err
	}
	provenance := []byte("{}")
	if version.Provenance != nil {
		if provenance, err = json.Marshal(version.Provenance); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `insert into agent_profile_versions(`+agentProfileVersionColumns+`) values($1,$2,$3,$4,$5,$6,$7::jsonb,$8::jsonb,$9,$10,$11::jsonb,$12,$13,$14)`,
		version.ID, version.Version, version.Status, version.Name, version.Role, version.DefaultModel, string(models), string(executors), version.PromptContract, version.PromptContractSHA256, string(provenance), version.CreatedAt, version.UpdatedAt, version.ActivatedAt)
	return mapPgErr(err)
}

func insertAgentProfileAuditTx(ctx context.Context, tx *sql.Tx, entry AgentProfileAuditEntry) error {
	_, err := tx.ExecContext(ctx, `insert into agent_profile_audit(profile_id, version, action, actor, reason, prompt_contract_sha256, previous_active_version, created_at) values($1,$2,$3,$4,$5,$6,$7,$8)`,
		entry.ProfileID, entry.Version, entry.Action, entry.Actor, entry.Reason, entry.PromptContractSHA256, entry.PreviousActiveVersion, entry.CreatedAt)
	return mapPgErr(err)
}

func stringsOrEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func scanAgentProfileIdentityRow(row scanner) (AgentProfileIdentity, error) {
	var identity AgentProfileIdentity
	if err := row.Scan(&identity.ID, &identity.ActiveVersion, &identity.DraftVersion, &identity.LatestVersion, &identity.StaticDefault, &identity.CreatedAt, &identity.UpdatedAt); err != nil {
		return AgentProfileIdentity{}, err
	}
	return identity, nil
}

func scanAgentProfileVersionRow(row scanner) (AgentProfileVersion, error) {
	var version AgentProfileVersion
	var models, executors, provenance []byte
	var activated sql.NullTime
	if err := row.Scan(&version.ID, &version.Version, &version.Status, &version.Name, &version.Role, &version.DefaultModel, &models, &executors, &version.PromptContract, &version.PromptContractSHA256, &provenance, &version.CreatedAt, &version.UpdatedAt, &activated); err != nil {
		return AgentProfileVersion{}, err
	}
	if err := json.Unmarshal(models, &version.AllowedModels); err != nil {
		return AgentProfileVersion{}, fmt.Errorf("decode agent profile %s allowed models: %w", version.ID, err)
	}
	if err := json.Unmarshal(executors, &version.AllowedExecutors); err != nil {
		return AgentProfileVersion{}, fmt.Errorf("decode agent profile %s allowed executors: %w", version.ID, err)
	}
	if len(provenance) > 0 && string(provenance) != "{}" {
		var decoded AgentProfileProvenance
		if err := json.Unmarshal(provenance, &decoded); err != nil {
			return AgentProfileVersion{}, fmt.Errorf("decode agent profile %s provenance: %w", version.ID, err)
		}
		version.Provenance = &decoded
	}
	if len(version.AllowedModels) == 0 {
		version.AllowedModels = nil
	}
	if activated.Valid {
		activatedAt := activated.Time.UTC()
		version.ActivatedAt = &activatedAt
	}
	version.Source = AgentProfileSourcePersisted
	version.CreatedAt, version.UpdatedAt = version.CreatedAt.UTC(), version.UpdatedAt.UTC()
	return version, nil
}

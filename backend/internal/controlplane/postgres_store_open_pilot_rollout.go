package controlplane

import (
	"database/sql"
	"encoding/json"
	"strings"
)

const openPilotRolloutColumns = "mode, registry_fingerprint, proof_targets, expires_at, closed_reason, generation, updated_at"

func (s *PostgresStore) OpenPilotRolloutStatus() (OpenPilotRolloutStatus, error) {
	tx, err := s.db.BeginTx(s.operationContext(), nil)
	if err != nil {
		return OpenPilotRolloutStatus{}, err
	}
	if _, err := tx.ExecContext(s.operationContext(), `update open_pilot_rollout_control set mode='disabled',closed_reason='proof expired',generation=generation+1,updated_at=clock_timestamp() where singleton and mode='proof' and (expires_at is null or expires_at <= clock_timestamp())`); err != nil {
		_ = tx.Rollback()
		return OpenPilotRolloutStatus{}, err
	}
	status, err := scanOpenPilotRollout(tx.QueryRowContext(s.operationContext(), `select `+openPilotRolloutColumns+` from open_pilot_rollout_control where singleton for update`))
	if err != nil {
		_ = tx.Rollback()
		return OpenPilotRolloutStatus{}, mapScanErr(err)
	}
	if err := tx.Commit(); err != nil {
		return OpenPilotRolloutStatus{}, err
	}
	return status, nil
}

func (s *PostgresStore) ConfigureOpenPilotRollout(input ConfigureOpenPilotRolloutInput) (OpenPilotRolloutStatus, error) {
	now := s.now().UTC()
	input.Reason = strings.TrimSpace(input.Reason)
	if len(input.Reason) > 512 {
		return OpenPilotRolloutStatus{}, ErrInvalid
	}
	if err := validateOpenPilotRolloutInput(input, now); err != nil {
		return OpenPilotRolloutStatus{}, err
	}
	tx, err := s.db.BeginTx(s.operationContext(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return OpenPilotRolloutStatus{}, err
	}
	current, err := scanOpenPilotRollout(tx.QueryRowContext(s.operationContext(), `select `+openPilotRolloutColumns+` from open_pilot_rollout_control where singleton for update`))
	if err != nil {
		_ = tx.Rollback()
		return OpenPilotRolloutStatus{}, mapScanErr(err)
	}
	targets := cloneOpenPilotProofTargets(input.ProofTargets)
	expires := input.ExpiresAt
	if input.Mode == "allow" {
		if current.Mode != "proof" || current.RegistryFingerprint != input.RegistryFingerprint {
			_ = tx.Rollback()
			return OpenPilotRolloutStatus{}, ErrConflict
		}
		for _, target := range current.ProofTargets {
			var succeeded bool
			err := tx.QueryRowContext(s.operationContext(), `select exists(select 1 from open_pilot_issue_states where repository_id=$1 and executor_generation=$2 and canonical_issue=$3 and phase=$4 and issue_closed)`, target.RepositoryID, target.ExecutorGeneration, target.Issue, OpenPilotPhaseIssueClosed).Scan(&succeeded)
			if err != nil || !succeeded {
				_ = tx.Rollback()
				if err != nil {
					return OpenPilotRolloutStatus{}, err
				}
				return OpenPilotRolloutStatus{}, ErrConflict
			}
		}
		if len(current.ProofTargets) != 2 {
			_ = tx.Rollback()
			return OpenPilotRolloutStatus{}, ErrConflict
		}
		targets, expires = current.ProofTargets, current.ExpiresAt
	}
	targetJSON, err := json.Marshal(targets)
	if err != nil {
		_ = tx.Rollback()
		return OpenPilotRolloutStatus{}, err
	}
	status, err := scanOpenPilotRollout(tx.QueryRowContext(s.operationContext(), `update open_pilot_rollout_control set mode=$1,registry_fingerprint=$2,proof_targets=$3,expires_at=$4,closed_reason=$5,generation=generation+1,updated_at=clock_timestamp() where singleton returning `+openPilotRolloutColumns, input.Mode, input.RegistryFingerprint, targetJSON, expires, input.Reason))
	if err != nil {
		_ = tx.Rollback()
		return OpenPilotRolloutStatus{}, mapScanErr(err)
	}
	if err := tx.Commit(); err != nil {
		return OpenPilotRolloutStatus{}, err
	}
	return status, nil
}

func scanOpenPilotRollout(row scanner) (OpenPilotRolloutStatus, error) {
	var status OpenPilotRolloutStatus
	var targets []byte
	var expires sql.NullTime
	if err := row.Scan(&status.Mode, &status.RegistryFingerprint, &targets, &expires, &status.ClosedReason, &status.Generation, &status.UpdatedAt); err != nil {
		return OpenPilotRolloutStatus{}, err
	}
	if len(targets) > 0 {
		if err := json.Unmarshal(targets, &status.ProofTargets); err != nil {
			return OpenPilotRolloutStatus{}, err
		}
	}
	status.ProofTargets = cloneOpenPilotProofTargets(status.ProofTargets)
	if expires.Valid {
		value := expires.Time.UTC()
		status.ExpiresAt = &value
	}
	status.UpdatedAt = status.UpdatedAt.UTC()
	return status, nil
}

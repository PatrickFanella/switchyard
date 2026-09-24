package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

func (s *PostgresStore) ReplaceWebhookAuth(ctx context.Context, config WebhookAuthConfig) error {
	if err := validateWebhookAuthConfig(config); err != nil {
		return err
	}
	all := append([]WebhookAuthMetadata(nil), config.Triggers...)
	sort.Slice(all, func(i, j int) bool { return all[i].TriggerID < all[j].TriggerID })
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner, digest string
	var generation int64
	if err := tx.QueryRowContext(ctx, `select owner,generation,digest from webhook_auth_config where singleton=true for update`).Scan(&owner, &generation, &digest); err != nil {
		return err
	}
	if owner != "" && owner != config.Owner {
		return ErrConflict
	}
	if config.Generation < generation || (config.Generation == generation && config.Digest != digest) {
		return ErrConflict
	}
	rows, err := tx.QueryContext(ctx, `select trigger_id from webhook_trigger_auth`)
	if err != nil {
		return err
	}
	union := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		union[id] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, metadata := range all {
		union[metadata.TriggerID] = true
	}
	triggerIDs := make([]string, 0, len(union))
	for id := range union {
		triggerIDs = append(triggerIDs, id)
	}
	sort.Strings(triggerIDs)
	incoming := map[string]WebhookAuthMetadata{}
	incomingIDs := make([]string, 0, len(all))
	for _, metadata := range all {
		incoming[metadata.TriggerID] = metadata
		incomingIDs = append(incomingIDs, metadata.TriggerID)
	}
	for _, triggerID := range triggerIDs {
		var triggerType TriggerType
		if err := tx.QueryRowContext(ctx, `select type from triggers where id=$1 for update`, triggerID).Scan(&triggerType); err != nil {
			return mapScanErr(err)
		}
		if _, included := incoming[triggerID]; included && triggerType != TriggerWebhook {
			return ErrInvalid
		}
	}
	if _, err := tx.ExecContext(ctx, `delete from webhook_trigger_auth where not (trigger_id = any($1))`, incomingIDs); err != nil {
		return err
	}
	for _, metadata := range all {
		if _, err := tx.ExecContext(ctx, `insert into webhook_trigger_auth(trigger_id,revision,updated_at) values($1,$2,clock_timestamp()) on conflict(trigger_id) do update set revision=excluded.revision,updated_at=excluded.updated_at`, metadata.TriggerID, metadata.Revision); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `delete from webhook_trigger_auth_keys where trigger_id=$1`, metadata.TriggerID); err != nil {
			return err
		}
		for _, key := range metadata.Keys {
			if _, err := tx.ExecContext(ctx, `insert into webhook_trigger_auth_keys(trigger_id,key_id,not_before,not_after) values($1,$2,$3,$4)`, metadata.TriggerID, key.ID, key.NotBefore.UTC(), key.NotAfter.UTC()); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `update webhook_auth_config set owner=$1,generation=$2,digest=$3,receipt_retention_seconds=$4,updated_at=clock_timestamp() where singleton=true`, config.Owner, config.Generation, config.Digest, int64(config.ReceiptRetention/time.Second)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PostgresStore) RevokeWebhookAuth(ctx context.Context, owner string, generation int64, digest string) error {
	if !validWebhookIdentifier(owner, 128) || generation < 1 || len(digest) != 64 || !lowerHex(digest) {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var activeOwner, activeDigest string
	var activeGeneration int64
	if err := tx.QueryRowContext(ctx, `select owner,generation,digest from webhook_auth_config where singleton=true for update`).Scan(&activeOwner, &activeGeneration, &activeDigest); err != nil {
		return err
	}
	if activeOwner == owner && activeGeneration == generation && activeDigest == digest {
		if _, err := tx.ExecContext(ctx, `delete from webhook_trigger_auth`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *PostgresStore) AdmitWebhook(input AdmitWebhookInput) (WebhookAdmission, error) {
	return s.AdmitWebhookContext(s.operationContext(), input)
}

func (s *PostgresStore) AdmitWebhookContext(ctx context.Context, input AdmitWebhookInput) (WebhookAdmission, error) {
	if err := validateWebhookAdmissionInput(input); err != nil {
		return WebhookAdmission{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WebhookAdmission{}, err
	}
	defer tx.Rollback()
	var now time.Time
	var receiptRetentionSeconds int64
	var authGeneration int64
	if err := tx.QueryRowContext(ctx, `select clock_timestamp(),receipt_retention_seconds,generation from webhook_auth_config where singleton=true for share`).Scan(&now, &receiptRetentionSeconds, &authGeneration); err != nil {
		return WebhookAdmission{}, err
	}
	if authGeneration != input.AuthGeneration {
		return WebhookAdmission{}, ErrConflict
	}
	var trigger Trigger
	var triggerInputs []byte
	if err := tx.QueryRowContext(ctx, `select id,workflow_id,type,enabled,inputs from triggers where id=$1 for update`, input.TriggerID).Scan(&trigger.ID, &trigger.WorkflowID, &trigger.Type, &trigger.Enabled, &triggerInputs); err != nil {
		return WebhookAdmission{}, mapScanErr(err)
	}
	if trigger.Type != TriggerWebhook || !trigger.Enabled {
		return WebhookAdmission{}, ErrInvalid
	}
	if err := json.Unmarshal(triggerInputs, &trigger.Inputs); err != nil {
		return WebhookAdmission{}, err
	}
	var valid bool
	if err := tx.QueryRowContext(ctx, `select exists(select 1 from webhook_trigger_auth a join webhook_trigger_auth_keys k using(trigger_id) where a.trigger_id=$1 and a.revision=$2 and k.key_id=$3 and $4 >= k.not_before and $4 < k.not_after)`, input.TriggerID, input.AuthRevision, input.KeyID, now).Scan(&valid); err != nil {
		return WebhookAdmission{}, err
	}
	if !valid {
		return WebhookAdmission{}, ErrConflict
	}
	var receiptDigest, receiptRunID string
	var receiptAcceptedAt time.Time
	err = tx.QueryRowContext(ctx, `select body_sha256,run_id,accepted_at from webhook_deliveries where trigger_id=$1 and delivery_id=$2 for update`, input.TriggerID, input.DeliveryID).Scan(&receiptDigest, &receiptRunID, &receiptAcceptedAt)
	receiptExpired := err == nil && !receiptAcceptedAt.After(now.UTC().Add(-time.Duration(receiptRetentionSeconds)*time.Second))
	if err == nil && !receiptExpired {
		if receiptDigest != input.BodySHA256 {
			return WebhookAdmission{}, ErrConflict
		}
		run, err := scanRunRow(tx.QueryRowContext(ctx, `select id, workflow_id, coalesce(trigger_id,''), status, inputs, created_at, queued_at, available_at, started_at, finished_at, claimed_at, claimed_by, lease_expires_at, attempt_count, max_attempts, summary, pipeline_id, pipeline_version, pipeline_snapshot from runs where id=$1`, receiptRunID))
		if errors.Is(err, sql.ErrNoRows) {
			run.ID = receiptRunID
		} else if err != nil {
			return WebhookAdmission{}, err
		}
		if err := tx.Commit(); err != nil {
			return WebhookAdmission{}, err
		}
		return WebhookAdmission{Run: run, Replayed: true}, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return WebhookAdmission{}, err
	}
	var definition WorkflowDefinition
	var inputShapeBytes, pipelineBytes []byte
	if err := tx.QueryRowContext(ctx, `select id,status,input_shape,pipeline from workflow_definitions where id=$1 and status='active' for share`, trigger.WorkflowID).Scan(&definition.ID, &definition.Status, &inputShapeBytes, &pipelineBytes); err != nil {
		return WebhookAdmission{}, mapScanErr(err)
	}
	if err := json.Unmarshal(inputShapeBytes, &definition.InputShape); err != nil {
		return WebhookAdmission{}, err
	}
	if err := json.Unmarshal(pipelineBytes, &definition.Pipeline); err != nil {
		return WebhookAdmission{}, err
	}
	inputs := cloneMap(trigger.Inputs)
	if inputs == nil {
		inputs = map[string]string{}
	}
	for key, value := range input.Inputs {
		if _, exists := inputs[key]; exists {
			return WebhookAdmission{}, ErrInvalid
		}
		inputs[key] = value
	}
	if err := validateWebhookRunInputs(definition.InputShape, inputs, input.InputTypes); err != nil {
		return WebhookAdmission{}, err
	}
	encodedInputs, err := json.Marshal(inputs)
	if err != nil {
		return WebhookAdmission{}, err
	}
	snapshot := pipelineFromDefinition(definition)
	attachPipelineProfileSnapshot(&snapshot, NewStoreBackedAgentProfileRegistry(s, DefaultAgentProfileRegistry()))
	encodedPipeline, err := json.Marshal(snapshot)
	if err != nil {
		return WebhookAdmission{}, err
	}
	maxAttempts := defaultRunMaxAttempts()
	run := Run{ID: newID("run"), WorkflowID: trigger.WorkflowID, TriggerID: trigger.ID, Status: RunQueued, Inputs: inputs, CreatedAt: now.UTC(), QueuedAt: now.UTC(), AvailableAt: now.UTC(), MaxAttempts: maxAttempts, PipelineID: snapshot.ID, PipelineVersion: pipelineVersion(snapshot), PipelineSnapshot: &snapshot}
	if receiptExpired {
		if _, err := tx.ExecContext(ctx, `delete from webhook_deliveries where trigger_id=$1 and delivery_id=$2`, input.TriggerID, input.DeliveryID); err != nil {
			return WebhookAdmission{}, err
		}
		if _, err := tx.ExecContext(ctx, `update webhook_receipt_metrics set receipts_pruned=receipts_pruned+1 where singleton=true`); err != nil {
			return WebhookAdmission{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `insert into runs(id,workflow_id,trigger_id,status,inputs,created_at,queued_at,available_at,attempt_count,max_attempts,summary,pipeline_id,pipeline_version,pipeline_snapshot) values($1,$2,$3,$4,$5::jsonb,$6,$6,$6,0,$7,'',$8,$9,$10::jsonb)`, run.ID, run.WorkflowID, run.TriggerID, run.Status, string(encodedInputs), run.CreatedAt, maxAttempts, run.PipelineID, run.PipelineVersion, string(encodedPipeline)); err != nil {
		return WebhookAdmission{}, mapPgErr(err)
	}
	if _, err := tx.ExecContext(ctx, `insert into webhook_deliveries(trigger_id,delivery_id,body_sha256,run_id,accepted_at) values($1,$2,$3,$4,$5)`, input.TriggerID, input.DeliveryID, input.BodySHA256, run.ID, run.CreatedAt); err != nil {
		return WebhookAdmission{}, mapPgErr(err)
	}
	if _, err := tx.ExecContext(ctx, `insert into run_events(id,run_id,type,message,created_at) values($1,$2,'run.created','Run queued',$3),($4,$2,'webhook.accepted','Webhook delivery accepted',$3)`, newID("evt"), run.ID, run.CreatedAt, newID("evt")); err != nil {
		return WebhookAdmission{}, err
	}
	if err := tx.Commit(); err != nil {
		return WebhookAdmission{}, err
	}
	return WebhookAdmission{Run: run}, nil
}

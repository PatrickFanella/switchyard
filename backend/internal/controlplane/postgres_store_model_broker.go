package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *PostgresStore) CreateModelBrokerRequest(input CreateModelBrokerRequestInput) (ModelBrokerRequest, error) {
	req, _, err := s.admitModelBrokerRequest(input, ModelBrokerRequestQueued, 0)
	return req, err
}

func (s *PostgresStore) BeginModelBrokerRequest(input CreateModelBrokerRequestInput) (ModelBrokerRequest, bool, error) {
	return s.admitModelBrokerRequest(input, ModelBrokerRequestRunning, 0)
}

func (s *PostgresStore) AdmitModelBrokerRequest(input CreateModelBrokerRequestInput, maxActive int) (ModelBrokerRequest, bool, error) {
	return s.admitModelBrokerRequest(input, ModelBrokerRequestQueued, maxActive)
}

func (s *PostgresStore) admitModelBrokerRequest(input CreateModelBrokerRequestInput, requestedStatus string, maxActive int) (ModelBrokerRequest, bool, error) {
	if strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.Model) == "" || maxActive < 0 {
		return ModelBrokerRequest{}, false, ErrInvalid
	}
	ctx := s.operationContext()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ModelBrokerRequest{}, false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `select pg_advisory_xact_lock($1)`, int64(7_031_270_001)); err != nil {
		return ModelBrokerRequest{}, false, err
	}
	if existing, err := scanModelBrokerRequestRow(tx.QueryRowContext(ctx, `select `+modelBrokerRequestColumns+` from model_broker_requests where id=$1`, input.ID)); err == nil {
		if err := tx.Commit(); err != nil {
			return ModelBrokerRequest{}, false, err
		}
		if existing.Status == ModelBrokerRequestDropped && existing.Error == "queue capacity exceeded" {
			return existing, false, ErrQueueFull
		}
		return existing, false, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ModelBrokerRequest{}, false, err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `select count(*) from model_broker_requests where status in ($1,$2,$3)`, ModelBrokerRequestQueued, ModelBrokerRequestRunning, ModelBrokerRequestCancelRequested).Scan(&active); err != nil {
		return ModelBrokerRequest{}, false, err
	}
	status, errorText := requestedStatus, ""
	if maxActive > 0 && active >= maxActive {
		status, errorText = ModelBrokerRequestDropped, "queue capacity exceeded"
	}
	body := redactPersistedJSON(input.RequestBody)
	row := tx.QueryRowContext(ctx, `insert into model_broker_requests(id,status,model,path,client,client_priority,request_body,error,correlation_ids,started_at,finished_at,created_at,updated_at) values($1,$2,$3,$4,$5,$6,$7::jsonb,$8,'{}'::jsonb,case when $2=$9 then now() end,case when $2=$10 then now() end,now(),now()) on conflict(id) do nothing returning `+modelBrokerRequestColumns, input.ID, status, input.Model, input.Path, input.Client, input.Priority, body, errorText, ModelBrokerRequestRunning, ModelBrokerRequestDropped)
	req, err := scanModelBrokerRequestRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		req, err = scanModelBrokerRequestRow(tx.QueryRowContext(ctx, `select `+modelBrokerRequestColumns+` from model_broker_requests where id=$1`, input.ID))
		if err == nil {
			if commitErr := tx.Commit(); commitErr != nil {
				return ModelBrokerRequest{}, false, commitErr
			}
			if req.Status == ModelBrokerRequestDropped && req.Error == "queue capacity exceeded" {
				return req, false, ErrQueueFull
			}
			return req, false, nil
		}
	}
	if err != nil {
		return ModelBrokerRequest{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return ModelBrokerRequest{}, false, err
	}
	if status == ModelBrokerRequestDropped {
		return req, false, ErrQueueFull
	}
	return req, true, nil
}

func (s *PostgresStore) ModelBrokerQueueSnapshot(idLimit int) (ModelBrokerQueueSnapshot, error) {
	if idLimit < 0 {
		return ModelBrokerQueueSnapshot{}, ErrInvalid
	}
	ctx := s.operationContext()
	var snapshot ModelBrokerQueueSnapshot
	err := s.db.QueryRowContext(ctx, `select count(*) filter (where status=$1), count(*) filter (where status=$2), count(*) filter (where status=$3) from model_broker_requests where status in ($1,$2,$3)`, ModelBrokerRequestQueued, ModelBrokerRequestRunning, ModelBrokerRequestCancelRequested).Scan(&snapshot.Queued, &snapshot.Running, &snapshot.CancelRequested)
	if err != nil {
		return ModelBrokerQueueSnapshot{}, err
	}
	rows, err := s.db.QueryContext(ctx, `select id from model_broker_requests where status in ($1,$2) order by created_at asc, id asc limit $3`, ModelBrokerRequestRunning, ModelBrokerRequestCancelRequested, idLimit)
	if err != nil {
		return ModelBrokerQueueSnapshot{}, err
	}
	defer rows.Close()
	snapshot.InFlightIDs = []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return ModelBrokerQueueSnapshot{}, err
		}
		snapshot.InFlightIDs = append(snapshot.InFlightIDs, id)
	}
	return snapshot, rows.Err()
}

func (s *PostgresStore) TransitionModelBrokerRequest(id string, input TransitionModelBrokerRequestInput) (ModelBrokerRequest, error) {
	if !ModelBrokerRequestTransitionAllowed(input.ExpectedStatus, input.Status) {
		return ModelBrokerRequest{}, ErrConflict
	}
	correlation, err := json.Marshal(mergeStringMaps(nil, input.CorrelationIDs))
	if err != nil {
		return ModelBrokerRequest{}, err
	}
	response := redactPersistedJSON(input.ResponseBody)
	errorText := embeddedSecretPattern.ReplaceAllString(input.Error, `${1}${2}${3}[REDACTED]`)
	terminal := isTerminalModelBrokerRequest(input.Status)
	row := s.db.QueryRowContext(s.operationContext(), `update model_broker_requests set status=$3, response_body=case when $7 then $4::jsonb else response_body end, error=case when $7 then $5 else error end, correlation_ids=`+modelCorrelationMergeSQL("$6")+`, started_at=case when $3=$8 then coalesce(started_at,now()) else started_at end, cancel_requested_at=case when $3 in ($9,$10) then coalesce(cancel_requested_at,now()) else cancel_requested_at end, finished_at=case when $7 then coalesce(finished_at,now()) else finished_at end, updated_at=now() where id=$1 and status=$2 returning `+modelBrokerRequestColumns, id, input.ExpectedStatus, input.Status, response, errorText, string(correlation), terminal, ModelBrokerRequestRunning, ModelBrokerRequestCancelRequested, ModelBrokerRequestCanceled)
	req, err := scanModelBrokerRequestRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		if _, lookupErr := s.ModelBrokerRequest(id); lookupErr != nil {
			return ModelBrokerRequest{}, lookupErr
		}
		return ModelBrokerRequest{}, ErrConflict
	}
	return req, err
}

func (s *PostgresStore) StartModelBrokerRequest(id string) (ModelBrokerRequest, error) {
	row := s.db.QueryRowContext(s.operationContext(), `update model_broker_requests set status=$2, started_at=coalesce(started_at, now()), updated_at=now() where id=$1 and status=$3 returning `+modelBrokerRequestColumns, id, ModelBrokerRequestRunning, ModelBrokerRequestQueued)
	req, err := scanModelBrokerRequestRow(row)
	return req, mapScanErr(err)
}

func (s *PostgresStore) ClaimNextModelBrokerRequest() (ModelBrokerRequest, error) {
	row := s.db.QueryRowContext(s.operationContext(), `
		with next_request as (
			select id from model_broker_requests
			where status=$1
			order by client_priority desc, created_at asc, id asc
			for update skip locked
			limit 1
		)
		update model_broker_requests
		set status=$2, started_at=coalesce(started_at, now()), updated_at=now()
		where id in (select id from next_request)
		returning `+modelBrokerRequestColumns, ModelBrokerRequestQueued, ModelBrokerRequestRunning)
	req, err := scanModelBrokerRequestRow(row)
	return req, mapScanErr(err)
}

func (s *PostgresStore) FinishModelBrokerRequest(id string, input FinishModelBrokerRequestInput) (ModelBrokerRequest, error) {
	status := strings.TrimSpace(input.Status)
	if status == "" {
		status = ModelBrokerRequestCompleted
	}
	correlation, err := json.Marshal(mergeStringMaps(nil, input.CorrelationIDs))
	if err != nil {
		return ModelBrokerRequest{}, err
	}
	response := redactPersistedJSON(input.ResponseBody)
	errorText := embeddedSecretPattern.ReplaceAllString(input.Error, `${1}${2}${3}[REDACTED]`)
	row := s.db.QueryRowContext(s.operationContext(), `update model_broker_requests set status=$2, response_body=$3::jsonb, error=$4, correlation_ids=`+modelCorrelationMergeSQL("$5")+`, finished_at=now(), updated_at=now() where id=$1 returning `+modelBrokerRequestColumns, id, status, response, errorText, string(correlation))
	req, err := scanModelBrokerRequestRow(row)
	return req, mapScanErr(err)
}

func (s *PostgresStore) MergeModelBrokerRequestCorrelationIDs(id string, correlationIDs map[string]string) (ModelBrokerRequest, error) {
	correlation, err := json.Marshal(mergeStringMaps(nil, correlationIDs))
	if err != nil {
		return ModelBrokerRequest{}, err
	}
	tx, err := s.db.BeginTx(s.operationContext(), nil)
	if err != nil {
		return ModelBrokerRequest{}, err
	}
	row := tx.QueryRowContext(s.operationContext(), `update model_broker_requests set correlation_ids=`+modelCorrelationMergeSQL("$2")+`, updated_at=now() where id=$1 returning `+modelBrokerRequestColumns, id, string(correlation))
	req, err := scanModelBrokerRequestRow(row)
	if err != nil {
		_ = tx.Rollback()
		return ModelBrokerRequest{}, mapScanErr(err)
	}
	if backend := strings.TrimSpace(correlationIDs[CorrelationDeprecatedBackend]); backend != "" {
		if _, err := tx.ExecContext(s.operationContext(), `insert into compatibility_usage_daily(usage_date, backend, attempts, first_attempt_at, last_attempt_at) values(current_date,$1,1,now(),now()) on conflict(usage_date, backend) do update set attempts=compatibility_usage_daily.attempts+1, first_attempt_at=least(compatibility_usage_daily.first_attempt_at, excluded.first_attempt_at), last_attempt_at=greatest(compatibility_usage_daily.last_attempt_at, excluded.last_attempt_at)`, backend); err != nil {
			_ = tx.Rollback()
			return ModelBrokerRequest{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ModelBrokerRequest{}, err
	}
	return req, nil
}

func (s *PostgresStore) CancelModelBrokerRequest(id string) (ModelBrokerRequest, error) {
	for {
		req, err := s.ModelBrokerRequest(id)
		if err != nil || isTerminalModelBrokerRequest(req.Status) || req.Status == ModelBrokerRequestCancelRequested {
			return req, err
		}
		status, errorText := ModelBrokerRequestCancelRequested, ""
		if req.Status == ModelBrokerRequestQueued {
			status, errorText = ModelBrokerRequestCanceled, "request canceled before execution"
		}
		req, err = s.TransitionModelBrokerRequest(id, TransitionModelBrokerRequestInput{ExpectedStatus: req.Status, Status: status, Error: errorText})
		if errors.Is(err, ErrConflict) {
			continue
		}
		return req, err
	}
}

func (s *PostgresStore) CancelAllModelBrokerRequests() (int, error) {
	requests, err := s.RequestCancelModelBrokerRequests(nil)
	return len(requests), err
}

func (s *PostgresStore) RequestCancelModelBrokerRequests(correlationIDs map[string]string) ([]ModelBrokerRequest, error) {
	correlation, err := json.Marshal(mergeStringMaps(nil, correlationIDs))
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(s.operationContext(), `update model_broker_requests set status=case when status=$1 then $4 else $3 end, error=case when status=$1 then 'request canceled before execution' else error end, correlation_ids=`+modelCorrelationMergeSQL("$5")+`, cancel_requested_at=coalesce(cancel_requested_at,now()), finished_at=case when status=$1 then coalesce(finished_at,now()) else finished_at end, updated_at=now() where status in ($1,$2) returning `+modelBrokerRequestColumns, ModelBrokerRequestQueued, ModelBrokerRequestRunning, ModelBrokerRequestCancelRequested, ModelBrokerRequestCanceled, string(correlation))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModelBrokerRequest{}
	for rows.Next() {
		req, err := scanModelBrokerRequestRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, rows.Err()
}

func modelCorrelationMergeSQL(parameter string) string {
	return `case when correlation_ids ? 'audit_action' then correlation_ids || (` + parameter + `::jsonb - 'audit_action' - 'audit_actor' - 'audit_reason') else correlation_ids || ` + parameter + `::jsonb end`
}

func (s *PostgresStore) ListModelBrokerRequests(filter ListModelBrokerRequestsFilter) ([]ModelBrokerRequest, error) {
	return s.ListModelBrokerRequestsContext(s.operationContext(), filter)
}

func (s *PostgresStore) ListModelBrokerRequestsContext(ctx context.Context, filter ListModelBrokerRequestsFilter) ([]ModelBrokerRequest, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 201 {
		limit = 200
	}
	args := []any{}
	where := []string{}
	if len(filter.Statuses) > 0 {
		placeholders := []string{}
		for _, status := range filter.Statuses {
			args = append(args, status)
			placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
		}
		where = append(where, "status in ("+strings.Join(placeholders, ",")+")")
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		args = append(args, "%"+strings.ToLower(query)+"%")
		where = append(where, fmt.Sprintf("(lower(id || ' ' || model || ' ' || path || ' ' || client || ' ' || request_body::text || ' ' || coalesce(response_body::text,'') || ' ' || error) like $%d)", len(args)))
	}
	if filter.BeforeCreated != nil {
		args = append(args, filter.BeforeCreated.UTC(), filter.BeforeID)
		where = append(where, fmt.Sprintf("(created_at, id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	if filter.SinceCreated != nil {
		args = append(args, filter.SinceCreated.UTC())
		where = append(where, fmt.Sprintf("created_at >= $%d", len(args)))
	}
	query := `select ` + modelBrokerRequestColumns + ` from model_broker_requests`
	if len(where) > 0 {
		query += ` where ` + strings.Join(where, ` and `)
	}
	args = append(args, limit)
	query += fmt.Sprintf(` order by created_at desc, id desc limit $%d`, len(args))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModelBrokerRequest{}
	for rows.Next() {
		req, err := scanModelBrokerRequestRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ModelBrokerRequest(id string) (ModelBrokerRequest, error) {
	return s.ModelBrokerRequestContext(s.operationContext(), id)
}

func (s *PostgresStore) ModelBrokerRequestContext(ctx context.Context, id string) (ModelBrokerRequest, error) {
	req, err := scanModelBrokerRequestRow(s.db.QueryRowContext(ctx, `select `+modelBrokerRequestColumns+` from model_broker_requests where id=$1`, id))
	return req, mapScanErr(err)
}

func (s *PostgresStore) AddModelBrokerFeedback(input CreateModelBrokerFeedbackInput) (ModelBrokerFeedback, error) {
	if strings.TrimSpace(input.RequestID) == "" || input.Rating < -1 || input.Rating > 1 || len(input.Comment) > 4096 {
		return ModelBrokerFeedback{}, ErrInvalid
	}
	feedback := ModelBrokerFeedback{ID: newID("feedback"), RequestID: input.RequestID, Rating: input.Rating, Comment: embeddedSecretPattern.ReplaceAllString(input.Comment, `${1}${2}${3}[REDACTED]`), CreatedAt: s.now().UTC()}
	err := s.db.QueryRowContext(s.operationContext(), `insert into model_broker_feedback(id, request_id, rating, comment, created_at) values($1,$2,$3,$4,$5) returning id, request_id, rating, comment, created_at`, feedback.ID, feedback.RequestID, feedback.Rating, feedback.Comment, feedback.CreatedAt).Scan(&feedback.ID, &feedback.RequestID, &feedback.Rating, &feedback.Comment, &feedback.CreatedAt)
	return feedback, mapPgErr(err)
}

func (s *PostgresStore) ListModelBrokerFeedback(limit int) ([]ModelBrokerFeedback, error) {
	if limit < 1 || limit > 200 {
		return nil, ErrInvalid
	}
	rows, err := s.db.QueryContext(s.operationContext(), `select id, request_id, rating, comment, created_at from model_broker_feedback order by created_at desc, id desc limit $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModelBrokerFeedback{}
	for rows.Next() {
		var feedback ModelBrokerFeedback
		if err := rows.Scan(&feedback.ID, &feedback.RequestID, &feedback.Rating, &feedback.Comment, &feedback.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, feedback)
	}
	return out, rows.Err()
}

func (s *PostgresStore) PruneRetention(before time.Time) (RetentionResult, error) {
	ctx := s.operationContext()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RetentionResult{}, err
	}
	defer tx.Rollback()
	runRows, err := tx.QueryContext(ctx, `select id from runs where created_at < $1 and status in ($2,$3,$4,$5,$6) order by created_at, id limit $7 for update skip locked`, before.UTC(), RunSucceeded, RunFailed, RunCanceled, RunPaused, RunTimedOut, retentionBatchSize)
	if err != nil {
		return RetentionResult{}, err
	}
	runIDs := []string{}
	for runRows.Next() {
		var id string
		if err := runRows.Scan(&id); err != nil {
			runRows.Close()
			return RetentionResult{}, err
		}
		runIDs = append(runIDs, id)
	}
	if err := runRows.Close(); err != nil {
		return RetentionResult{}, err
	}
	if err := runRows.Err(); err != nil {
		return RetentionResult{}, err
	}
	var artifacts, artifactBytes, artifactBytesUnmeasured int64
	var runResult sql.Result
	if len(runIDs) > 0 {
		placeholders, args := retentionIDArgs(runIDs)
		if err := tx.QueryRowContext(ctx, `with removed as (delete from artifacts where run_id in (`+placeholders+`) returning byte_size, storage_class) select count(*), coalesce(sum(byte_size) filter (where storage_class='inline-data-uri'),0), count(*) filter (where storage_class<>'inline-data-uri') from removed`, args...).Scan(&artifacts, &artifactBytes, &artifactBytesUnmeasured); err != nil {
			return RetentionResult{}, err
		}
		runResult, err = tx.ExecContext(ctx, `delete from runs where id in (`+placeholders+`)`, args...)
		if err != nil {
			return RetentionResult{}, err
		}
	}
	if artifacts > 0 {
		if _, err := tx.ExecContext(ctx, `update artifact_lifecycle_totals set pruned_count=pruned_count+$1, pruned_bytes=pruned_bytes+$2, pruned_bytes_unmeasured=pruned_bytes_unmeasured+$3 where singleton=true`, artifacts, artifactBytes, artifactBytesUnmeasured); err != nil {
			return RetentionResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `insert into artifact_lifecycle_daily(measurement_date, pruned_count, pruned_bytes, pruned_bytes_unmeasured) values(($1::timestamptz at time zone 'UTC')::date,$2,$3,$4) on conflict (measurement_date) do update set pruned_count=artifact_lifecycle_daily.pruned_count+excluded.pruned_count, pruned_bytes=artifact_lifecycle_daily.pruned_bytes+excluded.pruned_bytes, pruned_bytes_unmeasured=artifact_lifecycle_daily.pruned_bytes_unmeasured+excluded.pruned_bytes_unmeasured`, s.now().UTC(), artifacts, artifactBytes, artifactBytesUnmeasured); err != nil {
			return RetentionResult{}, err
		}
	}
	requestResult, err := tx.ExecContext(ctx, `with expired_requests as (select id from model_broker_requests where created_at < $1 and status in ($2,$3,$4,$5) order by created_at, id limit $6 for update skip locked) delete from model_broker_requests where id in (select id from expired_requests)`, before.UTC(), ModelBrokerRequestCompleted, ModelBrokerRequestFailed, ModelBrokerRequestCanceled, ModelBrokerRequestDropped, retentionBatchSize)
	if err != nil {
		return RetentionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return RetentionResult{}, err
	}
	var runs int64
	if runResult != nil {
		runs, _ = runResult.RowsAffected()
	}
	requests, _ := requestResult.RowsAffected()
	return RetentionResult{Runs: int(runs), ModelRequests: int(requests), Artifacts: int(artifacts), ArtifactBytes: artifactBytes, ArtifactBytesUnmeasured: int(artifactBytesUnmeasured)}, nil
}

const retentionBatchSize = 500

func retentionIDArgs(ids []string) (string, []any) {
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for index, id := range ids {
		placeholders[index] = fmt.Sprintf("$%d", index+1)
		args[index] = id
	}
	return strings.Join(placeholders, ","), args
}

func (s *PostgresStore) RuntimeMetrics() (RuntimeMetrics, error) {
	return s.RuntimeMetricsSince(s.operationContext(), time.Time{})
}

func (s *PostgresStore) RuntimeMetricsSince(ctx context.Context, since time.Time) (RuntimeMetrics, error) {
	metrics := RuntimeMetrics{RunStatusCounts: map[string]int{}, ModelRequestStatusCounts: map[string]int{}}
	if err := s.db.QueryRowContext(ctx, `select count(*), coalesce(sum(byte_size) filter (where storage_class='inline-data-uri'),0), count(*) filter (where storage_class<>'inline-data-uri') from artifacts`).Scan(&metrics.ArtifactsRetained, &metrics.ArtifactBytesRetained, &metrics.ArtifactBytesRetainedUnmeasured); err != nil {
		return RuntimeMetrics{}, err
	}
	if err := s.db.QueryRowContext(ctx, `select observation_started_at, created_count, created_bytes, pruned_count, pruned_bytes, pruned_bytes_unmeasured from artifact_lifecycle_totals where singleton=true`).Scan(&metrics.ArtifactObservationStartedAt, &metrics.ArtifactsCreated, &metrics.ArtifactBytesCreated, &metrics.ArtifactsPruned, &metrics.ArtifactBytesPruned, &metrics.ArtifactBytesPrunedUnmeasured); err != nil {
		return RuntimeMetrics{}, err
	}
	rows, err := s.db.QueryContext(ctx, `select status, count(*) from runs group by status`)
	if err != nil {
		return RuntimeMetrics{}, err
	}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			return RuntimeMetrics{}, err
		}
		metrics.RunStatusCounts[status] = count
	}
	rows.Close()
	where, args := "", []any{}
	if !since.IsZero() {
		where = " where created_at >= $1"
		args = append(args, since.UTC())
	}
	rows, err = s.db.QueryContext(ctx, `select status, count(*) from model_broker_requests`+where+` group by status`, args...)
	if err != nil {
		return RuntimeMetrics{}, err
	}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			return RuntimeMetrics{}, err
		}
		metrics.ModelRequestStatusCounts[status] = count
	}
	rows.Close()
	latencyWhere := ` where started_at is not null and finished_at is not null`
	if !since.IsZero() {
		latencyWhere += ` and created_at >= $1`
	}
	if err := s.db.QueryRowContext(ctx, `select count(*), coalesce(sum(extract(epoch from (finished_at-started_at))*1000),0), coalesce(max(extract(epoch from (finished_at-started_at))*1000),0) from model_broker_requests`+latencyWhere, args...).Scan(&metrics.ModelLatencyCount, &metrics.ModelLatencyMilliseconds, &metrics.ModelLatencyMaxMilliseconds); err != nil {
		return RuntimeMetrics{}, err
	}
	return metrics, nil
}

func normalizeJSONText(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "{}"
	}
	var value any
	if json.Unmarshal([]byte(trimmed), &value) == nil {
		return trimmed
	}
	encoded, _ := json.Marshal(map[string]string{"raw": raw})
	return string(encoded)
}

func scanModelBrokerRequestRow(row scanner) (ModelBrokerRequest, error) {
	var req ModelBrokerRequest
	var requestBody, responseBody, correlation []byte
	var responseBodyNull sql.NullString
	var started, finished, cancelRequested sql.NullTime
	if err := row.Scan(&req.ID, &req.Status, &req.Model, &req.Path, &req.Client, &req.ClientPriority, &requestBody, &responseBodyNull, &req.Error, &correlation, &req.CreatedAt, &started, &finished, &req.UpdatedAt, &cancelRequested); err != nil {
		return ModelBrokerRequest{}, err
	}
	req.RequestBody = string(requestBody)
	if responseBodyNull.Valid {
		responseBody = []byte(responseBodyNull.String)
		req.ResponseBody = string(responseBody)
	}
	if started.Valid {
		req.StartedAt = &started.Time
	}
	if finished.Valid {
		req.FinishedAt = &finished.Time
	}
	if cancelRequested.Valid {
		req.CancelRequestedAt = &cancelRequested.Time
	}
	_ = json.Unmarshal(correlation, &req.CorrelationIDs)
	return req, nil
}

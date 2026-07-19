package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func (s *PostgresStore) CreateModelBrokerRequest(input CreateModelBrokerRequestInput) (ModelBrokerRequest, error) {
	if strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.Model) == "" {
		return ModelBrokerRequest{}, ErrInvalid
	}
	body := redactPersistedJSON(input.RequestBody)
	if _, err := s.db.ExecContext(s.operationContext(), `
		insert into model_broker_requests(id, status, model, path, client, client_priority, request_body, correlation_ids, created_at, updated_at)
		values($1,$2,$3,$4,$5,$6,$7::jsonb,'{}'::jsonb,now(),now())
		on conflict(id) do nothing`, input.ID, ModelBrokerRequestQueued, input.Model, input.Path, input.Client, input.Priority, body); err != nil {
		return ModelBrokerRequest{}, err
	}
	return s.ModelBrokerRequest(input.ID)
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
	row := s.db.QueryRowContext(s.operationContext(), `update model_broker_requests set status=$2, response_body=$3::jsonb, error=$4, correlation_ids=correlation_ids || $5::jsonb, finished_at=now(), updated_at=now() where id=$1 returning `+modelBrokerRequestColumns, id, status, response, errorText, string(correlation))
	req, err := scanModelBrokerRequestRow(row)
	return req, mapScanErr(err)
}

func (s *PostgresStore) MergeModelBrokerRequestCorrelationIDs(id string, correlationIDs map[string]string) (ModelBrokerRequest, error) {
	correlation, err := json.Marshal(mergeStringMaps(nil, correlationIDs))
	if err != nil {
		return ModelBrokerRequest{}, err
	}
	row := s.db.QueryRowContext(s.operationContext(), `update model_broker_requests set correlation_ids=correlation_ids || $2::jsonb, updated_at=now() where id=$1 returning `+modelBrokerRequestColumns, id, string(correlation))
	req, err := scanModelBrokerRequestRow(row)
	return req, mapScanErr(err)
}

func (s *PostgresStore) CancelModelBrokerRequest(id string) (ModelBrokerRequest, error) {
	row := s.db.QueryRowContext(s.operationContext(), `update model_broker_requests set status=$2, cancel_requested_at=now(), updated_at=now() where id=$1 and status in ($3,$4,$5) returning `+modelBrokerRequestColumns, id, ModelBrokerRequestCancelRequested, ModelBrokerRequestQueued, ModelBrokerRequestRunning, ModelBrokerRequestCancelRequested)
	req, err := scanModelBrokerRequestRow(row)
	return req, mapScanErr(err)
}

func (s *PostgresStore) CancelAllModelBrokerRequests() (int, error) {
	result, err := s.db.ExecContext(s.operationContext(), `update model_broker_requests set status=$1, cancel_requested_at=now(), updated_at=now() where status in ($2,$3,$4)`, ModelBrokerRequestCancelRequested, ModelBrokerRequestQueued, ModelBrokerRequestRunning, ModelBrokerRequestCancelRequested)
	if err != nil {
		return 0, err
	}
	count, _ := result.RowsAffected()
	return int(count), nil
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
	query := `select ` + modelBrokerRequestColumns + ` from model_broker_requests`
	if len(where) > 0 {
		query += ` where ` + strings.Join(where, ` and `)
	}
	args = append(args, limit)
	if len(filter.Statuses) > 0 {
		query += fmt.Sprintf(` order by client_priority desc, created_at asc, id asc limit $%d`, len(args))
	} else {
		query += fmt.Sprintf(` order by created_at desc, id desc limit $%d`, len(args))
	}
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
	tx, err := s.db.BeginTx(s.operationContext(), nil)
	if err != nil {
		return RetentionResult{}, err
	}
	runResult, err := tx.ExecContext(s.operationContext(), `delete from runs where created_at < $1 and status in ($2,$3,$4,$5,$6)`, before.UTC(), RunSucceeded, RunFailed, RunCanceled, RunPaused, RunTimedOut)
	if err != nil {
		_ = tx.Rollback()
		return RetentionResult{}, err
	}
	requestResult, err := tx.ExecContext(s.operationContext(), `delete from model_broker_requests where created_at < $1 and status in ($2,$3,$4)`, before.UTC(), ModelBrokerRequestCompleted, ModelBrokerRequestFailed, ModelBrokerRequestCanceled)
	if err != nil {
		_ = tx.Rollback()
		return RetentionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return RetentionResult{}, err
	}
	runs, _ := runResult.RowsAffected()
	requests, _ := requestResult.RowsAffected()
	return RetentionResult{Runs: int(runs), ModelRequests: int(requests)}, nil
}

func (s *PostgresStore) RuntimeMetrics() (RuntimeMetrics, error) {
	ctx := s.operationContext()
	metrics := RuntimeMetrics{RunStatusCounts: map[string]int{}, ModelRequestStatusCounts: map[string]int{}}
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
	rows, err = s.db.QueryContext(ctx, `select status, count(*) from model_broker_requests group by status`)
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
	if err := s.db.QueryRowContext(ctx, `select count(*), coalesce(sum(extract(epoch from (finished_at-started_at))*1000),0) from model_broker_requests where started_at is not null and finished_at is not null`).Scan(&metrics.ModelLatencyCount, &metrics.ModelLatencyMilliseconds); err != nil {
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

package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
)

const appLogQueueSize = 1024
const requestEventQueueSize = 1024

// Store owns the Postgres pool and schema migrations.
type Store struct {
	pool              *pgxpool.Pool
	appLogMu          sync.Mutex
	appLogCh          chan AppLogEvent
	appLogDone        chan struct{}
	appLogWG          sync.WaitGroup
	appLogClosed      bool
	reqMu             sync.Mutex
	reqCh             chan RequestEvent
	reqDone           chan struct{}
	reqWG             sync.WaitGroup
	reqClosed         bool
	embeddingsEnabled bool
}

// Open creates a store, opens the pool, and optionally runs migrations.
func Open(ctx context.Context, cfg config.DatabaseConfig) (*Store, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if cfg.URL == "" {
		return nil, fmt.Errorf("database url required when enabled")
	}
	pcfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, err
	}
	if cfg.MaxConns > 0 {
		pcfg.MaxConns = int32(cfg.MaxConns)
	}
	if pcfg.MinConns == 0 {
		pcfg.MinConns = 1
	}
	if pcfg.MaxConnLifetime == 0 {
		pcfg.MaxConnLifetime = time.Hour
	}
	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, err
	}
	store := &Store{pool: pool}
	if cfg.PersistAppLogs {
		store.appLogCh = make(chan AppLogEvent, appLogQueueSize)
		store.appLogDone = make(chan struct{})
		store.appLogWG.Add(1)
		go store.runAppLogWriter()
	}
	if cfg.Enabled {
		store.reqCh = make(chan RequestEvent, requestEventQueueSize)
		store.reqDone = make(chan struct{})
		store.reqWG.Add(1)
		go store.runRequestEventWriter()
	}
	if cfg.RunMigrations {
		if err := store.Migrate(ctx); err != nil {
			pool.Close()
			return nil, err
		}
	}
	return store, nil
}

// Pool returns the underlying pgx pool.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// SetEmbeddingsEnabled enables embedding-job creation after optional embedding migrations are applied.
func (s *Store) SetEmbeddingsEnabled(enabled bool) {
	if s != nil {
		s.embeddingsEnabled = enabled
	}
}

// Close shuts down the pool.
func (s *Store) Close() {
	if s != nil && s.pool != nil {
		if s.appLogDone != nil {
			s.appLogMu.Lock()
			if !s.appLogClosed {
				s.appLogClosed = true
				close(s.appLogDone)
			}
			s.appLogMu.Unlock()
			s.appLogWG.Wait()
		}
		if s.reqDone != nil {
			s.reqMu.Lock()
			if !s.reqClosed {
				s.reqClosed = true
				close(s.reqDone)
			}
			s.reqMu.Unlock()
			s.reqWG.Wait()
		}
		s.pool.Close()
	}
}

// Ping checks database connectivity.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return nil
	}
	return s.pool.Ping(ctx)
}

// EnqueueAppLog queues an app log for persistence without blocking.
func (s *Store) EnqueueAppLog(evt AppLogEvent) bool {
	if s == nil || s.appLogCh == nil {
		return false
	}
	s.appLogMu.Lock()
	defer s.appLogMu.Unlock()
	if s.appLogClosed {
		return false
	}
	select {
	case s.appLogCh <- evt:
		return true
	default:
		return false
	}
}

// EnqueueRequestEvent queues a request/audit event for persistence without blocking.
func (s *Store) EnqueueRequestEvent(evt RequestEvent) bool {
	if s == nil || s.reqCh == nil {
		return false
	}
	s.reqMu.Lock()
	defer s.reqMu.Unlock()
	if s.reqClosed {
		return false
	}
	select {
	case s.reqCh <- evt:
		return true
	default:
		return false
	}
}

func (s *Store) runAppLogWriter() {
	defer s.appLogWG.Done()
	batch := make([]AppLogEvent, 0, 64)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	flush := func() {
		if len(batch) == 0 {
			return
		}
		_ = s.insertAppLogs(context.Background(), batch)
		batch = batch[:0]
	}
	for {
		select {
		case <-s.appLogDone:
			batch = s.drainAppLogChannel(batch)
			flush()
			return
		case evt := <-s.appLogCh:
			batch = append(batch, evt)
			if len(batch) >= cap(batch) {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (s *Store) runRequestEventWriter() {
	defer s.reqWG.Done()
	batch := make([]RequestEvent, 0, 64)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	flush := func() {
		if len(batch) == 0 {
			return
		}
		_ = s.insertRequestEvents(context.Background(), batch)
		batch = batch[:0]
	}
	for {
		select {
		case <-s.reqDone:
			batch = s.drainRequestEventChannel(batch)
			flush()
			return
		case evt := <-s.reqCh:
			batch = append(batch, evt)
			if len(batch) >= cap(batch) {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (s *Store) drainRequestEventChannel(batch []RequestEvent) []RequestEvent {
	if s == nil || s.reqCh == nil {
		return batch
	}
	for {
		select {
		case evt := <-s.reqCh:
			batch = append(batch, evt)
		default:
			return batch
		}
	}
}

func (s *Store) drainAppLogChannel(batch []AppLogEvent) []AppLogEvent {
	if s == nil || s.appLogCh == nil {
		return batch
	}
	for {
		select {
		case evt := <-s.appLogCh:
			batch = append(batch, evt)
		default:
			return batch
		}
	}
}

func (s *Store) insertAppLogs(ctx context.Context, events []AppLogEvent) error {
	if s == nil || s.pool == nil || len(events) == 0 {
		return nil
	}
	rows := make([][]any, 0, len(events))
	for _, evt := range events {
		fields := evt.Fields
		if fields == nil {
			fields = map[string]any{}
		}
		fieldsJSON, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		rows = append(rows, []any{evt.Time, evt.Level, evt.Msg, fieldsJSON, evt.RawJSON})
	}
	_, err := s.pool.CopyFrom(ctx, pgx.Identifier{"broker", "app_log"}, []string{"ts", "level", "msg", "fields", "raw_json"}, pgx.CopyFromRows(rows))
	return err
}

func (s *Store) insertRequestEvents(ctx context.Context, events []RequestEvent) error {
	if s == nil || s.pool == nil || len(events) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	sql := insertRequestEventsSQL()
	for _, evt := range events {
		respValid := any(nil)
		if evt.ResponseJSONValid != nil {
			respValid = *evt.ResponseJSONValid
		}
		batch.Queue(sql,
			evt.RequestID, evt.Time, evt.Client, evt.Model, evt.Path, nullString(evt.Upstream), evt.Status, evt.Streaming, evt.DurationMS, evt.QueueMS, evt.UpstreamMS,
			jsonOrNull(evt.RequestBody), jsonOrNull(evt.ResponseBody), evt.RequestBodyText, evt.ResponseBodyText, evt.RequestBodyBytes, evt.ResponseBodyBytes,
			respValid, evt.PromptTokens, evt.CompletionTokens, evt.RequestMessageCount, stringSliceOrEmpty(evt.ToolNames), evt.ToolErrorCount, stringSliceOrEmpty(evt.ToolErrors), jsonOrEmptyObject(evt.RawAudit),
		)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range events {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	if s.embeddingsEnabled {
		for _, evt := range events {
			if len(embeddingChunks(evt, 4000)) == 0 {
				continue
			}
			if err := s.EnqueueEmbeddingJob(ctx, evt.RequestID); err != nil {
				return err
			}
		}
	}
	return nil
}

func insertRequestEventsSQL() string {
	return `insert into broker.request_event (
		request_id, ts, client, model, path, upstream, status, streaming, duration_ms, queue_ms, upstream_ms,
		request_body, response_body, request_body_text, response_body_text, request_body_bytes, response_body_bytes,
		response_json_valid, prompt_tokens, completion_tokens, request_message_count, tool_names, tool_error_count, tool_errors, raw_audit
	) values (
		$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25
	) on conflict (request_id) do update set
		ts=excluded.ts,
		client=excluded.client,
		model=excluded.model,
		path=excluded.path,
		upstream=excluded.upstream,
		status=excluded.status,
		streaming=excluded.streaming,
		duration_ms=excluded.duration_ms,
		queue_ms=excluded.queue_ms,
		upstream_ms=excluded.upstream_ms,
		request_body=excluded.request_body,
		response_body=excluded.response_body,
		request_body_text=excluded.request_body_text,
		response_body_text=excluded.response_body_text,
		request_body_bytes=excluded.request_body_bytes,
		response_body_bytes=excluded.response_body_bytes,
		response_json_valid=excluded.response_json_valid,
		prompt_tokens=excluded.prompt_tokens,
		completion_tokens=excluded.completion_tokens,
		request_message_count=excluded.request_message_count,
		tool_names=excluded.tool_names,
		tool_error_count=excluded.tool_error_count,
		tool_errors=excluded.tool_errors,
		raw_audit=excluded.raw_audit`
}

func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func stringSliceOrEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func jsonOrNull(v json.RawMessage) any {
	if len(v) == 0 {
		return nil
	}
	return []byte(v)
}

func jsonOrEmptyObject(v json.RawMessage) []byte {
	if len(v) == 0 {
		return []byte(`{}`)
	}
	return []byte(v)
}

func (s *Store) SearchAppLogs(ctx context.Context, params AppLogSearchParams) ([]AppLogSearchRow, error) {
	if s == nil || s.pool == nil {
		return nil, nil
	}
	limit := clampLimit(params.Limit, 1, 500, 100)
	args := make([]any, 0, 7)
	where := []string{"true"}
	if params.Q != "" {
		args = append(args, params.Q)
		where = append(where, fmt.Sprintf("(search_tsv @@ plainto_tsquery('simple', $%d) or raw_json ilike '%%' || $%d || '%%')", len(args), len(args)))
	}
	if params.Level != "" {
		args = append(args, params.Level)
		where = append(where, fmt.Sprintf("level = $%d", len(args)))
	}
	if params.Client != "" {
		args = append(args, params.Client)
		where = append(where, fmt.Sprintf("client = $%d", len(args)))
	}
	if params.RequestID != "" {
		args = append(args, params.RequestID)
		where = append(where, fmt.Sprintf("request_id = $%d", len(args)))
	}
	if params.Since != nil {
		args = append(args, *params.Since)
		where = append(where, fmt.Sprintf("ts >= $%d", len(args)))
	}
	if params.Until != nil {
		args = append(args, *params.Until)
		where = append(where, fmt.Sprintf("ts <= $%d", len(args)))
	}
	args = append(args, limit)
	query := fmt.Sprintf(`select id, ts, level, msg, fields, raw_json, coalesce(client,''), coalesce(request_id,''), coalesce(model,''), coalesce(path,'')
		from broker.app_log
		where %s
		order by ts desc, id desc
		limit $%d`, joinWhere(where), len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AppLogSearchRow, 0, limit)
	for rows.Next() {
		var row AppLogSearchRow
		if err := rows.Scan(&row.ID, &row.Time, &row.Level, &row.Msg, &row.Fields, &row.RawJSON, &row.Client, &row.RequestID, &row.Model, &row.Path); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) SearchRequestEvents(ctx context.Context, params RequestEventSearchParams) ([]RequestEventSearchRow, error) {
	if s == nil || s.pool == nil {
		return nil, nil
	}
	limit := clampLimit(params.Limit, 1, 500, 100)
	args := make([]any, 0, 8)
	where := []string{"true"}
	if params.Q != "" {
		args = append(args, params.Q)
		where = append(where, fmt.Sprintf("search_tsv @@ plainto_tsquery('simple', $%d)", len(args)))
	}
	if params.Client != "" {
		args = append(args, params.Client)
		where = append(where, fmt.Sprintf("client = $%d", len(args)))
	}
	if params.Model != "" {
		args = append(args, params.Model)
		where = append(where, fmt.Sprintf("model = $%d", len(args)))
	}
	if params.Status != "" {
		args = append(args, params.Status)
		where = append(where, fmt.Sprintf("status = $%d", len(args)))
	}
	if params.Path != "" {
		args = append(args, params.Path)
		where = append(where, fmt.Sprintf("path = $%d", len(args)))
	}
	if params.Since != nil {
		args = append(args, *params.Since)
		where = append(where, fmt.Sprintf("ts >= $%d", len(args)))
	}
	if params.Until != nil {
		args = append(args, *params.Until)
		where = append(where, fmt.Sprintf("ts <= $%d", len(args)))
	}
	if params.ErrorsOnly {
		where = append(where, "(tool_error_count > 0 or response_json_valid = false or status ilike 'error%')")
	}
	args = append(args, limit)
	query := fmt.Sprintf(`select request_id, ts, coalesce(client,''), coalesce(model,''), coalesce(path,''), coalesce(upstream,''), status, streaming, duration_ms, queue_ms, upstream_ms,
		coalesce(request_body_text,''), coalesce(response_body_text,''), request_body_bytes, response_body_bytes, response_json_valid, request_message_count,
		tool_names, tool_error_count, tool_errors, raw_audit
		from broker.request_event
		where %s
		order by ts desc, request_id desc
		limit $%d`, joinWhere(where), len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RequestEventSearchRow, 0, limit)
	for rows.Next() {
		var row RequestEventSearchRow
		if err := rows.Scan(&row.RequestID, &row.Time, &row.Client, &row.Model, &row.Path, &row.Upstream, &row.Status, &row.Streaming, &row.DurationMS, &row.QueueMS, &row.UpstreamMS,
			&row.RequestBodyText, &row.ResponseBodyText, &row.RequestBodyBytes, &row.ResponseBodyBytes, &row.ResponseJSONValid, &row.RequestMessageCount,
			&row.ToolNames, &row.ToolErrorCount, &row.ToolErrors, &row.RawAudit); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func joinWhere(parts []string) string {
	if len(parts) == 0 {
		return "true"
	}
	return strings.Join(parts, " and ")
}

func clampLimit(v, min, max, def int) int {
	if v < min {
		return def
	}
	if v > max {
		return max
	}
	return v
}

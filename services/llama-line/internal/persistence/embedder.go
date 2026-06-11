package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
	"github.com/jackc/pgx/v5"
)

const EmbeddingDimension = 768

// Embedder converts text to an embedding vector.
type Embedder interface {
	Embed(ctx context.Context, model string, input string) ([]float32, error)
}

// OllamaEmbedder calls Ollama's preferred /api/embed endpoint.
type OllamaEmbedder struct {
	BaseURL    string
	HTTPClient *http.Client
	Dimension  int
}

func (e *OllamaEmbedder) Embed(ctx context.Context, model string, input string) ([]float32, error) {
	if e == nil {
		return nil, errors.New("nil embedder")
	}
	base := strings.TrimRight(e.BaseURL, "/")
	if base == "" {
		return nil, errors.New("ollama base url required")
	}
	body := map[string]any{"model": model, "input": input}
	if e.Dimension > 0 {
		body["dimensions"] = e.Dimension
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/embed", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := e.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ollama embed status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) == 0 || len(out.Embeddings[0]) == 0 {
		return nil, errors.New("ollama embed returned no vectors")
	}
	return out.Embeddings[0], nil
}

type embeddingChunk struct {
	Role    string         `json:"role"`
	Content string         `json:"content"`
	Meta    map[string]any `json:"meta,omitempty"`
}

func embeddingChunks(e RequestEvent, maxChars int) []embeddingChunk {
	if maxChars <= 0 {
		maxChars = 4000
	}
	chunks := make([]embeddingChunk, 0, 4+len(e.ToolErrors))
	for _, errText := range e.ToolErrors {
		if strings.TrimSpace(errText) == "" {
			continue
		}
		chunks = append(chunks, embeddingChunk{Role: "tool_error", Content: truncateString(errText, maxChars)})
	}
	chunks = append(chunks, chatChunks(e.RequestBodyText, "request", maxChars)...)
	chunks = append(chunks, chatChunks(e.ResponseBodyText, "response", maxChars)...)
	return chunks
}

func chatChunks(body string, fallbackRole string, maxChars int) []embeddingChunk {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &obj); err == nil {
		if raw := obj["messages"]; len(raw) > 0 {
			var msgs []struct {
				Role     string `json:"role"`
				Content  any    `json:"content"`
				Thinking any    `json:"thinking"`
			}
			if json.Unmarshal(raw, &msgs) == nil {
				out := make([]embeddingChunk, 0, len(msgs))
				for i, msg := range msgs {
					content := stringifyContent(msg.Content)
					if msg.Thinking != nil {
						content = strings.TrimSpace(content + "\n" + stringifyContent(msg.Thinking))
					}
					if content == "" {
						continue
					}
					role := msg.Role
					if role == "" {
						role = fallbackRole
					}
					out = append(out, embeddingChunk{Role: role, Content: truncateString(content, maxChars), Meta: map[string]any{"source": fallbackRole, "index": i}})
				}
				if len(out) > 0 {
					return out
				}
			}
		}
		if raw := obj["message"]; len(raw) > 0 {
			var msg struct {
				Role     string `json:"role"`
				Content  any    `json:"content"`
				Thinking any    `json:"thinking"`
			}
			if json.Unmarshal(raw, &msg) == nil {
				content := stringifyContent(msg.Content)
				if msg.Thinking != nil {
					content = strings.TrimSpace(content + "\n" + stringifyContent(msg.Thinking))
				}
				if content != "" {
					role := msg.Role
					if role == "" {
						role = fallbackRole
					}
					return []embeddingChunk{{Role: role, Content: truncateString(content, maxChars), Meta: map[string]any{"source": fallbackRole}}}
				}
			}
		}
	}
	return []embeddingChunk{{Role: fallbackRole, Content: truncateString(body, maxChars), Meta: map[string]any{"source": fallbackRole}}}
}

func stringifyContent(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprint(x)
		}
		return string(b)
	}
}

func truncateString(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max]
}

func (s *Store) EnqueueEmbeddingJob(ctx context.Context, requestID string) error {
	if s == nil || s.pool == nil || requestID == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `insert into broker.request_embedding_job(request_event_id)
		select id from broker.request_event where request_id=$1
		on conflict do nothing`, requestID)
	return err
}

func (s *Store) StartEmbeddingWorkers(ctx context.Context, cfg config.EmbeddingConfig, embedder Embedder) func() {
	if s == nil || s.pool == nil || !cfg.Enabled || embedder == nil {
		return func() {}
	}
	if cfg.Dimension != EmbeddingDimension {
		return func() {}
	}
	workers := cfg.WorkerCount
	if workers <= 0 {
		workers = 1
	}
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.runEmbeddingWorker(ctx, cfg, embedder)
		}()
	}
	return func() { cancel(); wg.Wait() }
}

func (s *Store) runEmbeddingWorker(ctx context.Context, cfg config.EmbeddingConfig, embedder Embedder) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.processOneEmbeddingJob(ctx, cfg, embedder)
		}
	}
}

func (s *Store) processOneEmbeddingJob(ctx context.Context, cfg config.EmbeddingConfig, embedder Embedder) error {
	job, evt, err := s.claimEmbeddingJob(ctx)
	if err != nil || job == "" {
		return err
	}
	chunks := embeddingChunks(evt, cfg.MaxChunkChars)
	if len(chunks) == 0 {
		return s.finishEmbeddingJob(ctx, job, "done", "")
	}
	if _, err := s.pool.Exec(ctx, `delete from broker.request_embedding where request_event_id=$1 and embedding_model=$2`, job, cfg.Model); err != nil {
		_ = s.finishEmbeddingJob(ctx, job, "error", err.Error())
		return err
	}
	for i, chunk := range chunks {
		vec, err := embedder.Embed(ctx, cfg.Model, chunk.Content)
		if err != nil {
			_ = s.finishEmbeddingJob(ctx, job, "error", err.Error())
			return err
		}
		if cfg.Dimension > 0 && len(vec) != cfg.Dimension {
			err := fmt.Errorf("embedding dimension %d != configured %d", len(vec), cfg.Dimension)
			_ = s.finishEmbeddingJob(ctx, job, "error", err.Error())
			return err
		}
		meta, _ := json.Marshal(chunk.Meta)
		if len(meta) == 0 || string(meta) == "null" {
			meta = []byte(`{}`)
		}
		literal, err := vectorLiteral(vec)
		if err != nil {
			_ = s.finishEmbeddingJob(ctx, job, "error", err.Error())
			return err
		}
		_, err = s.pool.Exec(ctx, `insert into broker.request_embedding (request_event_id, chunk_ix, role, content, embedding_model, embedding_dim, embedding, meta)
			values ($1,$2,$3,$4,$5,$6,$7::vector,$8)
			on conflict (request_event_id, chunk_ix, embedding_model) do update set
				ts=now(), role=excluded.role, content=excluded.content, embedding_dim=excluded.embedding_dim, embedding=excluded.embedding, meta=excluded.meta`,
			job, i, chunk.Role, chunk.Content, cfg.Model, len(vec), literal, meta)
		if err != nil {
			_ = s.finishEmbeddingJob(ctx, job, "error", err.Error())
			return err
		}
	}
	return s.finishEmbeddingJob(ctx, job, "done", "")
}

func (s *Store) claimEmbeddingJob(ctx context.Context) (string, RequestEvent, error) {
	if s == nil || s.pool == nil {
		return "", RequestEvent{}, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", RequestEvent{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var eventID string
	row := tx.QueryRow(ctx, `select request_event_id::text from broker.request_embedding_job
		where status in ('pending','error') and available_at <= now()
		order by available_at asc
		limit 1
		for update skip locked`)
	if err := row.Scan(&eventID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", RequestEvent{}, tx.Commit(ctx)
		}
		return "", RequestEvent{}, err
	}
	if _, err := tx.Exec(ctx, `update broker.request_embedding_job set status='running', attempts=attempts+1, updated_at=now() where request_event_id=$1`, eventID); err != nil {
		return "", RequestEvent{}, err
	}
	var evt RequestEvent
	var reqText, respText, upstream string
	var toolNames, toolErrors []string
	err = tx.QueryRow(ctx, `select request_id, ts, client, model, path, coalesce(upstream,''), status, streaming, duration_ms, queue_ms, upstream_ms,
		coalesce(request_body_text,''), coalesce(response_body_text,''), request_body_bytes, response_body_bytes, response_json_valid, request_message_count,
		tool_names, tool_error_count, tool_errors, raw_audit
		from broker.request_event where id=$1`, eventID).Scan(
		&evt.RequestID, &evt.Time, &evt.Client, &evt.Model, &evt.Path, &upstream, &evt.Status, &evt.Streaming, &evt.DurationMS, &evt.QueueMS, &evt.UpstreamMS,
		&reqText, &respText, &evt.RequestBodyBytes, &evt.ResponseBodyBytes, &evt.ResponseJSONValid, &evt.RequestMessageCount,
		&toolNames, &evt.ToolErrorCount, &toolErrors, &evt.RawAudit)
	if err != nil {
		return "", RequestEvent{}, err
	}
	evt.Upstream = upstream
	evt.RequestBodyText = reqText
	evt.ResponseBodyText = respText
	evt.ToolNames = toolNames
	evt.ToolErrors = toolErrors
	return eventID, evt, tx.Commit(ctx)
}

func (s *Store) finishEmbeddingJob(ctx context.Context, eventID, status, lastErr string) error {
	if s == nil || s.pool == nil || eventID == "" {
		return nil
	}
	available := "now()"
	if status == "error" {
		available = "now() + interval '30 seconds'"
	}
	_, err := s.pool.Exec(ctx, fmt.Sprintf(`update broker.request_embedding_job set status=$2, last_error=nullif($3,''), available_at=%s, updated_at=now() where request_event_id=$1`, available), eventID, status, lastErr)
	return err
}

func (s *Store) SemanticSearch(ctx context.Context, queryVector []float32, params SemanticSearchParams) ([]SemanticSearchRow, error) {
	if s == nil || s.pool == nil {
		return nil, nil
	}
	limit := clampLimit(params.Limit, 1, 100, 20)
	literal, err := vectorLiteral(queryVector)
	if err != nil {
		return nil, err
	}
	args := []any{literal}
	where := []string{"true"}
	if params.Client != "" {
		args = append(args, params.Client)
		where = append(where, fmt.Sprintf("re.client = $%d", len(args)))
	}
	if params.Model != "" {
		args = append(args, params.Model)
		where = append(where, fmt.Sprintf("re.model = $%d", len(args)))
	}
	if params.Status != "" {
		args = append(args, params.Status)
		where = append(where, fmt.Sprintf("re.status = $%d", len(args)))
	}
	if params.Path != "" {
		args = append(args, params.Path)
		where = append(where, fmt.Sprintf("re.path = $%d", len(args)))
	}
	args = append(args, limit)
	query := fmt.Sprintf(`select re.request_id, re.ts, re.client, re.model, re.path, re.status, e.role, e.content, 1 - (e.embedding <=> $1::vector) as score
		from broker.request_embedding e
		join broker.request_event re on re.id = e.request_event_id
		where %s
		order by e.embedding <=> $1::vector
		limit $%d`, joinWhere(where), len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]SemanticSearchRow, 0, limit)
	for rows.Next() {
		var row SemanticSearchRow
		if err := rows.Scan(&row.RequestID, &row.Time, &row.Client, &row.Model, &row.Path, &row.Status, &row.Role, &row.Content, &row.Score); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func vectorLiteral(vec []float32) (string, error) {
	if len(vec) == 0 {
		return "", errors.New("empty vector")
	}
	parts := make([]string, len(vec))
	for i, v := range vec {
		parts[i] = strconv.FormatFloat(float64(v), 'g', -1, 32)
	}
	return "[" + strings.Join(parts, ",") + "]", nil
}

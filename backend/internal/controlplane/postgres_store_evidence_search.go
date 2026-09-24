package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// evidenceSearchSourceSQL projects runs, steps, and model requests onto the
// shared hit shape. Inputs, request bodies, and response bodies are never
// selected or matched. The retention floor is applied inside each branch so
// the planner can use the created_at indexes before the union. Stored
// correlation_ids may be JSON null, so every jsonb_each_text call is guarded.
const evidenceSearchSourceSQL = `
select 'run' as kind, r.id, r.id as run_id, r.workflow_id, r.status, r.workflow_id as title, r.summary, '' as error, '{}'::jsonb as correlation_ids, r.created_at, r.finished_at,
       lower(r.id || ' ' || r.workflow_id || ' ' || coalesce(r.trigger_id, '') || ' ' || r.status || ' ' || r.summary) as haystack,
       lower(r.id) as correlation_haystack,
       (r.status in ('failed', 'timed_out')) as is_error
  from runs r
 where r.created_at >= $1
union all
select 'step', s.id, s.run_id, r.workflow_id, s.status, s.name, s.output_summary, s.error, s.correlation_ids, s.created_at, s.finished_at,
       lower(s.id || ' ' || s.run_id || ' ' || s.name || ' ' || s.executor || ' ' || s.model || ' ' || s.status || ' ' || s.output_summary || ' ' || s.error || ' ' || coalesce((select string_agg(c.value, ' ' order by c.key) from jsonb_each_text(case when jsonb_typeof(s.correlation_ids) = 'object' then s.correlation_ids else '{}'::jsonb end) c), '')),
       lower(s.id || ' ' || s.run_id || ' ' || coalesce((select string_agg(c.value, ' ' order by c.key) from jsonb_each_text(case when jsonb_typeof(s.correlation_ids) = 'object' then s.correlation_ids else '{}'::jsonb end) c), '')),
       (s.status in ('failed', 'timed_out') or s.error <> '')
  from run_steps s join runs r on r.id = s.run_id
 where s.created_at >= $1
union all
select 'request', m.id, '', '', m.status, m.model, trim(m.path || ' ' || m.client), m.error, m.correlation_ids, m.created_at, m.finished_at,
       lower(m.id || ' ' || m.model || ' ' || m.path || ' ' || m.client || ' ' || m.status || ' ' || m.error || ' ' || coalesce((select string_agg(c.value, ' ' order by c.key) from jsonb_each_text(case when jsonb_typeof(m.correlation_ids) = 'object' then m.correlation_ids else '{}'::jsonb end) c), '')),
       lower(m.id || ' ' || coalesce((select string_agg(c.value, ' ' order by c.key) from jsonb_each_text(case when jsonb_typeof(m.correlation_ids) = 'object' then m.correlation_ids else '{}'::jsonb end) c), '')),
       (m.status = 'failed' or m.error <> '')
  from model_broker_requests m
 where m.created_at >= $1`

func (s *PostgresStore) SearchEvidence(ctx context.Context, input EvidenceSearchInput) (EvidenceSearchPage, error) {
	input, err := normalizeEvidenceSearchInput(input)
	if err != nil {
		return EvidenceSearchPage{}, err
	}
	args := []any{input.RetainedSince}
	where := []string{}
	kinds := evidenceScopeKinds(input.Scope)
	placeholders := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		args = append(args, kind)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}
	where = append(where, "kind in ("+strings.Join(placeholders, ",")+")")
	if input.Scope == EvidenceScopeErrors {
		where = append(where, "is_error")
	}
	if input.Query != "" {
		column := "haystack"
		if input.Scope == EvidenceScopeCorrelations {
			column = "correlation_haystack"
		}
		args = append(args, "%"+escapeLikePattern(input.Query)+"%")
		where = append(where, fmt.Sprintf(`%s like $%d escape '\'`, column, len(args)))
	}
	if input.Status != "" {
		args = append(args, input.Status)
		where = append(where, fmt.Sprintf("lower(status) = $%d", len(args)))
	}
	if input.WorkflowID != "" {
		args = append(args, input.WorkflowID)
		where = append(where, fmt.Sprintf("workflow_id = $%d", len(args)))
	}
	if input.RunID != "" {
		args = append(args, input.RunID)
		where = append(where, fmt.Sprintf("((kind <> 'request' and run_id = $%d) or (kind = 'request' and exists (select 1 from jsonb_each_text(case when jsonb_typeof(correlation_ids) = 'object' then correlation_ids else '{}'::jsonb end) c where c.value = $%d)))", len(args), len(args)))
	}
	if input.BeforeCreated != nil {
		args = append(args, *input.BeforeCreated, input.BeforeKind, input.BeforeID)
		where = append(where, fmt.Sprintf("(created_at, kind, id) < ($%d, $%d, $%d)", len(args)-2, len(args)-1, len(args)))
	}
	args = append(args, input.Limit+1)
	query := `with hits as (` + evidenceSearchSourceSQL + `) select kind, id, run_id, workflow_id, status, title, summary, error, correlation_ids, created_at, finished_at from hits where ` + strings.Join(where, " and ") + fmt.Sprintf(` order by created_at desc, kind desc, id desc limit $%d`, len(args))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return EvidenceSearchPage{}, err
	}
	defer rows.Close()
	page := EvidenceSearchPage{Hits: []EvidenceHit{}}
	for rows.Next() {
		var hit EvidenceHit
		var correlation []byte
		var finished sql.NullTime
		if err := rows.Scan(&hit.Kind, &hit.ID, &hit.RunID, &hit.WorkflowID, &hit.Status, &hit.Title, &hit.Summary, &hit.Error, &correlation, &hit.CreatedAt, &finished); err != nil {
			return EvidenceSearchPage{}, err
		}
		if finished.Valid {
			value := finished.Time.UTC()
			hit.FinishedAt = &value
		}
		hit.CreatedAt = hit.CreatedAt.UTC()
		_ = json.Unmarshal(correlation, &hit.CorrelationIDs)
		if len(hit.CorrelationIDs) == 0 {
			hit.CorrelationIDs = nil
		}
		page.Hits = append(page.Hits, hit)
	}
	if err := rows.Err(); err != nil {
		return EvidenceSearchPage{}, err
	}
	if len(page.Hits) > input.Limit {
		page.HasMore = true
		page.Hits = page.Hits[:input.Limit]
	}
	return page, nil
}

func escapeLikePattern(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

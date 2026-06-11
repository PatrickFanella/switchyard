package persistence

import (
	"context"
	"time"
)

// StatsSummary is the admin ops/quality dashboard aggregate for a time range.
type StatsSummary struct {
	TotalRequests         int64 `json:"total_requests"`
	SuccessCount          int64 `json:"success_count"`
	ErrorCount            int64 `json:"error_count"`
	CacheHitCount         int64 `json:"cache_hit_count"`
	TotalPromptTokens     int64 `json:"total_prompt_tokens"`
	TotalCompletionTokens int64 `json:"total_completion_tokens"`
	ToolErrorCount        int64 `json:"tool_error_count"`
	MalformedCount        int64 `json:"malformed_count"`
	P50Ms                 int64 `json:"p50_ms"`
	P95Ms                 int64 `json:"p95_ms"`
	P99Ms                 int64 `json:"p99_ms"`
	QueueP95Ms            int64 `json:"queue_p95_ms"`
	UpstreamP95Ms         int64 `json:"upstream_p95_ms"`
}

func statsSummarySQL() string {
	return `
		select
			count(*) as total_requests,
			count(*) filter (where status='success') as success_count,
			count(*) filter (where status!='success') as error_count,
			count(*) filter (where status='cache_hit') as cache_hit_count,
			coalesce(sum(prompt_tokens),0)::bigint as total_prompt_tokens,
			coalesce(sum(completion_tokens),0)::bigint as total_completion_tokens,
			coalesce(sum(tool_error_count),0)::bigint as tool_error_count,
			count(*) filter (where response_json_valid = false) as malformed_count,
			coalesce(percentile_cont(0.50) within group (order by duration_ms),0)::bigint as p50_ms,
			coalesce(percentile_cont(0.95) within group (order by duration_ms),0)::bigint as p95_ms,
			coalesce(percentile_cont(0.99) within group (order by duration_ms),0)::bigint as p99_ms,
			coalesce(percentile_cont(0.95) within group (order by queue_ms),0)::bigint as queue_p95_ms,
			coalesce(percentile_cont(0.95) within group (order by upstream_ms),0)::bigint as upstream_p95_ms
		from broker.request_event
		where ts >= $1 and ts <= $2`
}

// StatsSummary returns aggregate request, latency, and quality metrics for a range.
func (s *Store) StatsSummary(ctx context.Context, since, until time.Time) (StatsSummary, error) {
	var out StatsSummary
	if s == nil || s.pool == nil {
		return out, nil
	}
	err := s.pool.QueryRow(ctx, statsSummarySQL(), since, until).Scan(
		&out.TotalRequests,
		&out.SuccessCount,
		&out.ErrorCount,
		&out.CacheHitCount,
		&out.TotalPromptTokens,
		&out.TotalCompletionTokens,
		&out.ToolErrorCount,
		&out.MalformedCount,
		&out.P50Ms,
		&out.P95Ms,
		&out.P99Ms,
		&out.QueueP95Ms,
		&out.UpstreamP95Ms,
	)
	return out, err
}

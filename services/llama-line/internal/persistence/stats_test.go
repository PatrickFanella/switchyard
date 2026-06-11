package persistence

import (
	"strings"
	"testing"
)

func TestStatsSummarySQLIncludesOpsAndQualityMetrics(t *testing.T) {
	sql := statsSummarySQL()
	for _, want := range []string{"count(*)", "percentile_cont(0.95)", "tool_error_count", "response_json_valid = false", "status", "upstream_ms", "prompt_tokens", "completion_tokens"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("stats SQL missing %q: %s", want, sql)
		}
	}
}

func TestStatsSummarySQLCountsOnlySuccessAsSuccess(t *testing.T) {
	sql := statsSummarySQL()
	for _, want := range []string{"status='success'", "status!='success'", "status='cache_hit'"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("stats SQL missing status contract %q: %s", want, sql)
		}
	}
	if strings.Contains(sql, "status in ('success','cache_hit','dedup_hit')") {
		t.Fatalf("stats SQL should not classify cache/dedup hits as success_count: %s", sql)
	}
}

func TestRequestEventInsertSQLIncludesTokenCounts(t *testing.T) {
	const want = "prompt_tokens=excluded.prompt_tokens"
	if !strings.Contains(insertRequestEventsSQL(), want) {
		t.Fatalf("request insert SQL missing %q", want)
	}
	if !strings.Contains(insertRequestEventsSQL(), "completion_tokens") {
		t.Fatalf("request insert SQL missing completion_tokens")
	}
}

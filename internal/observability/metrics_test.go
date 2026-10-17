package observability

import (
	"io"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestBoundedLabels(t *testing.T) {
	deleted := []string{}
	b := NewBoundedLabels(2, func(v string) { deleted = append(deleted, v) })
	if b.Label("a") != "a" || b.Label("b") != "b" || b.Label("c") != Other || b.Label("a") != "a" || b.Label("") != Other {
		t.Fatal("bound not applied")
	}
	b.Sync([]string{"b", "c"})
	if len(deleted) != 1 || deleted[0] != "a" || b.Label("c") != "c" || b.Label("a") != Other {
		t.Fatalf("sync: deleted %v", deleted)
	}
	if MCPMethod("tools/call") != "tools/call" || MCPMethod("x/"+strings.Repeat("y", 10)) != "other" {
		t.Fatal("methods")
	}
}

// The endpoint serves the documented families and never an ID-like label.
func TestMetricsContent(t *testing.T) {
	m := NewMetrics(nil)
	ObserveMCP("tools/call", "ok", 20*time.Millisecond)
	ObserveMCP("01234567-89ab-cdef-0123-456789abcdef", "ok", time.Millisecond)
	ObserveCall("support", "status", 15*time.Millisecond)
	ObserveHTTP("GET /v1/teams/{team}", "GET", 200, time.Millisecond)
	ObserveHTTP("", "GET", 404, time.Millisecond)
	MCPRateLimited.WithLabelValues("person_per_minute").Inc()
	ObserveProviderRefresh("github", "ok")
	SetLeader("retention", true)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	text := string(body)
	for _, family := range []string{"omp_build_info", "omp_mcp_requests_total", "omp_mcp_request_duration_seconds", "omp_mcp_call_duration_seconds",
		"omp_mcp_rate_limited_total", "omp_http_requests_total", "omp_http_request_duration_seconds", "omp_provider_token_refresh_total",
		"omp_loop_leader", "go_goroutines", `route="unmatched"`, `method="other"`, `code="2xx"`} {
		if !strings.Contains(text, family) {
			t.Errorf("missing %s", family)
		}
	}
	if regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}`).MatchString(text) {
		t.Error("an ID-like label value is exposed")
	}
}

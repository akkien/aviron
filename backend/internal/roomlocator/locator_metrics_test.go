package roomlocator

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

// newTestLocatorWithRegistry mirrors newTestLocator but also returns the
// registry its metrics were registered into, so a test can scrape it
// directly.
func newTestLocatorWithRegistry(t *testing.T, instanceID string) (*Locator, *prometheus.Registry) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })

	reg := prometheus.NewRegistry()
	return NewLocator(client, instanceID, reg), reg
}

func scrapeRegistry(t *testing.T, reg *prometheus.Registry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(rec, req)

	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatalf("read scrape body: %v", err)
	}
	return string(body)
}

func TestLocator_Claim_RecordsOkOutcomeMetric(t *testing.T) {
	ctx := context.Background()
	l, reg := newTestLocatorWithRegistry(t, "instance-a")

	if _, err := l.Claim(ctx, "race-1"); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	body := scrapeRegistry(t, reg)
	if !strings.Contains(body, `aviron_roomlocator_lookup_duration_seconds_count{op="claim",outcome="ok"} 1`) {
		t.Errorf("scrape output missing claim/ok observation\nfull output:\n%s", body)
	}
	if strings.Contains(body, `aviron_roomlocator_errors_total{op="claim"}`) {
		t.Errorf("expected no errors_total series for a successful claim\nfull output:\n%s", body)
	}
}

func TestLocator_Refresh_UnclaimedRecordsErrorMetric(t *testing.T) {
	ctx := context.Background()
	l, reg := newTestLocatorWithRegistry(t, "instance-a")

	if err := l.Refresh(ctx, "race-never-claimed"); err == nil {
		t.Fatal("expected an error refreshing a claim that was never made")
	}

	body := scrapeRegistry(t, reg)
	if !strings.Contains(body, `aviron_roomlocator_lookup_duration_seconds_count{op="refresh",outcome="error"} 1`) {
		t.Errorf("scrape output missing refresh/error observation\nfull output:\n%s", body)
	}
	if !strings.Contains(body, `aviron_roomlocator_errors_total{op="refresh"} 1`) {
		t.Errorf("scrape output missing aviron_roomlocator_errors_total{op=\"refresh\"} 1\nfull output:\n%s", body)
	}
}

func TestLocator_Owner_RecordsNotFoundOutcomeMetric(t *testing.T) {
	ctx := context.Background()
	l, reg := newTestLocatorWithRegistry(t, "instance-a")

	if _, ok, err := l.Owner(ctx, "race-1"); err != nil || ok {
		t.Fatalf("Owner = ok=%v, err=%v, want ok=false, err=nil", ok, err)
	}

	body := scrapeRegistry(t, reg)
	if !strings.Contains(body, `aviron_roomlocator_lookup_duration_seconds_count{op="owner",outcome="not_found"} 1`) {
		t.Errorf("scrape output missing owner/not_found observation\nfull output:\n%s", body)
	}
	if strings.Contains(body, `aviron_roomlocator_errors_total{op="owner"}`) {
		t.Errorf("expected no errors_total series for not_found — that's a normal outcome, not an error\nfull output:\n%s", body)
	}
}

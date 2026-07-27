package wsgateway

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	natsservertest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
)

func newHealthyRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	return client
}

func newHealthyNATS(t *testing.T) *nats.Conn {
	t.Helper()
	srv := natsservertest.RunRandClientPortServer()
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("nats.Connect: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func TestHealthz_OKWhenBothReachable(t *testing.T) {
	handler := NewHealthzHandler(newHealthyRedis(t), newHealthyNATS(t))

	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest("GET", "/healthz", nil))

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

// TestHealthz_ServiceUnavailableWhenRedisUnreachable confirms Redis alone
// being down is enough to fail the check, even with NATS healthy — not
// just both at once.
func TestHealthz_ServiceUnavailableWhenRedisUnreachable(t *testing.T) {
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer redisClient.Close()
	mr.Close() // Redis now unreachable; NATS stays fine.

	handler := NewHealthzHandler(redisClient, newHealthyNATS(t))

	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest("GET", "/healthz", nil))

	if w.Code != 503 {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

// TestHealthz_ServiceUnavailableWhenNATSUnreachable confirms NATS alone
// being down is enough to fail the check, even with Redis healthy.
func TestHealthz_ServiceUnavailableWhenNATSUnreachable(t *testing.T) {
	srv := natsservertest.RunRandClientPortServer()
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("nats.Connect: %v", err)
	}
	defer nc.Close()
	srv.Shutdown() // NATS now unreachable; Redis stays fine.

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && nc.Status() == nats.CONNECTED {
		time.Sleep(10 * time.Millisecond)
	}
	if nc.Status() == nats.CONNECTED {
		t.Fatal("setup failed: nats connection still reports CONNECTED after server shutdown")
	}

	handler := NewHealthzHandler(newHealthyRedis(t), nc)

	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest("GET", "/healthz", nil))

	if w.Code != 503 {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

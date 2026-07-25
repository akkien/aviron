package racerouter

import (
	"testing"
	"time"
)

func TestLoadConfig_MissingBackendsFailsFast(t *testing.T) {
	t.Setenv("RACE_SERVICE_INSTANCES", "")

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected an error when RACE_SERVICE_INSTANCES is unset")
	}
}

func TestLoadConfig_ParsesBackendsAndDefaults(t *testing.T) {
	t.Setenv("RACE_SERVICE_INSTANCES", "host1:8080, host2:8080 ,host3:8080")
	t.Setenv("RACE_ROUTER_LISTEN_ADDR", "")
	t.Setenv("REDIS_URL", "")
	t.Setenv("ROUTING_CACHE_TTL", "")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	wantBackends := []string{"host1:8080", "host2:8080", "host3:8080"}
	if len(cfg.Backends) != len(wantBackends) {
		t.Fatalf("Backends = %v, want %v", cfg.Backends, wantBackends)
	}
	for i, b := range wantBackends {
		if cfg.Backends[i] != b {
			t.Errorf("Backends[%d] = %q, want %q", i, cfg.Backends[i], b)
		}
	}

	if cfg.ListenAddr != ":8090" {
		t.Errorf("ListenAddr = %q, want :8090 default", cfg.ListenAddr)
	}
	if cfg.RedisURL != "redis://localhost:6379/0" {
		t.Errorf("RedisURL = %q, want default", cfg.RedisURL)
	}
	if cfg.CacheTTL != 30*time.Second {
		t.Errorf("CacheTTL = %v, want 30s default", cfg.CacheTTL)
	}
}

func TestLoadConfig_OverridesFromEnv(t *testing.T) {
	t.Setenv("RACE_SERVICE_INSTANCES", "host1:8080")
	t.Setenv("RACE_ROUTER_LISTEN_ADDR", ":9999")
	t.Setenv("REDIS_URL", "redis://example.com:6379/1")
	t.Setenv("ROUTING_CACHE_TTL", "5s")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.ListenAddr != ":9999" {
		t.Errorf("ListenAddr = %q, want :9999", cfg.ListenAddr)
	}
	if cfg.RedisURL != "redis://example.com:6379/1" {
		t.Errorf("RedisURL = %q, want override", cfg.RedisURL)
	}
	if cfg.CacheTTL != 5*time.Second {
		t.Errorf("CacheTTL = %v, want 5s", cfg.CacheTTL)
	}
}

func TestLoadConfig_InvalidCacheTTLFallsBackToDefault(t *testing.T) {
	t.Setenv("RACE_SERVICE_INSTANCES", "host1:8080")
	t.Setenv("ROUTING_CACHE_TTL", "not-a-duration")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.CacheTTL != 30*time.Second {
		t.Errorf("CacheTTL = %v, want 30s default on invalid value", cfg.CacheTTL)
	}
}

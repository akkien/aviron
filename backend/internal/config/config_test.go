package config

import "testing"

func TestGetEnvBool(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		setEnv   bool
		fallback bool
		want     bool
	}{
		{"unset uses fallback true", "", false, true, true},
		{"unset uses fallback false", "", false, false, false},
		{"true", "true", true, false, true},
		{"false", "false", true, true, false},
		{"invalid value falls back", "not-a-bool", true, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const key = "TEST_GETENVBOOL_VAR"
			if tt.setEnv {
				t.Setenv(key, tt.envValue)
			}

			if got := getEnvBool(key, tt.fallback); got != tt.want {
				t.Errorf("getEnvBool(%q, %v) = %v, want %v", key, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestLoad_PprofEnabledDefaultsTrue(t *testing.T) {
	cfg := Load()
	if !cfg.PprofEnabled {
		t.Error("PprofEnabled = false, want true when PPROF_ENABLED is unset")
	}
}

func TestLoad_PprofEnabledRespectsEnv(t *testing.T) {
	t.Setenv("PPROF_ENABLED", "false")

	cfg := Load()
	if cfg.PprofEnabled {
		t.Error("PprofEnabled = true, want false when PPROF_ENABLED=false")
	}
}

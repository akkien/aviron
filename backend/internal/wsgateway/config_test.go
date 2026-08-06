package wsgateway

import "testing"

func TestLoadConfig_PprofEnabledDefaultsTrue(t *testing.T) {
	t.Setenv("RACE_SERVICE_INSTANCES", "localhost:8080")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.PprofEnabled {
		t.Error("PprofEnabled = false, want true when PPROF_ENABLED is unset")
	}
}

func TestLoadConfig_PprofEnabledRespectsEnv(t *testing.T) {
	t.Setenv("RACE_SERVICE_INSTANCES", "localhost:8080")
	t.Setenv("PPROF_ENABLED", "false")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PprofEnabled {
		t.Error("PprofEnabled = true, want false when PPROF_ENABLED=false")
	}
}

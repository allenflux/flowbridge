package main

import "testing"

func TestApplyEnvOverridesAllowsZeroSubmitRetries(t *testing.T) {
	t.Setenv("FLOWBRIDGE_MAX_SUBMIT_RETRIES", "0")
	cfg := defaultConfig()
	applyEnvOverrides(&cfg)
	if cfg.MaxSubmitRetries != 0 {
		t.Fatalf("MaxSubmitRetries = %d, want 0", cfg.MaxSubmitRetries)
	}
}

package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLoadWritesDefaultsWhenMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 8799 || cfg.ProbeInterval != 5*time.Second {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	// File should now exist and reload identically.
	cfg2, err := Load(p)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg2.Port != cfg.Port {
		t.Fatalf("reload mismatch: %+v vs %+v", cfg2, cfg)
	}
}

func TestApplyDefaultsFillsMissingFields(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	// Write a partial config (only port set).
	if err := Save(p, Config{Port: 9000}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 9000 {
		t.Fatalf("port not preserved: %d", cfg.Port)
	}
	if len(cfg.Targets) == 0 || cfg.ProbeInterval == 0 {
		t.Fatalf("defaults not applied to partial config: %+v", cfg)
	}
}

// Package config loads and persists netqa's YAML configuration and resolves the
// data directory (database + config) under the macOS Application Support path.
package config

import (
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the user-tunable configuration.
type Config struct {
	// Targets are the internet hosts probed for reachability/latency.
	Targets []string `yaml:"targets"`
	// DNSServers are resolvers probed for DNS health.
	DNSServers []string `yaml:"dns_servers"`
	// DNSHost is the hostname resolved during DNS health checks.
	DNSHost string `yaml:"dns_host"`
	// ProbeInterval is the cadence of reachability probes.
	ProbeInterval time.Duration `yaml:"probe_interval"`
	// WindowSize is how many recent probes feed the rolling stats.
	WindowSize int `yaml:"window_size"`
	// DNSInterval is the cadence of DNS health checks.
	DNSInterval time.Duration `yaml:"dns_interval"`
	// ThroughputCheckInterval is how often the link is evaluated for an idle
	// window in which to run a speed test.
	ThroughputCheckInterval time.Duration `yaml:"throughput_check_interval"`
	// ThroughputMinGap is the minimum time between two automatic speed tests,
	// so a long idle period does not trigger back-to-back tests.
	ThroughputMinGap time.Duration `yaml:"throughput_min_gap"`
	// ThroughputIdleMbit is the X in "run a test whenever the link is using less
	// than X Mbit". A test is skipped when current traffic exceeds this.
	ThroughputIdleMbit float64 `yaml:"throughput_idle_mbit"`
	// Port is the local dashboard HTTP port.
	Port int `yaml:"port"`
	// Alerts enables macOS notifications on confirmed outages.
	Alerts bool `yaml:"alerts"`
}

// Default returns a sane default configuration.
func Default() Config {
	return Config{
		Targets:            []string{"1.1.1.1", "8.8.8.8", "9.9.9.9"},
		DNSServers:         []string{"1.1.1.1", "8.8.8.8"},
		DNSHost:            "example.com",
		ProbeInterval:      5 * time.Second,
		WindowSize:         60,
		DNSInterval:             60 * time.Second,
		ThroughputCheckInterval: 2 * time.Minute,
		ThroughputMinGap:        5 * time.Minute,
		ThroughputIdleMbit:      3.0,
		Port:                    8799,
		Alerts:                  true,
	}
}

// DataDir returns the netqa data directory, creating it if needed.
func DataDir() (string, error) {
	base, err := os.UserConfigDir() // ~/Library/Application Support on macOS
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "netqa")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// ConfigPath returns the config.yaml path inside the data dir.
func ConfigPath(dir string) string { return filepath.Join(dir, "config.yaml") }

// DBPath returns the database path inside the data dir.
func DBPath(dir string) string { return filepath.Join(dir, "netqa.db") }

// Load reads config from path, returning defaults (and writing them) when the
// file does not yet exist. Unset fields fall back to defaults.
func Load(path string) (Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		_ = Save(path, cfg)
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	cfg.applyDefaults()
	return cfg, nil
}

// Save writes config as YAML to path.
func Save(path string, cfg Config) error {
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func (c *Config) applyDefaults() {
	d := Default()
	if len(c.Targets) == 0 {
		c.Targets = d.Targets
	}
	if len(c.DNSServers) == 0 {
		c.DNSServers = d.DNSServers
	}
	if c.DNSHost == "" {
		c.DNSHost = d.DNSHost
	}
	if c.ProbeInterval == 0 {
		c.ProbeInterval = d.ProbeInterval
	}
	if c.WindowSize == 0 {
		c.WindowSize = d.WindowSize
	}
	if c.DNSInterval == 0 {
		c.DNSInterval = d.DNSInterval
	}
	if c.ThroughputCheckInterval == 0 {
		c.ThroughputCheckInterval = d.ThroughputCheckInterval
	}
	if c.ThroughputMinGap == 0 {
		c.ThroughputMinGap = d.ThroughputMinGap
	}
	if c.ThroughputIdleMbit == 0 {
		c.ThroughputIdleMbit = d.ThroughputIdleMbit
	}
	if c.Port == 0 {
		c.Port = d.Port
	}
}

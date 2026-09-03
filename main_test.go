package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serge-r/cloud-route-manager/internal/config"
)

const testConfig = `
general:
  interval: 10m
  log-severity: info
  dry-run: false
source:
  static:
    routes: [1.1.1.1]
destination:
  route-table-ids: [rtb-1]
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFlagsOverrideTheConfiguredValues(t *testing.T) {
	opts := newOptions(os.Stderr)
	if err := opts.fs.Parse([]string{
		"-log-severity", "debug",
		"-log-format", "json",
		"-dry-run",
		"-cloud", "yandex",
		"-interface", "eth1",
		"-ip-address", "10.0.0.9",
		"-interval", "30s",
		"-timeout", "45s",
		"-action-timeout", "5s",
	}); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Parse([]byte(testConfig))
	if err != nil {
		t.Fatal(err)
	}
	opts.applyTo(&cfg)

	if cfg.General.LogSeverity != "debug" || cfg.General.LogFormat != "json" {
		t.Errorf("logging overrides not applied: %+v", cfg.General)
	}
	if !cfg.General.DryRun {
		t.Error("dry-run override not applied")
	}
	if cfg.General.Cloud != "yandex" || cfg.General.Interface != "eth1" || cfg.General.IPAddress != "10.0.0.9" {
		t.Errorf("placement overrides not applied: %+v", cfg.General)
	}
	if cfg.General.Interval.Duration() != 30*time.Second ||
		cfg.General.Timeout.Duration() != 45*time.Second ||
		cfg.General.ActionTimeout.Duration() != 5*time.Second {
		t.Errorf("duration overrides not applied: %+v", cfg.General)
	}
}

func TestUnsetFlagsKeepTheConfiguredValues(t *testing.T) {
	opts := newOptions(os.Stderr)
	if err := opts.fs.Parse(nil); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Parse([]byte(testConfig))
	if err != nil {
		t.Fatal(err)
	}
	opts.applyTo(&cfg)

	if cfg.General.LogSeverity != "info" {
		t.Errorf("log-severity = %q, want the configured info", cfg.General.LogSeverity)
	}
	if cfg.General.Interval.Duration() != 10*time.Minute {
		t.Errorf("interval = %v, want the configured 10m", cfg.General.Interval)
	}
	if cfg.General.DryRun {
		t.Error("dry-run must stay false when the flag is not given")
	}
}

func TestOnceClearsTheInterval(t *testing.T) {
	opts := newOptions(os.Stderr)
	if err := opts.fs.Parse([]string{"-once"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse([]byte(testConfig))
	if err != nil {
		t.Fatal(err)
	}
	opts.applyTo(&cfg)
	if cfg.General.Interval.Duration() != 0 {
		t.Errorf("interval = %v, want 0", cfg.General.Interval)
	}
}

// Every general.* key must have a flag of the same name, otherwise the two
// ways of configuring the service drift apart.
func TestEveryGeneralKeyHasAMatchingFlag(t *testing.T) {
	opts := newOptions(os.Stderr)
	flags := map[string]bool{}
	opts.fs.VisitAll(func(f *flag.Flag) { flags[f.Name] = true })

	for _, key := range config.GeneralKeys() {
		if !flags[key] {
			t.Errorf("general.%s has no matching command line flag", key)
		}
	}
}

func TestCheckConfigValidatesOverridesToo(t *testing.T) {
	path := writeConfig(t, testConfig)

	var out bytes.Buffer
	if err := run([]string{"-config", path, "-check-config"}, &out, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), "ok") {
		t.Errorf("output = %q, want a confirmation", out.String())
	}

	out.Reset()
	err := run([]string{"-config", path, "-check-config", "-log-severity", "loud"}, &out, &out)
	if err == nil || !strings.Contains(err.Error(), "log-severity") {
		t.Fatalf("run error = %v, want a complaint about log-severity", err)
	}
}

func TestVersionFlag(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"-version"}, &out, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), version) {
		t.Errorf("output = %q, want the version", out.String())
	}
}

func TestMissingConfigIsReported(t *testing.T) {
	var out bytes.Buffer
	err := run([]string{"-config", filepath.Join(t.TempDir(), "absent.yml")}, &out, &out)
	if err == nil {
		t.Fatal("run succeeded with a missing configuration file")
	}
}

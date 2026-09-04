package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const minimal = `
general:
  interval: 30s
source:
  static:
    routes:
      - 1.1.1.1
destination:
  route-table-ids:
    - rtb-1
`

func TestParseAppliesDefaults(t *testing.T) {
	cfg, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := cfg.General.Interval.Duration(); got != 30*time.Second {
		t.Errorf("interval = %v, want 30s", got)
	}
	if cfg.General.LogSeverity != "info" || cfg.General.LogFile != "stdout" {
		t.Errorf("defaults not applied: %+v", cfg.General)
	}
	if cfg.General.Cloud != CloudAuto {
		t.Errorf("cloud = %q, want auto", cfg.General.Cloud)
	}
	if cfg.General.ActionTimeout.Duration() != time.Minute {
		t.Errorf("action-timeout = %v, want 1m", cfg.General.ActionTimeout)
	}
}

func TestLoadExampleConfig(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.yml")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%s): %v", path, err)
	}
	if len(cfg.Destination.RouteTableIDs) == 0 {
		t.Error("example config has no route tables")
	}
	if cfg.Source.Static == nil {
		t.Error("example config has no enabled source")
	}
	if cfg.General.LogSeverity != "info" || cfg.General.Cloud != CloudAuto {
		t.Errorf("example config does not spell out the defaults: %+v", cfg.General)
	}
}

// full covers every key the example config documents, including the ones it
// keeps commented out.
const full = `
general:
  interval: 10m
  log-severity: debug
  log-file: /var/log/crm.log
  log-format: json
  dry-run: true
  cloud: yandex
  interface: eth0
  ip-address: 10.0.0.5
  timeout: 2m
  action-timeout: 30s
  remove-stale-local-routes: true
local-static-routes:
  - "192.168.0.0/24 via default"
  - "10.10.0.0/16 via 1.1.1.1"
  - "172.16.5.0/24 via blackhole"
source:
  static:
    routes: [1.1.1.1, 2.2.2.2/32]
  file:
    path: /etc/routes.txt
    optional: true
  s3:
    region: eu-north-1
    bucket: configs
    path: data/routes.cfg
    endpoint: https://storage.yandexcloud.net
    use-path-style: true
  aws-ssm:
    region: eu-north-1
    path: /zerotier/routes
  yandex-instance-metadata:
    key: routes
destination:
  route-table-ids: [rtb-1, rtb-2]
actions:
  run-on-start: true
  success: ["echo ${routes}"]
  failed: ["echo failed"]
`

func TestParseEveryDocumentedKey(t *testing.T) {
	cfg, err := Parse([]byte(full))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.General.LogSeverity != "debug" || !cfg.General.DryRun || cfg.General.Cloud != CloudYandex {
		t.Errorf("general not decoded: %+v", cfg.General)
	}
	if cfg.Source.Static == nil || cfg.Source.File == nil || cfg.Source.S3 == nil ||
		cfg.Source.AWSSSM == nil || cfg.Source.YandexMetadata == nil {
		t.Errorf("not every source was decoded: %+v", cfg.Source)
	}
	if !cfg.Source.File.Optional || !cfg.Source.S3.UsePathStyle {
		t.Errorf("source flags not decoded: %+v", cfg.Source)
	}
	if !cfg.Actions.RunOnStart || len(cfg.Actions.Success) != 1 || len(cfg.Actions.Failed) != 1 {
		t.Errorf("actions not decoded: %+v", cfg.Actions)
	}
	if len(cfg.LocalStaticRoutes) != 3 || !cfg.General.RemoveStaleLocalRoutes {
		t.Errorf("local static routes not decoded: %+v", cfg.LocalStaticRoutes)
	}
}

func TestValidateRejectsBadLocalStaticRoutes(t *testing.T) {
	for _, bad := range []string{
		"192.168.0.0/24",
		"192.168.0.0/24 via",
		"192.168.0.0/24 through 1.1.1.1",
		"not-a-prefix via default",
		"192.168.0.0/24 via not-an-ip",
		"192.168.0.0/24 via 2001:db8::1",
	} {
		body := minimal + "local-static-routes:\n  - \"" + bad + "\"\n"
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("Parse accepted the invalid local route %q", bad)
		}
	}
}

func TestGeneralKeysMatchTheYAMLTags(t *testing.T) {
	want := []string{
		"interval", "log-severity", "log-file", "log-format", "dry-run",
		"cloud", "interface", "ip-address", "timeout", "action-timeout",
		"remove-stale-local-routes",
	}
	got := GeneralKeys()
	if len(got) != len(want) {
		t.Fatalf("GeneralKeys() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("GeneralKeys() = %v, want %v", got, want)
		}
	}
}

func TestParseRejectsBadConfigs(t *testing.T) {
	cases := map[string]string{
		"no sources":     "general:\n  log-severity: info\ndestination:\n  route-table-ids: [rtb-1]\n",
		"no tables":      "source:\n  static:\n    routes: [1.1.1.1]\n",
		"bad severity":   strings.Replace(minimal, "interval: 30s", "log-severity: loud", 1),
		"unknown field":  minimal + "\nnonsense: true\n",
		"empty ssm path": "source:\n  aws-ssm:\n    path: \"\"\ndestination:\n  route-table-ids: [rtb-1]\n",
	}
	for name, body := range cases {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("%s: Parse succeeded, want an error", name)
		}
	}
}

func TestDurationAcceptsBareSeconds(t *testing.T) {
	cfg, err := Parse([]byte(strings.Replace(minimal, "interval: 30s", "interval: 45", 1)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := cfg.General.Interval.Duration(); got != 45*time.Second {
		t.Errorf("interval = %v, want 45s", got)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yml")); err == nil {
		t.Fatal("Load succeeded for a missing file")
	}
}

func TestLoadFromDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(minimal), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

package actions

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testVars() Vars {
	return Vars{
		Routes:    []string{"1.1.1.1/32", "10.0.0.0/8"},
		IPAddress: "192.168.1.10",
		Interface: "eth0",
		Cloud:     "aws",
	}
}

func TestExpand(t *testing.T) {
	cases := map[string]string{
		"/bin/reload.sh ${ip-address}":  "/bin/reload.sh 192.168.1.10",
		"echo ${routes}":                "echo 1.1.1.1/32,10.0.0.0/8",
		"x ${interface} ${cloud}":       "x eth0 aws",
		"count=${routes-count}":         "count=2",
		"keep ${UNKNOWN_PLACEHOLDER}":   "keep ${UNKNOWN_PLACEHOLDER}",
		"no placeholders here at all":   "no placeholders here at all",
		"${ip_address} also works fine": "192.168.1.10 also works fine",
	}
	for in, want := range cases {
		if got := Expand(in, testVars()); got != want {
			t.Errorf("Expand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRunnerExecutesCommandsAndExposesEnv(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.txt")

	r := &Runner{Log: slog.New(slog.NewTextHandler(os.Stderr, nil)), Timeout: 10 * time.Second}
	err := r.Run(context.Background(), "success", []string{
		"printf '%s|%s' \"${ip-address}\" \"$CRM_ROUTES\" > " + out,
	}, testVars())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if want := "192.168.1.10|1.1.1.1/32,10.0.0.0/8"; string(data) != want {
		t.Fatalf("command output = %q, want %q", data, want)
	}
}

func TestRunnerReportsFailuresAndKeepsGoing(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "second")

	r := &Runner{Log: slog.New(slog.NewTextHandler(os.Stderr, nil)), Timeout: 10 * time.Second}
	err := r.Run(context.Background(), "failed", []string{"exit 3", "touch " + marker}, testVars())
	if err == nil || !strings.Contains(err.Error(), "exit 3") {
		t.Fatalf("Run error = %v, want it to mention the failing command", err)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("second command did not run: %v", statErr)
	}
}

func TestRunnerWithoutCommandsDoesNothing(t *testing.T) {
	r := &Runner{Log: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	if err := r.Run(context.Background(), "success", nil, testVars()); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

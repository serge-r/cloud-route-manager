package app

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/serge-r/cloud-route-manager/internal/actions"
	"github.com/serge-r/cloud-route-manager/internal/cloud"
	"github.com/serge-r/cloud-route-manager/internal/config"
	"github.com/serge-r/cloud-route-manager/internal/source"
)

type fakeManager struct {
	changes []cloud.Change
	err     error
	calls   int
	lastIP  netip.Addr
}

func (f *fakeManager) Provider() cloud.Provider { return cloud.ProviderAWS }

func (f *fakeManager) Sync(_ context.Context, _ []string, _ []netip.Prefix, nextHop netip.Addr, _ bool) ([]cloud.Change, error) {
	f.calls++
	f.lastIP = nextHop
	return f.changes, f.err
}

// newTestApp builds an App around a fake cloud manager, with the primary
// address pinned so the test does not depend on the host's routing table.
func newTestApp(t *testing.T, mgr cloud.Manager, cfg config.Config) *App {
	t.Helper()
	cfg.General.IPAddress = "10.1.2.3"
	if cfg.Source.Static == nil {
		cfg.Source.Static = &config.StaticSource{Routes: []string{"1.1.1.1", "2.2.2.2"}}
	}
	cfg.Destination.RouteTableIDs = []string{"rtb-1"}

	sources, err := source.Build(cfg.Source)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	return &App{
		cfg:        cfg,
		log:        log,
		sources:    sources,
		manager:    mgr,
		runner:     &actions.Runner{Log: log, Timeout: 10 * time.Second},
		trigger:    make(chan struct{}, 1),
		firstCycle: true,
	}
}

func TestRunOnceRunsSuccessActionsWhenRoutesChange(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "success")
	cfg := config.Defaults()
	cfg.Actions.Success = []string{"echo ${ip-address} ${routes} > " + marker}
	cfg.Actions.Failed = []string{"exit 1"}

	mgr := &fakeManager{changes: []cloud.Change{
		{Table: "rtb-1", Prefix: "1.1.1.1/32", Action: cloud.ActionCreate},
	}}
	if err := newTestApp(t, mgr, cfg).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if mgr.lastIP.String() != "10.1.2.3" {
		t.Errorf("sync used next hop %s, want 10.1.2.3", mgr.lastIP)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("success action did not run: %v", err)
	}
	if want := "10.1.2.3 1.1.1.1/32,2.2.2.2/32\n"; string(data) != want {
		t.Errorf("success action wrote %q, want %q", data, want)
	}
}

func TestRunOnceStaysQuietWhenNothingChanges(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "success")
	cfg := config.Defaults()
	cfg.Actions.Success = []string{"touch " + marker}

	mgr := &fakeManager{changes: []cloud.Change{
		{Table: "rtb-1", Prefix: "1.1.1.1/32", Action: cloud.ActionNoop},
	}}
	if err := newTestApp(t, mgr, cfg).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("success actions must not run when nothing changed")
	}
}

func TestRunOnceRunsSuccessActionsOnStartWhenAsked(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "success")
	cfg := config.Defaults()
	cfg.Actions.RunOnStart = true
	cfg.Actions.Success = []string{"touch " + marker}

	app := newTestApp(t, &fakeManager{}, cfg)
	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("run-on-start did not fire the success actions: %v", err)
	}

	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("run-on-start must only fire on the first cycle")
	}
}

func TestRunOnceRunsFailedActionsOnSyncError(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "failed")
	cfg := config.Defaults()
	cfg.Actions.Failed = []string{"echo ${cloud} > " + marker}

	err := newTestApp(t, &fakeManager{err: errors.New("access denied")}, cfg).RunOnce(context.Background())
	if err == nil {
		t.Fatal("RunOnce: expected the sync error to be returned")
	}
	data, readErr := os.ReadFile(marker)
	if readErr != nil {
		t.Fatalf("failed action did not run: %v", readErr)
	}
	if want := "aws\n"; string(data) != want {
		t.Errorf("failed action wrote %q, want %q", data, want)
	}
}

func TestRunOnceFailsWhenNoRoutesAreFound(t *testing.T) {
	cfg := config.Defaults()
	cfg.Source.Static = &config.StaticSource{Routes: []string{}}
	cfg.Source.File = &config.FileSource{Path: filepath.Join(t.TempDir(), "absent"), Optional: true}

	mgr := &fakeManager{}
	if err := newTestApp(t, mgr, cfg).RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce: expected an error when no routes were collected")
	}
	if mgr.calls != 0 {
		t.Error("route tables must not be touched when the source list is empty")
	}
}

func TestRunOnceDryRunDoesNotFireSuccessActions(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "success")
	cfg := config.Defaults()
	cfg.General.DryRun = true
	cfg.Actions.Success = []string{"touch " + marker}

	mgr := &fakeManager{changes: []cloud.Change{
		{Table: "rtb-1", Prefix: "1.1.1.1/32", Action: cloud.ActionCreate},
	}}
	if err := newTestApp(t, mgr, cfg).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("dry-run must not fire the success actions")
	}
}

func TestRunSingleCycleWhenIntervalIsZero(t *testing.T) {
	cfg := config.Defaults()
	cfg.General.Interval = 0

	mgr := &fakeManager{}
	if err := newTestApp(t, mgr, cfg).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mgr.calls != 1 {
		t.Fatalf("sync called %d times, want exactly one cycle", mgr.calls)
	}
}

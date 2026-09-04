package app

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serge-r/cloud-route-manager/internal/actions"
	"github.com/serge-r/cloud-route-manager/internal/cloud"
	"github.com/serge-r/cloud-route-manager/internal/config"
	"github.com/serge-r/cloud-route-manager/internal/localroutes"
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

type fakeLocal struct {
	changes   []cloud.Change
	err       error
	calls     int
	gotSpecs  []localroutes.Spec
	gotStale  bool
	gotDryRun bool
}

func (f *fakeLocal) Sync(_ context.Context, specs []localroutes.Spec, removeStale, dryRun bool) ([]cloud.Change, error) {
	f.calls++
	f.gotSpecs, f.gotStale, f.gotDryRun = specs, removeStale, dryRun
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

// withLocal attaches a fake local route manager to an app.
func withLocal(t *testing.T, app *App, local localManager, entries ...string) *App {
	t.Helper()
	specs, err := localroutes.ParseSpecs(entries)
	if err != nil {
		t.Fatal(err)
	}
	app.local, app.localSpecs = local, specs
	return app
}

func TestLocalRouteChangesTriggerSuccessActions(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "success")
	cfg := config.Defaults()
	cfg.General.RemoveStaleLocalRoutes = true
	cfg.Actions.Success = []string{"touch " + marker}

	local := &fakeLocal{changes: []cloud.Change{
		{Table: "local", Prefix: "192.168.0.0/24", Action: cloud.ActionCreate},
	}}
	app := withLocal(t, newTestApp(t, &fakeManager{}, cfg), local, "192.168.0.0/24 via default")

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if local.calls != 1 || len(local.gotSpecs) != 1 || !local.gotStale || local.gotDryRun {
		t.Fatalf("local manager called with %+v, stale=%v dry-run=%v", local.gotSpecs, local.gotStale, local.gotDryRun)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("a local route change must fire the success actions: %v", err)
	}
}

func TestLocalRouteFailureDoesNotSkipTheCloud(t *testing.T) {
	cfg := config.Defaults()
	mgr := &fakeManager{}
	app := withLocal(t, newTestApp(t, mgr, cfg),
		&fakeLocal{err: errors.New("RTNETLINK answers: Network is unreachable")},
		"192.168.0.0/24 via 1.1.1.1")

	err := app.RunOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "local static routes") {
		t.Fatalf("RunOnce error = %v, want the local failure reported", err)
	}
	if mgr.calls != 1 {
		t.Error("the cloud route tables must still be updated when the local step fails")
	}
}

func TestLocalRoutesRunEvenWithoutCollectedRoutes(t *testing.T) {
	cfg := config.Defaults()
	cfg.Source.Static = &config.StaticSource{Routes: []string{}}

	local := &fakeLocal{}
	app := withLocal(t, newTestApp(t, &fakeManager{}, cfg), local, "192.168.0.0/24 via blackhole")

	if err := app.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce: expected the empty source list to be reported")
	}
	if local.calls != 1 {
		t.Error("local static routes must be applied even when no source yielded a route")
	}
}

func TestLocalRoutesHonourDryRun(t *testing.T) {
	cfg := config.Defaults()
	cfg.General.DryRun = true

	local := &fakeLocal{}
	app := withLocal(t, newTestApp(t, &fakeManager{}, cfg), local, "192.168.0.0/24 via blackhole")

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !local.gotDryRun {
		t.Error("dry-run was not passed down to the local route manager")
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

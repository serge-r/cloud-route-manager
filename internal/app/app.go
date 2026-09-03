// Package app wires the configuration, sources, cloud managers and actions
// into the scan-and-update loop.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/serge-r/cloud-route-manager/internal/actions"
	"github.com/serge-r/cloud-route-manager/internal/cloud"
	awscloud "github.com/serge-r/cloud-route-manager/internal/cloud/aws"
	yandexcloud "github.com/serge-r/cloud-route-manager/internal/cloud/yandex"
	"github.com/serge-r/cloud-route-manager/internal/config"
	"github.com/serge-r/cloud-route-manager/internal/netinfo"
	"github.com/serge-r/cloud-route-manager/internal/routes"
	"github.com/serge-r/cloud-route-manager/internal/source"
)

// App holds everything a cycle needs.
type App struct {
	cfg     config.Config
	log     *slog.Logger
	sources []source.Source
	manager cloud.Manager
	runner  *actions.Runner

	// trigger asks the loop for an immediate out-of-schedule cycle.
	trigger chan struct{}
	// firstCycle lets actions.run-on-start fire once after startup.
	firstCycle bool
}

// New prepares the service: it detects the cloud, builds the cloud manager
// and validates the configured sources.
func New(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	sources, err := source.Build(cfg.Source)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(sources))
	for _, s := range sources {
		names = append(names, s.Name())
	}
	log.Info("sources configured", "sources", strings.Join(names, ", "))

	provider, err := resolveProvider(ctx, cfg.General.Cloud, log)
	if err != nil {
		return nil, err
	}
	log.Info("cloud detected", "provider", string(provider))

	manager, err := newManager(ctx, provider, log)
	if err != nil {
		return nil, err
	}

	return &App{
		cfg:     cfg,
		log:     log,
		sources: sources,
		manager: manager,
		runner: &actions.Runner{
			Log:     log,
			Timeout: cfg.General.ActionTimeout.Duration(),
		},
		trigger:    make(chan struct{}, 1),
		firstCycle: true,
	}, nil
}

// NotifyOnSignal makes the given signals request an immediate cycle, which is
// how an operator forces a refresh without restarting the service.
func (a *App) NotifyOnSignal(ctx context.Context, sigs ...os.Signal) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, sigs...)
	go func() {
		defer signal.Stop(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				a.Trigger()
			}
		}
	}()
}

// Trigger schedules an immediate cycle. It never blocks: if a refresh is
// already pending, the request is dropped.
func (a *App) Trigger() {
	select {
	case a.trigger <- struct{}{}:
	default:
	}
}

// resolveProvider honours general.cloud and falls back to autodetection.
func resolveProvider(ctx context.Context, configured string, log *slog.Logger) (cloud.Provider, error) {
	switch strings.ToLower(strings.TrimSpace(configured)) {
	case config.CloudAWS:
		return cloud.ProviderAWS, nil
	case config.CloudYandex:
		return cloud.ProviderYandex, nil
	}
	log.Debug("detecting the cloud provider from instance metadata")
	return cloud.Detect(ctx)
}

func newManager(ctx context.Context, provider cloud.Provider, log *slog.Logger) (cloud.Manager, error) {
	switch provider {
	case cloud.ProviderAWS:
		return awscloud.NewManager(ctx, log)
	case cloud.ProviderYandex:
		return yandexcloud.NewManager(ctx, log)
	default:
		return nil, fmt.Errorf("unsupported cloud provider %q", provider)
	}
}

// Run executes cycles until the context is cancelled. A non-positive
// general.interval means a single cycle, after which Run returns.
func (a *App) Run(ctx context.Context) error {
	interval := a.cfg.General.Interval.Duration()

	err := a.RunOnce(ctx)
	if interval <= 0 {
		a.log.Info("interval is not set, exiting after a single cycle")
		return err
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	a.log.Info("watching for route changes", "interval", interval.String())

	for {
		select {
		case <-ctx.Done():
			a.log.Info("shutting down")
			return nil
		case <-a.trigger:
			a.log.Info("refresh requested")
			if err := a.RunOnce(ctx); err != nil {
				a.log.Debug("cycle finished with errors", "error", err)
			}
			ticker.Reset(interval)
		case <-ticker.C:
			if err := a.RunOnce(ctx); err != nil {
				a.log.Debug("cycle finished with errors", "error", err)
			}
		}
	}
}

// RunOnce performs a full scan-and-update cycle and runs the matching
// actions. The returned error describes the cycle failure, if any; the
// actions have already been executed by then.
func (a *App) RunOnce(ctx context.Context) error {
	first := a.firstCycle
	a.firstCycle = false

	if t := a.cfg.General.Timeout.Duration(); t > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t)
		defer cancel()
	}

	changed, prefixes, primary, cycleErr := a.cycle(ctx)

	vars := actions.Vars{
		Routes:    routes.Strings(prefixes),
		IPAddress: primary.IP.String(),
		Interface: primary.Interface,
		Cloud:     string(a.manager.Provider()),
	}
	if !primary.IP.IsValid() {
		vars.IPAddress = ""
	}

	// Actions are deliberately run on a background context: a cancelled or
	// timed out cycle must still be able to report the failure.
	actionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.actionsBudget())
	defer cancel()

	switch {
	case cycleErr != nil:
		a.log.Error("update cycle failed", "error", cycleErr)
		if err := a.runner.Run(actionCtx, "failed", a.cfg.Actions.Failed, vars); err != nil {
			a.log.Error("failed actions reported errors", "error", err)
		}
	case changed || (first && a.cfg.Actions.RunOnStart):
		if err := a.runner.Run(actionCtx, "success", a.cfg.Actions.Success, vars); err != nil {
			a.log.Error("success actions reported errors", "error", err)
		}
	default:
		a.log.Debug("nothing changed, no actions to run")
	}

	return cycleErr
}

// actionsBudget bounds the whole action batch.
func (a *App) actionsBudget() time.Duration {
	per := a.cfg.General.ActionTimeout.Duration()
	if per <= 0 {
		per = time.Minute
	}
	count := len(a.cfg.Actions.Success) + len(a.cfg.Actions.Failed)
	if count == 0 {
		count = 1
	}
	return per * time.Duration(count)
}

// cycle collects routes and pushes them into the cloud route tables.
func (a *App) cycle(ctx context.Context) (changed bool, prefixes []netip.Prefix, primary netinfo.Primary, err error) {
	primary, err = netinfo.Detect(a.cfg.General.Interface, a.cfg.General.IPAddress)
	if err != nil {
		return false, nil, primary, fmt.Errorf("detect primary interface: %w", err)
	}
	a.log.Debug("primary interface detected", "interface", primary.Interface, "ip", primary.IP.String(), "mac", primary.MAC)

	prefixes, srcErr := source.Collect(ctx, a.log, a.sources)
	if len(prefixes) == 0 {
		if srcErr != nil {
			return false, nil, primary, fmt.Errorf("no routes collected: %w", srcErr)
		}
		return false, nil, primary, errors.New("no routes found in any configured source")
	}
	a.log.Info("routes collected", "count", len(prefixes), "routes", strings.Join(routes.Strings(prefixes), ","))

	changes, syncErr := a.manager.Sync(ctx, a.cfg.Destination.RouteTableIDs, prefixes, primary.IP, a.cfg.General.DryRun)
	applied := cloud.Applied(changes)
	for _, c := range applied {
		if a.cfg.General.DryRun {
			a.log.Info("dry-run: route would be updated", "table", c.Table, "prefix", c.Prefix, "action", string(c.Action), "previous", c.PrevNextHop)
			continue
		}
		a.log.Info("route updated", "table", c.Table, "prefix", c.Prefix, "action", string(c.Action), "previous", c.PrevNextHop)
	}

	if err := errors.Join(srcErr, syncErr); err != nil {
		return len(applied) > 0 && !a.cfg.General.DryRun, prefixes, primary, err
	}
	if a.cfg.General.DryRun {
		// Nothing was actually applied, so success actions must not fire.
		return false, prefixes, primary, nil
	}
	return len(applied) > 0, prefixes, primary, nil
}

// Command cloud-route-manager keeps cloud route tables pointing at this
// instance for the prefixes published by the configured sources.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/serge-r/cloud-route-manager/internal/app"
	"github.com/serge-r/cloud-route-manager/internal/config"
	"github.com/serge-r/cloud-route-manager/internal/logging"
)

// Build information, injected at link time.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// DefaultConfigPath is where the deb/rpm packages install the configuration.
const DefaultConfigPath = "/opt/cloud-route-manager/config.yml"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "cloud-route-manager: "+err.Error())
		os.Exit(1)
	}
}

// options holds the command line flags. Every flag that overrides a setting
// is named exactly like its key in the general section of the configuration
// file, so `-log-severity debug` and `log-severity: debug` are the same knob.
type options struct {
	fs *flag.FlagSet

	configPath  string
	once        bool
	checkConfig bool
	version     bool

	interval               time.Duration
	logSeverity            string
	logFile                string
	logFormat              string
	dryRun                 bool
	cloud                  string
	iface                  string
	ipAddress              string
	timeout                time.Duration
	actionTimeout          time.Duration
	removeStaleLocalRoutes bool
}

func newOptions(out io.Writer) *options {
	o := &options{fs: flag.NewFlagSet("cloud-route-manager", flag.ContinueOnError)}
	o.fs.SetOutput(out)

	o.fs.StringVar(&o.configPath, "config", DefaultConfigPath, "path to the configuration file")
	o.fs.BoolVar(&o.once, "once", false, "run a single scan-and-update cycle and exit (same as interval: 0)")
	o.fs.BoolVar(&o.checkConfig, "check-config", false, "validate the configuration file and exit")
	o.fs.BoolVar(&o.version, "version", false, "print the version and exit")

	o.fs.DurationVar(&o.interval, "interval", 0, "override general.interval, e.g. 10m")
	o.fs.StringVar(&o.logSeverity, "log-severity", "", "override general.log-severity: debug, info, warn or error")
	o.fs.StringVar(&o.logFile, "log-file", "", "override general.log-file: stdout, stderr or a path")
	o.fs.StringVar(&o.logFormat, "log-format", "", "override general.log-format: text or json")
	o.fs.BoolVar(&o.dryRun, "dry-run", false, "override general.dry-run: log the planned changes without applying them")
	o.fs.StringVar(&o.cloud, "cloud", "", "override general.cloud: auto, aws or yandex")
	o.fs.StringVar(&o.iface, "interface", "", "override general.interface")
	o.fs.StringVar(&o.ipAddress, "ip-address", "", "override general.ip-address")
	o.fs.DurationVar(&o.timeout, "timeout", 0, "override general.timeout")
	o.fs.DurationVar(&o.actionTimeout, "action-timeout", 0, "override general.action-timeout")
	o.fs.BoolVar(&o.removeStaleLocalRoutes, "remove-stale-local-routes", false,
		"override general.remove-stale-local-routes: delete local routes of this service that left the config")

	o.fs.Usage = func() {
		_, _ = fmt.Fprintf(out, "cloud-route-manager %s\n\n"+
			"Usage:\n  cloud-route-manager -config %s\n\n"+
			"Flags named after a general.* key override that key of the configuration file.\n\n",
			version, DefaultConfigPath)
		o.fs.PrintDefaults()
	}
	return o
}

// applyTo overrides the configuration with the flags that were given
// explicitly; a flag that was not passed leaves the file value alone.
func (o *options) applyTo(cfg *config.Config) {
	o.fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "interval":
			cfg.General.Interval = config.Duration(o.interval)
		case "log-severity":
			cfg.General.LogSeverity = o.logSeverity
		case "log-file":
			cfg.General.LogFile = o.logFile
		case "log-format":
			cfg.General.LogFormat = o.logFormat
		case "dry-run":
			cfg.General.DryRun = o.dryRun
		case "cloud":
			cfg.General.Cloud = o.cloud
		case "interface":
			cfg.General.Interface = o.iface
		case "ip-address":
			cfg.General.IPAddress = o.ipAddress
		case "timeout":
			cfg.General.Timeout = config.Duration(o.timeout)
		case "action-timeout":
			cfg.General.ActionTimeout = config.Duration(o.actionTimeout)
		case "remove-stale-local-routes":
			cfg.General.RemoveStaleLocalRoutes = o.removeStaleLocalRoutes
		}
	})
	if o.once {
		cfg.General.Interval = 0
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	opts := newOptions(stderr)
	if err := opts.fs.Parse(args); err != nil {
		return err
	}

	if opts.version {
		_, _ = fmt.Fprintf(stdout, "cloud-route-manager %s (commit %s, built %s)\n", version, commit, date)
		return nil
	}

	cfg, err := config.Load(opts.configPath)
	if err != nil {
		return err
	}
	opts.applyTo(&cfg)
	// Overrides can be invalid too, so validate what will actually be used.
	if err := cfg.Validate(); err != nil {
		return err
	}
	if opts.checkConfig {
		_, _ = fmt.Fprintf(stdout, "%s: ok\n", opts.configPath)
		return nil
	}

	log, closer, err := logging.New(cfg.General)
	if err != nil {
		return err
	}
	if closer != nil {
		defer closer.Close()
	}
	log.Info("starting cloud-route-manager",
		"version", version,
		"commit", commit,
		"config", opts.configPath,
		"dry-run", cfg.General.DryRun,
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	a.NotifyOnSignal(ctx, syscall.SIGHUP)

	if err := a.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

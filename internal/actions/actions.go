// Package actions runs the shell commands configured for successful and
// failed update cycles.
package actions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Vars holds the values substituted into the configured commands.
type Vars struct {
	Routes    []string
	IPAddress string
	Interface string
	Cloud     string
}

// substitutions maps the placeholder name to its value.
func (v Vars) substitutions() map[string]string {
	return map[string]string{
		"routes":       strings.Join(v.Routes, ","),
		"ip-address":   v.IPAddress,
		"ip_address":   v.IPAddress,
		"interface":    v.Interface,
		"cloud":        v.Cloud,
		"routes-count": fmt.Sprint(len(v.Routes)),
	}
}

// env exposes the same values as environment variables so that commands can
// read long route lists without stuffing them into the command line.
func (v Vars) env() []string {
	return append(os.Environ(),
		"CRM_ROUTES="+strings.Join(v.Routes, ","),
		"CRM_IP_ADDRESS="+v.IPAddress,
		"CRM_INTERFACE="+v.Interface,
		"CRM_CLOUD="+v.Cloud,
	)
}

// Expand replaces ${name} placeholders in a command template.
func Expand(command string, vars Vars) string {
	subs := vars.substitutions()
	return os.Expand(command, func(key string) string {
		if v, ok := subs[key]; ok {
			return v
		}
		// Leave unknown placeholders to the shell.
		return "${" + key + "}"
	})
}

// Runner executes action commands through the system shell.
type Runner struct {
	Log     *slog.Logger
	Timeout time.Duration
	// Shell is the interpreter used to run a command, "/bin/sh" by default.
	Shell string
}

// Run executes every command in order. A failing command is logged and does
// not stop the remaining ones; all failures are returned joined.
func (r *Runner) Run(ctx context.Context, kind string, commands []string, vars Vars) error {
	if len(commands) == 0 {
		r.Log.Debug("no actions configured", "kind", kind)
		return nil
	}

	shell := r.Shell
	if shell == "" {
		shell = "/bin/sh"
	}

	var errs []error
	for _, raw := range commands {
		command := Expand(raw, vars)
		r.Log.Info("running action", "kind", kind, "command", command)

		cmdCtx := ctx
		if r.Timeout > 0 {
			var cancel context.CancelFunc
			cmdCtx, cancel = context.WithTimeout(ctx, r.Timeout)
			defer cancel()
		}

		var stdout, stderr bytes.Buffer
		cmd := exec.CommandContext(cmdCtx, shell, "-c", command)
		cmd.Env = vars.env()
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		start := time.Now()
		err := cmd.Run()
		attrs := []any{
			"kind", kind,
			"command", command,
			"duration", time.Since(start).Round(time.Millisecond).String(),
		}
		if out := strings.TrimSpace(stdout.String()); out != "" {
			attrs = append(attrs, "stdout", out)
		}
		if out := strings.TrimSpace(stderr.String()); out != "" {
			attrs = append(attrs, "stderr", out)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("action %q: %w", command, err))
			r.Log.Error("action failed", append(attrs, "error", err)...)
			continue
		}
		r.Log.Info("action finished", attrs...)
	}
	return errors.Join(errs...)
}

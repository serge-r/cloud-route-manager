package localroutes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os/exec"
	"strings"

	"github.com/serge-r/cloud-route-manager/internal/cloud"
	"github.com/serge-r/cloud-route-manager/internal/routes"
)

// Proto is the routing protocol id stamped on every route this service
// installs. It is how "our" routes are told apart from everyone else's:
// `ip route show proto 201` lists exactly what we own, which survives a
// restart of the service and a reboot of the host without a state file.
const Proto = 201

// Table is the value used in the Table field of the reported changes.
const Table = "local"

// Runner executes ip(8). Tests replace it; nothing else does.
type Runner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

type execRunner struct{}

// Run executes ip with the given arguments and returns its output.
func (execRunner) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "ip", args...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text != "" {
			return text, fmt.Errorf("ip %s: %w: %s", strings.Join(args, " "), err, text)
		}
		return text, fmt.Errorf("ip %s: %w", strings.Join(args, " "), err)
	}
	return text, nil
}

// Manager maintains the local static routes.
type Manager struct {
	run   Runner
	log   *slog.Logger
	proto int
}

// NewManager returns a manager driving the ip(8) command.
func NewManager(log *slog.Logger) *Manager {
	return &Manager{run: execRunner{}, log: log, proto: Proto}
}

// entry is a route currently installed by this service.
type entry struct {
	gateway   netip.Addr
	blackhole bool
}

// nextHop renders the entry for logs.
func (e entry) nextHop() string {
	if e.blackhole {
		return TargetBlackhole
	}
	if e.gateway.IsValid() {
		return e.gateway.String()
	}
	return "link"
}

// Sync installs every spec in the host routing table. Entries that already
// look the way they should are left alone; the rest are replaced. When
// removeStale is set, routes carrying our protocol id that are no longer
// configured are deleted. With dryRun nothing is executed.
func (m *Manager) Sync(ctx context.Context, specs []Spec, removeStale, dryRun bool) ([]cloud.Change, error) {
	current, err := m.list(ctx)
	if err != nil {
		return nil, err
	}

	var (
		changes []cloud.Change
		errs    []error
		desired = make(map[netip.Prefix]struct{}, len(specs))
		gateway netip.Addr
	)

	for _, spec := range specs {
		want := entry{gateway: spec.Gateway, blackhole: spec.Blackhole}
		if spec.ViaDefault {
			// Resolved once per pass, and only when something needs it.
			if !gateway.IsValid() {
				if gateway, err = m.defaultGateway(ctx); err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", spec, err))
					continue
				}
			}
			want.gateway = gateway
		}
		desired[spec.Prefix] = struct{}{}

		cur, found := current[spec.Prefix]
		if found && cur == want {
			changes = append(changes, cloud.Change{Table: Table, Prefix: spec.Prefix.String(), Action: cloud.ActionNoop})
			continue
		}

		change := cloud.Change{Table: Table, Prefix: spec.Prefix.String(), Action: cloud.ActionCreate}
		if found {
			change.Action = cloud.ActionReplace
			change.PrevNextHop = cur.nextHop()
		}
		if !dryRun {
			if _, err := m.run.Run(ctx, m.replaceArgs(spec.Prefix, want)...); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		changes = append(changes, change)
	}

	if removeStale {
		for prefix, cur := range current {
			if _, keep := desired[prefix]; keep {
				continue
			}
			change := cloud.Change{
				Table:       Table,
				Prefix:      prefix.String(),
				Action:      cloud.ActionDelete,
				PrevNextHop: cur.nextHop(),
			}
			if !dryRun {
				if _, err := m.run.Run(ctx, "route", "del", prefix.String(), "proto", m.protoArg()); err != nil {
					errs = append(errs, err)
					continue
				}
			}
			changes = append(changes, change)
		}
	}

	return changes, errors.Join(errs...)
}

// replaceArgs builds the `ip route replace` invocation for one route.
func (m *Manager) replaceArgs(prefix netip.Prefix, want entry) []string {
	if want.blackhole {
		return []string{"route", "replace", TargetBlackhole, prefix.String(), "proto", m.protoArg()}
	}
	return []string{"route", "replace", prefix.String(), "via", want.gateway.String(), "proto", m.protoArg()}
}

func (m *Manager) protoArg() string { return fmt.Sprint(m.proto) }

// list returns the routes currently tagged with our protocol id.
func (m *Manager) list(ctx context.Context) (map[netip.Prefix]entry, error) {
	out, err := m.run.Run(ctx, "-o", "route", "show", "proto", m.protoArg())
	if err != nil {
		return nil, err
	}

	current := make(map[netip.Prefix]entry)
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}

		var e entry
		if strings.EqualFold(fields[0], TargetBlackhole) {
			if len(fields) < 2 {
				continue
			}
			e.blackhole = true
			fields = fields[1:]
		}
		// ip(8) prints host routes without a mask, hence ParseOne.
		prefix, err := routes.ParseOne(fields[0])
		if err != nil {
			m.log.Debug("ignoring unparsable local route", "line", line)
			continue
		}
		for i, f := range fields {
			if f == "via" && i+1 < len(fields) {
				if gw, err := netip.ParseAddr(fields[i+1]); err == nil {
					e.gateway = gw.Unmap()
				}
				break
			}
		}
		current[prefix] = e
	}
	return current, nil
}

// defaultGateway reads the next hop of the current default route.
func (m *Manager) defaultGateway(ctx context.Context) (netip.Addr, error) {
	out, err := m.run.Run(ctx, "-o", "route", "show", "default")
	if err != nil {
		return netip.Addr{}, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "via" && i+1 < len(fields) {
				gw, err := netip.ParseAddr(fields[i+1])
				if err != nil {
					continue
				}
				return gw.Unmap(), nil
			}
		}
	}
	return netip.Addr{}, errors.New("no default route with a gateway to resolve \"via default\"")
}

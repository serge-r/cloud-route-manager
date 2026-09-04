package localroutes

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/serge-r/cloud-route-manager/internal/cloud"
)

// fakeIP answers `ip` invocations from a canned table and records the
// mutating calls.
type fakeIP struct {
	show     string
	def      string
	showErr  error
	applyErr error
	calls    []string
}

func (f *fakeIP) Run(_ context.Context, args ...string) (string, error) {
	line := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(line, "-o route show proto"):
		return f.show, f.showErr
	case line == "-o route show default":
		return f.def, nil
	default:
		f.calls = append(f.calls, line)
		return "", f.applyErr
	}
}

func newTestManager(ip *fakeIP) *Manager {
	return &Manager{run: ip, log: slog.New(slog.NewTextHandler(os.Stderr, nil)), proto: Proto}
}

func specs(t *testing.T, list ...string) []Spec {
	t.Helper()
	parsed, err := ParseSpecs(list)
	if err != nil {
		t.Fatalf("ParseSpecs(%v): %v", list, err)
	}
	return parsed
}

func TestParseSpec(t *testing.T) {
	cases := []struct {
		in   string
		want string
		err  bool
	}{
		{in: "192.168.0.0/24 via default", want: "192.168.0.0/24 via default"},
		{in: "192.168.0.0/24 via 1.1.1.1", want: "192.168.0.0/24 via 1.1.1.1"},
		{in: "192.168.0.0/24 via blackhole", want: "192.168.0.0/24 via blackhole"},
		{in: "  10.0.0.1   VIA   BLACKHOLE  ", want: "10.0.0.1/32 via blackhole"},
		{in: "192.168.0.5/24 via 1.1.1.1", want: "192.168.0.0/24 via 1.1.1.1"},
		{in: "2001:db8::/32 via 2001:db8::1", want: "2001:db8::/32 via 2001:db8::1"},
		{in: "192.168.0.0/24", err: true},
		{in: "192.168.0.0/24 via", err: true},
		{in: "192.168.0.0/24 through 1.1.1.1", err: true},
		{in: "nonsense via default", err: true},
		{in: "192.168.0.0/24 via nonsense", err: true},
		{in: "192.168.0.0/24 via 2001:db8::1", err: true},
	}
	for _, c := range cases {
		got, err := ParseSpec(c.in)
		if c.err {
			if err == nil {
				t.Errorf("ParseSpec(%q) = %v, want an error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSpec(%q): %v", c.in, err)
			continue
		}
		if got.String() != c.want {
			t.Errorf("ParseSpec(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSyncInstallsMissingRoutes(t *testing.T) {
	ip := &fakeIP{def: "default via 10.0.0.1 dev eth0 proto dhcp metric 100"}
	m := newTestManager(ip)

	changes, err := m.Sync(context.Background(), specs(t,
		"192.168.0.0/24 via default",
		"10.10.0.0/16 via 1.1.1.1",
		"172.16.5.0/24 via blackhole",
	), false, false)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	want := []string{
		"route replace 192.168.0.0/24 via 10.0.0.1 proto 201",
		"route replace 10.10.0.0/16 via 1.1.1.1 proto 201",
		"route replace blackhole 172.16.5.0/24 proto 201",
	}
	if len(ip.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", ip.calls, want)
	}
	for i := range want {
		if ip.calls[i] != want[i] {
			t.Errorf("call %d = %q, want %q", i, ip.calls[i], want[i])
		}
	}
	if len(cloud.Applied(changes)) != 3 {
		t.Fatalf("Applied = %v, want three changes", changes)
	}
}

func TestSyncLeavesCorrectRoutesAlone(t *testing.T) {
	ip := &fakeIP{show: "" +
		"10.10.0.0/16 via 1.1.1.1 dev eth0 proto 201\n" +
		"blackhole 172.16.5.0/24 proto 201\n"}
	m := newTestManager(ip)

	changes, err := m.Sync(context.Background(), specs(t,
		"10.10.0.0/16 via 1.1.1.1",
		"172.16.5.0/24 via blackhole",
	), false, false)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(ip.calls) != 0 {
		t.Fatalf("routes were touched without a reason: %v", ip.calls)
	}
	if len(cloud.Applied(changes)) != 0 {
		t.Fatalf("Applied = %v, want no changes", cloud.Applied(changes))
	}
}

func TestSyncReplacesAChangedGateway(t *testing.T) {
	ip := &fakeIP{show: "10.10.0.0/16 via 9.9.9.9 dev eth0 proto 201\n"}
	m := newTestManager(ip)

	changes, err := m.Sync(context.Background(), specs(t, "10.10.0.0/16 via 1.1.1.1"), false, false)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(ip.calls) != 1 || ip.calls[0] != "route replace 10.10.0.0/16 via 1.1.1.1 proto 201" {
		t.Fatalf("calls = %v, want a single replace", ip.calls)
	}
	if changes[0].Action != cloud.ActionReplace || changes[0].PrevNextHop != "9.9.9.9" {
		t.Errorf("change = %+v, want a replace reporting the old gateway", changes[0])
	}
}

func TestSyncFollowsAMovingDefaultGateway(t *testing.T) {
	ip := &fakeIP{
		show: "192.168.0.0/24 via 10.0.0.1 dev eth0 proto 201\n",
		def:  "default via 10.0.0.254 dev eth0 proto dhcp",
	}
	m := newTestManager(ip)

	if _, err := m.Sync(context.Background(), specs(t, "192.168.0.0/24 via default"), false, false); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(ip.calls) != 1 || !strings.Contains(ip.calls[0], "via 10.0.0.254") {
		t.Fatalf("calls = %v, want the route to follow the new default gateway", ip.calls)
	}
}

func TestSyncReportsAMissingDefaultRoute(t *testing.T) {
	ip := &fakeIP{def: ""}
	m := newTestManager(ip)

	_, err := m.Sync(context.Background(), specs(t,
		"192.168.0.0/24 via default",
		"10.10.0.0/16 via 1.1.1.1",
	), false, false)
	if err == nil || !strings.Contains(err.Error(), "default route") {
		t.Fatalf("Sync error = %v, want a complaint about the default route", err)
	}
	// The entry that does not depend on the default route still went in.
	if len(ip.calls) != 1 || !strings.Contains(ip.calls[0], "10.10.0.0/16") {
		t.Fatalf("calls = %v, want the independent route to be installed anyway", ip.calls)
	}
}

func TestSyncRemovesStaleRoutesOnlyWhenAsked(t *testing.T) {
	const show = "" +
		"10.10.0.0/16 via 1.1.1.1 dev eth0 proto 201\n" +
		"192.168.99.0/24 via 1.1.1.1 dev eth0 proto 201\n"

	ip := &fakeIP{show: show}
	if _, err := newTestManager(ip).Sync(context.Background(), specs(t, "10.10.0.0/16 via 1.1.1.1"), false, false); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(ip.calls) != 0 {
		t.Fatalf("calls = %v, want nothing deleted with cleanup off", ip.calls)
	}

	ip = &fakeIP{show: show}
	changes, err := newTestManager(ip).Sync(context.Background(), specs(t, "10.10.0.0/16 via 1.1.1.1"), true, false)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(ip.calls) != 1 || ip.calls[0] != "route del 192.168.99.0/24 proto 201" {
		t.Fatalf("calls = %v, want the stale route deleted", ip.calls)
	}
	if changes[len(changes)-1].Action != cloud.ActionDelete {
		t.Errorf("changes = %+v, want a delete reported", changes)
	}
}

func TestSyncDryRunTouchesNothing(t *testing.T) {
	ip := &fakeIP{show: "192.168.99.0/24 via 1.1.1.1 dev eth0 proto 201\n"}
	m := newTestManager(ip)

	changes, err := m.Sync(context.Background(), specs(t, "10.10.0.0/16 via 1.1.1.1"), true, true)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(ip.calls) != 0 {
		t.Fatalf("dry-run executed %v", ip.calls)
	}
	if len(cloud.Applied(changes)) != 2 {
		t.Fatalf("Applied = %v, want the planned create and delete", changes)
	}
}

func TestSyncReportsCommandFailures(t *testing.T) {
	ip := &fakeIP{applyErr: errors.New("RTNETLINK answers: Network is unreachable")}
	_, err := newTestManager(ip).Sync(context.Background(), specs(t, "10.10.0.0/16 via 1.1.1.1"), false, false)
	if err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("Sync error = %v, want the ip failure", err)
	}

	ip = &fakeIP{showErr: errors.New("ip: command not found")}
	if _, err := newTestManager(ip).Sync(context.Background(), specs(t, "10.10.0.0/16 via 1.1.1.1"), false, false); err == nil {
		t.Fatal("Sync succeeded although the routing table could not be read")
	}
}

package aws

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"strings"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/serge-r/cloud-route-manager/internal/cloud"
)

type fakeEC2 struct {
	routes      []ec2types.Route
	created     []ec2.CreateRouteInput
	replaced    []ec2.ReplaceRouteInput
	describeErr error
}

func (f *fakeEC2) DescribeRouteTables(context.Context, *ec2.DescribeRouteTablesInput, ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error) {
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	return &ec2.DescribeRouteTablesOutput{
		RouteTables: []ec2types.RouteTable{{Routes: f.routes}},
	}, nil
}

func (f *fakeEC2) DescribeNetworkInterfaces(context.Context, *ec2.DescribeNetworkInterfacesInput, ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error) {
	return &ec2.DescribeNetworkInterfacesOutput{}, nil
}

func (f *fakeEC2) CreateRoute(_ context.Context, in *ec2.CreateRouteInput, _ ...func(*ec2.Options)) (*ec2.CreateRouteOutput, error) {
	f.created = append(f.created, *in)
	return &ec2.CreateRouteOutput{}, nil
}

func (f *fakeEC2) ReplaceRoute(_ context.Context, in *ec2.ReplaceRouteInput, _ ...func(*ec2.Options)) (*ec2.ReplaceRouteOutput, error) {
	f.replaced = append(f.replaced, *in)
	return &ec2.ReplaceRouteOutput{}, nil
}

// newTestManager returns a manager with the ENI lookup already resolved so
// that tests never touch instance metadata.
func newTestManager(api EC2API) *Manager {
	return &Manager{ec2: api, log: slog.New(slog.NewTextHandler(os.Stderr, nil)), eni: "eni-primary"}
}

func prefixes(t *testing.T, list ...string) []netip.Prefix {
	t.Helper()
	out := make([]netip.Prefix, 0, len(list))
	for _, s := range list {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

func TestSyncCreatesReplacesAndSkips(t *testing.T) {
	api := &fakeEC2{routes: []ec2types.Route{
		{DestinationCidrBlock: awssdk.String("10.0.0.0/8"), NatGatewayId: awssdk.String("nat-1"), State: ec2types.RouteStateActive},
		{DestinationCidrBlock: awssdk.String("8.8.8.8/32"), NetworkInterfaceId: awssdk.String("eni-primary"), State: ec2types.RouteStateActive},
		{DestinationCidrBlock: awssdk.String("172.16.0.0/12"), GatewayId: awssdk.String("igw-1"), State: ec2types.RouteStateActive},
	}}
	m := newTestManager(api)

	changes, err := m.Sync(context.Background(), []string{"rtb-1"},
		prefixes(t, "10.0.0.0/8", "8.8.8.8/32", "1.1.1.1/32"),
		netip.MustParseAddr("192.168.1.10"), false)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if len(api.created) != 1 || awssdk.ToString(api.created[0].DestinationCidrBlock) != "1.1.1.1/32" {
		t.Fatalf("created = %v, want a single route for 1.1.1.1/32", api.created)
	}
	if len(api.replaced) != 1 || awssdk.ToString(api.replaced[0].DestinationCidrBlock) != "10.0.0.0/8" {
		t.Fatalf("replaced = %v, want a single route for 10.0.0.0/8", api.replaced)
	}
	if eni := awssdk.ToString(api.created[0].NetworkInterfaceId); eni != "eni-primary" {
		t.Errorf("created route target = %q, want eni-primary", eni)
	}

	byPrefix := map[string]cloud.Change{}
	for _, c := range changes {
		byPrefix[c.Prefix] = c
	}
	if got := byPrefix["8.8.8.8/32"].Action; got != cloud.ActionNoop {
		t.Errorf("8.8.8.8/32 action = %q, want noop", got)
	}
	if got := byPrefix["10.0.0.0/8"].PrevNextHop; got != "nat-1" {
		t.Errorf("10.0.0.0/8 previous next hop = %q, want nat-1", got)
	}
	if _, ok := byPrefix["172.16.0.0/12"]; ok {
		t.Error("routes outside the source list must not be touched")
	}
}

func TestSyncIPv6UsesTheIPv6Destination(t *testing.T) {
	api := &fakeEC2{}
	m := newTestManager(api)

	if _, err := m.Sync(context.Background(), []string{"rtb-1"},
		prefixes(t, "2001:db8::/32"), netip.MustParseAddr("192.168.1.10"), false); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(api.created) != 1 {
		t.Fatalf("created = %v, want one route", api.created)
	}
	if api.created[0].DestinationCidrBlock != nil {
		t.Error("IPv4 destination must stay unset for an IPv6 prefix")
	}
	if got := awssdk.ToString(api.created[0].DestinationIpv6CidrBlock); got != "2001:db8::/32" {
		t.Errorf("DestinationIpv6CidrBlock = %q, want 2001:db8::/32", got)
	}
}

func TestSyncDryRunDoesNotCallTheAPI(t *testing.T) {
	api := &fakeEC2{}
	m := newTestManager(api)

	changes, err := m.Sync(context.Background(), []string{"rtb-1"},
		prefixes(t, "1.1.1.1/32"), netip.MustParseAddr("192.168.1.10"), true)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(api.created) != 0 || len(api.replaced) != 0 {
		t.Fatal("dry-run must not modify anything")
	}
	if len(cloud.Applied(changes)) != 1 {
		t.Fatalf("Applied = %v, want the planned change", changes)
	}
}

func TestSyncReportsTableErrors(t *testing.T) {
	m := newTestManager(&fakeEC2{describeErr: errors.New("access denied")})

	_, err := m.Sync(context.Background(), []string{"rtb-broken"},
		prefixes(t, "1.1.1.1/32"), netip.MustParseAddr("192.168.1.10"), false)
	if err == nil || !strings.Contains(err.Error(), "rtb-broken") {
		t.Fatalf("Sync error = %v, want it to name the failing table", err)
	}
}

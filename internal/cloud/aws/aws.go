// Package aws implements route table management for AWS VPCs.
package aws

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/serge-r/cloud-route-manager/internal/cloud"
)

// EC2API is the subset of the EC2 client the manager uses.
type EC2API interface {
	DescribeRouteTables(context.Context, *ec2.DescribeRouteTablesInput, ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error)
	DescribeNetworkInterfaces(context.Context, *ec2.DescribeNetworkInterfacesInput, ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error)
	CreateRoute(context.Context, *ec2.CreateRouteInput, ...func(*ec2.Options)) (*ec2.CreateRouteOutput, error)
	ReplaceRoute(context.Context, *ec2.ReplaceRouteInput, ...func(*ec2.Options)) (*ec2.ReplaceRouteOutput, error)
}

// Manager updates AWS VPC route tables.
type Manager struct {
	ec2  EC2API
	imds *imds.Client
	log  *slog.Logger

	mu  sync.Mutex
	eni string
}

// NewManager builds a route table manager for AWS. The region is taken from
// the usual AWS configuration chain, falling back to instance metadata.
func NewManager(ctx context.Context, log *slog.Logger) (*Manager, error) {
	meta := imds.New(imds.Options{})

	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	if cfg.Region == "" {
		region, err := meta.GetRegion(ctx, &imds.GetRegionInput{})
		if err != nil {
			return nil, fmt.Errorf("detect aws region: %w", err)
		}
		cfg.Region = region.Region
	}

	return &Manager{ec2: ec2.NewFromConfig(cfg), imds: meta, log: log}, nil
}

// Provider implements cloud.Manager.
func (m *Manager) Provider() cloud.Provider { return cloud.ProviderAWS }

// Sync implements cloud.Manager.
func (m *Manager) Sync(ctx context.Context, tables []string, routes []netip.Prefix, nextHop netip.Addr, dryRun bool) ([]cloud.Change, error) {
	eni, err := m.networkInterfaceID(ctx, nextHop)
	if err != nil {
		return nil, err
	}
	m.log.Debug("resolved network interface", "eni", eni, "ip", nextHop.String())

	var (
		changes []cloud.Change
		errs    []error
	)
	for _, table := range tables {
		tableChanges, err := m.syncTable(ctx, table, routes, eni, dryRun)
		changes = append(changes, tableChanges...)
		if err != nil {
			errs = append(errs, fmt.Errorf("route table %s: %w", table, err))
		}
	}
	return changes, errors.Join(errs...)
}

func (m *Manager) syncTable(ctx context.Context, tableID string, routes []netip.Prefix, eni string, dryRun bool) ([]cloud.Change, error) {
	out, err := m.ec2.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{RouteTableIds: []string{tableID}})
	if err != nil {
		return nil, fmt.Errorf("describe: %w", err)
	}
	if len(out.RouteTables) == 0 {
		return nil, fmt.Errorf("not found")
	}

	existing := make(map[netip.Prefix]ec2types.Route, len(out.RouteTables[0].Routes))
	for _, r := range out.RouteTables[0].Routes {
		cidr := awssdk.ToString(r.DestinationCidrBlock)
		if cidr == "" {
			cidr = awssdk.ToString(r.DestinationIpv6CidrBlock)
		}
		if cidr == "" {
			// Prefix-list routes have no CIDR of their own.
			continue
		}
		p, err := netip.ParsePrefix(cidr)
		if err != nil {
			continue
		}
		existing[p.Masked()] = r
	}

	var (
		changes []cloud.Change
		errs    []error
	)
	for _, p := range routes {
		cur, found := existing[p]
		switch {
		case found && awssdk.ToString(cur.NetworkInterfaceId) == eni && cur.State == ec2types.RouteStateActive:
			changes = append(changes, cloud.Change{Table: tableID, Prefix: p.String(), Action: cloud.ActionNoop})
		case found:
			change := cloud.Change{
				Table:       tableID,
				Prefix:      p.String(),
				Action:      cloud.ActionReplace,
				PrevNextHop: routeTarget(cur),
			}
			if !dryRun {
				if _, err := m.ec2.ReplaceRoute(ctx, &ec2.ReplaceRouteInput{
					RouteTableId:       awssdk.String(tableID),
					NetworkInterfaceId: awssdk.String(eni),
					DestinationCidrBlock: destination(p, func(p netip.Prefix) bool {
						return p.Addr().Is4()
					}),
					DestinationIpv6CidrBlock: destination(p, func(p netip.Prefix) bool {
						return !p.Addr().Is4()
					}),
				}); err != nil {
					errs = append(errs, fmt.Errorf("replace %s: %w", p, err))
					continue
				}
			}
			changes = append(changes, change)
		default:
			change := cloud.Change{Table: tableID, Prefix: p.String(), Action: cloud.ActionCreate}
			if !dryRun {
				if _, err := m.ec2.CreateRoute(ctx, &ec2.CreateRouteInput{
					RouteTableId:       awssdk.String(tableID),
					NetworkInterfaceId: awssdk.String(eni),
					DestinationCidrBlock: destination(p, func(p netip.Prefix) bool {
						return p.Addr().Is4()
					}),
					DestinationIpv6CidrBlock: destination(p, func(p netip.Prefix) bool {
						return !p.Addr().Is4()
					}),
				}); err != nil {
					errs = append(errs, fmt.Errorf("create %s: %w", p, err))
					continue
				}
			}
			changes = append(changes, change)
		}
	}
	return changes, errors.Join(errs...)
}

// destination returns the CIDR string only when the family matches, so that
// exactly one of the two destination fields of the EC2 API is set.
func destination(p netip.Prefix, match func(netip.Prefix) bool) *string {
	if !match(p) {
		return nil
	}
	return awssdk.String(p.String())
}

// routeTarget renders the current next hop of a route for logging.
func routeTarget(r ec2types.Route) string {
	for _, v := range []*string{
		r.NetworkInterfaceId, r.GatewayId, r.NatGatewayId, r.TransitGatewayId,
		r.InstanceId, r.VpcPeeringConnectionId, r.EgressOnlyInternetGatewayId,
		r.LocalGatewayId, r.CarrierGatewayId, r.CoreNetworkArn,
	} {
		if s := awssdk.ToString(v); s != "" {
			return s
		}
	}
	if r.State == ec2types.RouteStateBlackhole {
		return string(ec2types.RouteStateBlackhole)
	}
	return "unknown"
}

// networkInterfaceID resolves the ENI that owns the given private address.
// Instance metadata answers this without any IAM permission; the EC2 API is
// the fallback for secondary addresses and unusual setups.
func (m *Manager) networkInterfaceID(ctx context.Context, ip netip.Addr) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.eni != "" {
		return m.eni, nil
	}

	eni, err := m.eniFromMetadata(ctx, ip)
	if err == nil {
		m.eni = eni
		return eni, nil
	}
	m.log.Debug("interface lookup via metadata failed, falling back to the EC2 API", "error", err)

	out, err := m.ec2.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{
		Filters: []ec2types.Filter{{
			Name:   awssdk.String("addresses.private-ip-address"),
			Values: []string{ip.String()},
		}},
	})
	if err != nil {
		return "", fmt.Errorf("describe network interfaces for %s: %w", ip, err)
	}
	if len(out.NetworkInterfaces) == 0 {
		return "", fmt.Errorf("no network interface carries address %s", ip)
	}
	m.eni = awssdk.ToString(out.NetworkInterfaces[0].NetworkInterfaceId)
	return m.eni, nil
}

func (m *Manager) eniFromMetadata(ctx context.Context, ip netip.Addr) (string, error) {
	macs, err := m.metadata(ctx, "network/interfaces/macs/")
	if err != nil {
		return "", err
	}
	for mac := range strings.FieldsSeq(macs) {
		mac = strings.TrimSuffix(mac, "/")
		if mac == "" {
			continue
		}
		ips, err := m.metadata(ctx, "network/interfaces/macs/"+mac+"/local-ipv4s")
		if err != nil {
			continue
		}
		if slices.Contains(strings.Fields(ips), ip.String()) {
			return m.metadata(ctx, "network/interfaces/macs/"+mac+"/interface-id")
		}
	}
	return "", fmt.Errorf("no interface in instance metadata carries address %s", ip)
}

func (m *Manager) metadata(ctx context.Context, path string) (string, error) {
	out, err := m.imds.GetMetadata(ctx, &imds.GetMetadataInput{Path: path})
	if err != nil {
		return "", err
	}
	defer out.Content.Close()
	raw, err := io.ReadAll(io.LimitReader(out.Content, 1<<20))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

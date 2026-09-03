package yandex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/serge-r/cloud-route-manager/internal/cloud"
)

// Default API endpoints.
const (
	DefaultVPCEndpoint       = "https://vpc.api.cloud.yandex.net"
	DefaultOperationEndpoint = "https://operation.api.cloud.yandex.net"
)

// Manager updates Yandex Cloud VPC route tables.
type Manager struct {
	meta *MetadataClient
	log  *slog.Logger
	http *http.Client

	vpcEndpoint string
	opEndpoint  string
	// opTimeout bounds waiting for an update operation to finish.
	opTimeout time.Duration
}

// NewManager builds a route table manager for Yandex Cloud.
func NewManager(ctx context.Context, log *slog.Logger) (*Manager, error) {
	httpc := &http.Client{Timeout: 30 * time.Second}
	m := &Manager{
		meta:        NewMetadataClient(httpc),
		log:         log,
		http:        httpc,
		vpcEndpoint: endpointFromEnv("YC_VPC_ENDPOINT", DefaultVPCEndpoint),
		opEndpoint:  endpointFromEnv("YC_OPERATION_ENDPOINT", DefaultOperationEndpoint),
		opTimeout:   60 * time.Second,
	}
	// Fail fast if we cannot authenticate at all.
	if _, err := m.meta.Token(ctx); err != nil {
		return nil, err
	}
	return m, nil
}

func endpointFromEnv(env, def string) string {
	if v := strings.TrimSpace(os.Getenv(env)); v != "" {
		return strings.TrimSuffix(v, "/")
	}
	return def
}

// Provider implements cloud.Manager.
func (m *Manager) Provider() cloud.Provider { return cloud.ProviderYandex }

// Sync implements cloud.Manager.
func (m *Manager) Sync(ctx context.Context, tables []string, routes []netip.Prefix, nextHop netip.Addr, dryRun bool) ([]cloud.Change, error) {
	var (
		changes []cloud.Change
		errs    []error
	)
	for _, table := range tables {
		tableChanges, err := m.syncTable(ctx, table, routes, nextHop, dryRun)
		changes = append(changes, tableChanges...)
		if err != nil {
			errs = append(errs, fmt.Errorf("route table %s: %w", table, err))
		}
	}
	return changes, errors.Join(errs...)
}

// staticRoutes are kept as raw maps so that fields this service does not know
// about (labels, gateway targets, future additions) survive the update.
type routeTable struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	StaticRoutes []map[string]any `json:"staticRoutes"`
}

func (m *Manager) syncTable(ctx context.Context, tableID string, routes []netip.Prefix, nextHop netip.Addr, dryRun bool) ([]cloud.Change, error) {
	table, err := m.getRouteTable(ctx, tableID)
	if err != nil {
		return nil, err
	}

	wanted := make(map[netip.Prefix]bool, len(routes))
	for _, p := range routes {
		wanted[p] = true
	}

	var (
		changes []cloud.Change
		seen    = make(map[netip.Prefix]bool, len(routes))
		desired = make([]map[string]any, 0, len(table.StaticRoutes)+len(routes))
		mutated bool
	)

	for _, existing := range table.StaticRoutes {
		prefix, ok := parsePrefixField(existing["destinationPrefix"])
		if !ok || !wanted[prefix] || seen[prefix] {
			// Not ours (or a duplicate entry): keep it verbatim.
			desired = append(desired, existing)
			continue
		}
		seen[prefix] = true

		prev := previousNextHop(existing)
		if prev == nextHop.String() {
			changes = append(changes, cloud.Change{Table: tableID, Prefix: prefix.String(), Action: cloud.ActionNoop})
			desired = append(desired, existing)
			continue
		}

		updated := make(map[string]any, len(existing))
		maps.Copy(updated, existing)
		// A static route carries exactly one next hop, so competing target
		// fields have to go when we take the prefix over.
		delete(updated, "gatewayId")
		updated["nextHopAddress"] = nextHop.String()
		desired = append(desired, updated)

		changes = append(changes, cloud.Change{
			Table:       tableID,
			Prefix:      prefix.String(),
			Action:      cloud.ActionReplace,
			PrevNextHop: prev,
		})
		mutated = true
	}

	for _, p := range routes {
		if seen[p] {
			continue
		}
		desired = append(desired, map[string]any{
			"destinationPrefix": p.String(),
			"nextHopAddress":    nextHop.String(),
		})
		changes = append(changes, cloud.Change{Table: tableID, Prefix: p.String(), Action: cloud.ActionCreate})
		mutated = true
	}

	if !mutated {
		m.log.Debug("route table already up to date", "table", tableID, "provider", "yandex")
		return changes, nil
	}
	if dryRun {
		return changes, nil
	}

	if err := m.updateStaticRoutes(ctx, tableID, desired); err != nil {
		return changes, err
	}
	return changes, nil
}

func previousNextHop(route map[string]any) string {
	for _, key := range []string{"nextHopAddress", "gatewayId"} {
		if v, ok := route[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func parsePrefixField(v any) (netip.Prefix, bool) {
	s, ok := v.(string)
	if !ok || s == "" {
		return netip.Prefix{}, false
	}
	p, err := netip.ParsePrefix(strings.TrimSpace(s))
	if err != nil {
		return netip.Prefix{}, false
	}
	return p.Masked(), true
}

func (m *Manager) getRouteTable(ctx context.Context, id string) (*routeTable, error) {
	var table routeTable
	if err := m.do(ctx, http.MethodGet, m.vpcEndpoint+"/vpc/v1/routeTables/"+id, nil, &table); err != nil {
		return nil, err
	}
	return &table, nil
}

func (m *Manager) updateStaticRoutes(ctx context.Context, id string, staticRoutes []map[string]any) error {
	body := map[string]any{
		"updateMask":   "staticRoutes",
		"staticRoutes": staticRoutes,
	}
	var op operation
	if err := m.do(ctx, http.MethodPatch, m.vpcEndpoint+"/vpc/v1/routeTables/"+id, body, &op); err != nil {
		return err
	}
	return m.waitOperation(ctx, op)
}

type operation struct {
	ID    string `json:"id"`
	Done  bool   `json:"done"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (o operation) err() error {
	if o.Error != nil && (o.Error.Message != "" || o.Error.Code != 0) {
		return fmt.Errorf("operation %s failed: code %d: %s", o.ID, o.Error.Code, o.Error.Message)
	}
	return nil
}

// waitOperation polls the operation until it is done so that a cycle never
// reports success for an update the API later rejected.
func (m *Manager) waitOperation(ctx context.Context, op operation) error {
	if err := op.err(); err != nil {
		return err
	}
	if op.Done || op.ID == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, m.opTimeout)
	defer cancel()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("operation %s: %w", op.ID, ctx.Err())
		case <-ticker.C:
			var cur operation
			if err := m.do(ctx, http.MethodGet, m.opEndpoint+"/operations/"+op.ID, nil, &cur); err != nil {
				return err
			}
			if err := cur.err(); err != nil {
				return err
			}
			if cur.Done {
				return nil
			}
		}
	}
}

func (m *Manager) do(ctx context.Context, method, url string, in, out any) error {
	var body io.Reader
	if in != nil {
		buf, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	token, err := m.meta.Token(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := m.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, url, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("%s %s: read response: %w", method, url, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s: %s", method, url, resp.Status, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", method, url, err)
	}
	return nil
}

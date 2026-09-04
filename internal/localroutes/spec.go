// Package localroutes maintains static routes in the routing table of the
// host itself, next to the cloud route tables.
package localroutes

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/serge-r/cloud-route-manager/internal/routes"
)

// Keywords accepted in place of a gateway address.
const (
	TargetDefault   = "default"
	TargetBlackhole = "blackhole"
)

// Spec is one entry of local-static-routes: "<prefix> via <target>", where
// the target is a gateway address, "default" (whatever the default route
// points at right now) or "blackhole" (drop the traffic).
type Spec struct {
	Prefix netip.Prefix
	// Gateway is set when the spec names an explicit next hop.
	Gateway netip.Addr
	// ViaDefault resolves the next hop from the current default route on
	// every pass, so the entry follows a changing default gateway.
	ViaDefault bool
	// Blackhole installs a null route instead of a next hop.
	Blackhole bool
}

// String renders the spec in its configuration form.
func (s Spec) String() string {
	switch {
	case s.Blackhole:
		return s.Prefix.String() + " via " + TargetBlackhole
	case s.ViaDefault:
		return s.Prefix.String() + " via " + TargetDefault
	default:
		return s.Prefix.String() + " via " + s.Gateway.String()
	}
}

// ParseSpec parses a single local-static-routes entry.
func ParseSpec(raw string) (Spec, error) {
	fields := strings.Fields(raw)
	if len(fields) != 3 || !strings.EqualFold(fields[1], "via") {
		return Spec{}, fmt.Errorf("invalid route %q: expected \"<prefix> via <gateway|default|blackhole>\"", raw)
	}

	prefix, err := routes.ParseOne(fields[0])
	if err != nil {
		return Spec{}, fmt.Errorf("invalid route %q: %w", raw, err)
	}
	spec := Spec{Prefix: prefix}

	switch target := fields[2]; strings.ToLower(target) {
	case TargetDefault:
		spec.ViaDefault = true
	case TargetBlackhole:
		spec.Blackhole = true
	default:
		gw, err := netip.ParseAddr(target)
		if err != nil {
			return Spec{}, fmt.Errorf("invalid route %q: gateway: %w", raw, err)
		}
		gw = gw.Unmap()
		if gw.Is4() != prefix.Addr().Is4() {
			return Spec{}, fmt.Errorf("invalid route %q: gateway and prefix belong to different address families", raw)
		}
		spec.Gateway = gw
	}
	return spec, nil
}

// ParseSpecs parses every entry, reporting the first invalid one.
func ParseSpecs(raw []string) ([]Spec, error) {
	specs := make([]Spec, 0, len(raw))
	for _, entry := range raw {
		spec, err := ParseSpec(entry)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// Package cloud defines the provider-agnostic route table interface and the
// autodetection of the cloud the service runs in.
package cloud

import (
	"context"
	"fmt"
	"net/netip"
)

// Provider identifies a supported cloud.
type Provider string

// Supported providers.
const (
	ProviderAWS    Provider = "aws"
	ProviderYandex Provider = "yandex"
)

// Action describes what happened (or would happen) to a single route.
type Action string

// Route actions reported by a Manager.
const (
	ActionCreate  Action = "create"
	ActionReplace Action = "replace"
	ActionNoop    Action = "noop"
)

// Change is a single route table modification.
type Change struct {
	Table       string
	Prefix      string
	Action      Action
	PrevNextHop string
}

// String renders a change for logs.
func (c Change) String() string {
	if c.PrevNextHop != "" {
		return fmt.Sprintf("%s %s in %s (was %s)", c.Action, c.Prefix, c.Table, c.PrevNextHop)
	}
	return fmt.Sprintf("%s %s in %s", c.Action, c.Prefix, c.Table)
}

// Manager maintains route tables of one cloud provider.
type Manager interface {
	// Provider returns the cloud this manager talks to.
	Provider() Provider
	// Sync makes every prefix of routes point at nextHop in each of the
	// given tables. Prefixes that already exist are overwritten regardless
	// of their current next hop; routes that are not in the list are left
	// untouched. With dryRun the changes are only computed, not applied.
	//
	// Sync reports the changes it made (or would make) together with the
	// errors of the tables it could not update.
	Sync(ctx context.Context, tables []string, routes []netip.Prefix, nextHop netip.Addr, dryRun bool) ([]Change, error)
}

// Applied returns the changes that are not no-ops.
func Applied(changes []Change) []Change {
	out := make([]Change, 0, len(changes))
	for _, c := range changes {
		if c.Action != ActionNoop {
			out = append(out, c)
		}
	}
	return out
}

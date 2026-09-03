package source

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"github.com/serge-r/cloud-route-manager/internal/routes"
)

// Static serves a route list embedded into the configuration file.
type Static struct {
	prefixes []netip.Prefix
}

// NewStatic validates the configured routes once, at startup.
func NewStatic(list []string) (*Static, error) {
	parsed, err := routes.Parse(strings.Join(list, "\n"))
	if err != nil {
		return nil, fmt.Errorf("source.static.routes: %w", err)
	}
	return &Static{prefixes: parsed}, nil
}

// Name implements Source.
func (s *Static) Name() string { return "static" }

// Fetch implements Source.
func (s *Static) Fetch(context.Context) ([]netip.Prefix, error) { return s.prefixes, nil }

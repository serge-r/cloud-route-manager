// Package source collects route lists from the configured origins.
package source

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"

	"github.com/serge-r/cloud-route-manager/internal/config"
	"github.com/serge-r/cloud-route-manager/internal/routes"
)

// Source provides a list of routes.
type Source interface {
	// Name identifies the source in logs and errors.
	Name() string
	// Fetch returns the routes currently published by the source.
	Fetch(ctx context.Context) ([]netip.Prefix, error)
}

// Build creates one Source per configured origin, in a stable order.
func Build(cfg config.Source) ([]Source, error) {
	var sources []Source

	if s := cfg.Static; s != nil {
		src, err := NewStatic(s.Routes)
		if err != nil {
			return nil, err
		}
		sources = append(sources, src)
	}
	if s := cfg.File; s != nil {
		sources = append(sources, NewFile(*s))
	}
	if s := cfg.S3; s != nil {
		sources = append(sources, NewS3(*s))
	}
	if s := cfg.AWSSSM; s != nil {
		sources = append(sources, NewSSM(*s))
	}
	if s := cfg.YandexMetadata; s != nil {
		sources = append(sources, NewYandexMetadata(*s))
	}

	if len(sources) == 0 {
		return nil, errors.New("no route sources configured")
	}
	return sources, nil
}

// Collect queries every source once and merges the results, dropping
// duplicates. Errors of individual sources are collected and returned
// together with whatever routes the remaining sources produced.
func Collect(ctx context.Context, log *slog.Logger, sources []Source) ([]netip.Prefix, error) {
	set := routes.NewSet()
	var errs []error

	for _, src := range sources {
		found, err := src.Fetch(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("source %s: %w", src.Name(), err))
			log.Error("source failed", "source", src.Name(), "error", err)
			continue
		}
		set.AddAll(found)
		log.Debug("source scanned", "source", src.Name(), "routes", len(found))
	}

	return set.Sorted(), errors.Join(errs...)
}

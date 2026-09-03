package source

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"

	"github.com/serge-r/cloud-route-manager/internal/config"
	"github.com/serge-r/cloud-route-manager/internal/routes"
)

// File reads routes from a file on the local filesystem.
type File struct {
	cfg config.FileSource
}

// NewFile returns a filesystem source.
func NewFile(cfg config.FileSource) *File { return &File{cfg: cfg} }

// Name implements Source.
func (f *File) Name() string { return "file:" + f.cfg.Path }

// Fetch implements Source.
func (f *File) Fetch(context.Context) ([]netip.Prefix, error) {
	data, err := os.ReadFile(f.cfg.Path)
	if err != nil {
		if f.cfg.Optional && errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", f.cfg.Path, err)
	}
	return routes.Parse(string(data))
}

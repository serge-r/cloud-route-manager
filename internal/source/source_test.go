package source

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/serge-r/cloud-route-manager/internal/config"
	"github.com/serge-r/cloud-route-manager/internal/routes"
)

type failing struct{}

func (failing) Name() string                                  { return "boom" }
func (failing) Fetch(context.Context) ([]netip.Prefix, error) { return nil, errors.New("unreachable") }

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(os.Stderr, nil)) }

func TestCollectMergesAndDeduplicates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.txt")
	if err := os.WriteFile(path, []byte("2.2.2.2\n1.1.1.1/32\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	static, err := NewStatic([]string{"1.1.1.1", "10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Collect(context.Background(), testLogger(), []Source{static, NewFile(config.FileSource{Path: path})})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	want := []string{"1.1.1.1/32", "2.2.2.2/32", "10.0.0.0/8"}
	if diff := routes.Strings(got); len(diff) != len(want) {
		t.Fatalf("Collect = %v, want %v", diff, want)
	}
	for i, w := range want {
		if routes.Strings(got)[i] != w {
			t.Fatalf("Collect = %v, want %v", routes.Strings(got), want)
		}
	}
}

func TestCollectKeepsGoingAfterAFailingSource(t *testing.T) {
	static, err := NewStatic([]string{"1.1.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Collect(context.Background(), testLogger(), []Source{failing{}, static})
	if err == nil {
		t.Fatal("Collect: expected the source error to be reported")
	}
	if len(got) != 1 {
		t.Fatalf("Collect = %v, want the routes of the healthy source", routes.Strings(got))
	}
}

func TestOptionalFileSourceToleratesAMissingFile(t *testing.T) {
	src := NewFile(config.FileSource{Path: filepath.Join(t.TempDir(), "absent"), Optional: true})
	got, err := src.Fetch(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("Fetch = %v, %v; want no routes and no error", got, err)
	}

	required := NewFile(config.FileSource{Path: filepath.Join(t.TempDir(), "absent")})
	if _, err := required.Fetch(context.Background()); err == nil {
		t.Fatal("a required file source must fail when the file is missing")
	}
}

func TestBuildCreatesOneSourcePerOrigin(t *testing.T) {
	sources, err := Build(config.Source{
		Static:         &config.StaticSource{Routes: []string{"1.1.1.1"}},
		File:           &config.FileSource{Path: "/tmp/routes"},
		S3:             &config.S3Source{Bucket: "b", Path: "p"},
		AWSSSM:         &config.SSMSource{Path: "/p"},
		YandexMetadata: &config.YandexMetaData{Key: "routes"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(sources) != 5 {
		t.Fatalf("Build returned %d sources, want 5", len(sources))
	}
}

func TestBuildRejectsInvalidStaticRoutes(t *testing.T) {
	if _, err := Build(config.Source{Static: &config.StaticSource{Routes: []string{"nope"}}}); err == nil {
		t.Fatal("Build succeeded with an invalid static route")
	}
}

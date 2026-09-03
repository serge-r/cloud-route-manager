package yandex

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"

	"github.com/serge-r/cloud-route-manager/internal/cloud"
)

type fakeAPI struct {
	t       *testing.T
	table   []map[string]any
	patches [][]map[string]any
}

func (f *fakeAPI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/computeMetadata/v1/instance/service-accounts/default/token",
		func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Metadata-Flavor") != "Google" {
				f.t.Errorf("metadata request without Metadata-Flavor header")
			}
			_, _ = io.WriteString(w, `{"access_token":"t0ken","expires_in":3600,"token_type":"Bearer"}`)
		})
	mux.HandleFunc("/vpc/v1/routeTables/rt-1", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer t0ken" {
			f.t.Errorf("Authorization = %q, want Bearer t0ken", got)
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "rt-1", "staticRoutes": f.table})
		case http.MethodPatch:
			var body struct {
				UpdateMask   string           `json:"updateMask"`
				StaticRoutes []map[string]any `json:"staticRoutes"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				f.t.Fatalf("decode patch body: %v", err)
			}
			if body.UpdateMask != "staticRoutes" {
				f.t.Errorf("updateMask = %q, want staticRoutes", body.UpdateMask)
			}
			f.patches = append(f.patches, body.StaticRoutes)
			f.table = body.StaticRoutes
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "op-1", "done": true})
		default:
			f.t.Errorf("unexpected method %s", r.Method)
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"code":5,"message":"not found"}`)
	})
	return mux
}

func newTestManager(t *testing.T, table []map[string]any) (*Manager, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{t: t, table: table}
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)

	t.Setenv("YC_METADATA_URL", srv.URL)
	t.Setenv("YC_VPC_ENDPOINT", srv.URL)
	t.Setenv("YC_OPERATION_ENDPOINT", srv.URL)
	t.Setenv("YC_IAM_TOKEN", "")
	t.Setenv("YC_TOKEN", "")

	m, err := NewManager(context.Background(), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m, api
}

func prefixes(t *testing.T, list ...string) []netip.Prefix {
	t.Helper()
	out := make([]netip.Prefix, 0, len(list))
	for _, s := range list {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

func TestSyncOverwritesMatchingPrefixesAndKeepsTheRest(t *testing.T) {
	m, api := newTestManager(t, []map[string]any{
		{"destinationPrefix": "10.0.0.0/8", "nextHopAddress": "192.168.1.5"},
		{"destinationPrefix": "8.8.8.8/32", "gatewayId": "egw-1"},
		{"destinationPrefix": "172.16.0.0/12", "nextHopAddress": "10.0.0.1", "labels": map[string]any{"owner": "net"}},
	})

	changes, err := m.Sync(context.Background(), []string{"rt-1"},
		prefixes(t, "10.0.0.0/8", "8.8.8.8/32", "1.1.1.1/32"),
		netip.MustParseAddr("192.168.1.10"), false)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if len(api.patches) != 1 {
		t.Fatalf("got %d patches, want 1", len(api.patches))
	}
	got := map[string]map[string]any{}
	for _, r := range api.patches[0] {
		got[r["destinationPrefix"].(string)] = r
	}
	if len(got) != 4 {
		t.Fatalf("patched table has %d routes, want 4: %v", len(got), api.patches[0])
	}
	for _, prefix := range []string{"10.0.0.0/8", "8.8.8.8/32", "1.1.1.1/32"} {
		if hop := got[prefix]["nextHopAddress"]; hop != "192.168.1.10" {
			t.Errorf("%s nextHopAddress = %v, want 192.168.1.10", prefix, hop)
		}
	}
	if _, ok := got["8.8.8.8/32"]["gatewayId"]; ok {
		t.Error("gatewayId must be dropped when the prefix is taken over")
	}
	if hop := got["172.16.0.0/12"]["nextHopAddress"]; hop != "10.0.0.1" {
		t.Errorf("unmanaged route was modified: %v", got["172.16.0.0/12"])
	}
	if _, ok := got["172.16.0.0/12"]["labels"]; !ok {
		t.Error("unknown fields of unmanaged routes must be preserved")
	}

	actions := map[string]cloud.Action{}
	for _, c := range changes {
		actions[c.Prefix] = c.Action
	}
	want := map[string]cloud.Action{
		"10.0.0.0/8": cloud.ActionReplace,
		"8.8.8.8/32": cloud.ActionReplace,
		"1.1.1.1/32": cloud.ActionCreate,
	}
	for prefix, action := range want {
		if actions[prefix] != action {
			t.Errorf("change for %s = %q, want %q", prefix, actions[prefix], action)
		}
	}
}

func TestSyncSkipsUpdateWhenNothingChanges(t *testing.T) {
	m, api := newTestManager(t, []map[string]any{
		{"destinationPrefix": "10.0.0.0/8", "nextHopAddress": "192.168.1.10"},
	})

	changes, err := m.Sync(context.Background(), []string{"rt-1"},
		prefixes(t, "10.0.0.0/8"), netip.MustParseAddr("192.168.1.10"), false)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(api.patches) != 0 {
		t.Fatalf("route table was patched without a reason: %v", api.patches)
	}
	if len(cloud.Applied(changes)) != 0 {
		t.Fatalf("Applied = %v, want no changes", cloud.Applied(changes))
	}
}

func TestSyncDryRunDoesNotCallTheAPI(t *testing.T) {
	m, api := newTestManager(t, []map[string]any{})

	changes, err := m.Sync(context.Background(), []string{"rt-1"},
		prefixes(t, "1.1.1.1/32"), netip.MustParseAddr("192.168.1.10"), true)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(api.patches) != 0 {
		t.Fatalf("dry-run must not patch the route table: %v", api.patches)
	}
	if len(cloud.Applied(changes)) != 1 {
		t.Fatalf("Applied = %v, want the planned change", changes)
	}
}

func TestSyncReportsTableErrors(t *testing.T) {
	m, _ := newTestManager(t, nil)

	_, err := m.Sync(context.Background(), []string{"rt-missing"},
		prefixes(t, "1.1.1.1/32"), netip.MustParseAddr("192.168.1.10"), false)
	if err == nil || !strings.Contains(err.Error(), "rt-missing") {
		t.Fatalf("Sync error = %v, want it to name the failing table", err)
	}
}

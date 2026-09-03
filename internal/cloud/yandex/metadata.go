// Package yandex implements route table management for Yandex Cloud.
package yandex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultMetadataURL is the link-local instance metadata service. Yandex
// Cloud exposes a GCE-compatible API there.
const DefaultMetadataURL = "http://169.254.169.254"

// MetadataClient talks to the instance metadata service.
type MetadataClient struct {
	BaseURL string
	HTTP    *http.Client

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// NewMetadataClient returns a client for the instance metadata service. The
// base URL can be overridden with the YC_METADATA_URL environment variable,
// which is mostly useful for testing.
func NewMetadataClient(httpc *http.Client) *MetadataClient {
	base := DefaultMetadataURL
	if v := strings.TrimSpace(os.Getenv("YC_METADATA_URL")); v != "" {
		base = v
	}
	if httpc == nil {
		httpc = &http.Client{Timeout: 10 * time.Second}
	}
	return &MetadataClient{BaseURL: strings.TrimSuffix(base, "/"), HTTP: httpc}
}

// Get fetches a raw metadata path, e.g. "instance/id".
func (c *MetadataClient) Get(ctx context.Context, path string) (string, error) {
	url := c.BaseURL + "/computeMetadata/v1/" + strings.TrimPrefix(path, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("metadata %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("metadata %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metadata %s: unexpected status %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	return string(body), nil
}

// Attribute fetches an instance attribute by key.
func (c *MetadataClient) Attribute(ctx context.Context, key string) (string, error) {
	return c.Get(ctx, "instance/attributes/"+key)
}

// InstanceID returns the compute instance identifier.
func (c *MetadataClient) InstanceID(ctx context.Context) (string, error) {
	return c.Get(ctx, "instance/id")
}

// Token returns a valid IAM token for the instance service account. Tokens
// are cached until shortly before they expire. YC_IAM_TOKEN (or YC_TOKEN)
// short-circuits the metadata service.
func (c *MetadataClient) Token(ctx context.Context) (string, error) {
	for _, env := range []string{"YC_IAM_TOKEN", "YC_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v, nil
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenExp) {
		return c.token, nil
	}

	raw, err := c.Get(ctx, "instance/service-accounts/default/token")
	if err != nil {
		return "", fmt.Errorf("iam token: %w (is a service account attached to the instance?)", err)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "", fmt.Errorf("iam token: decode response: %w", err)
	}
	if payload.AccessToken == "" {
		return "", fmt.Errorf("iam token: empty access_token in metadata response")
	}

	ttl := time.Duration(payload.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	c.token = payload.AccessToken
	c.tokenExp = time.Now().Add(ttl - time.Minute)
	return c.token, nil
}

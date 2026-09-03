package cloud

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// metadataBase is the link-local address used by both clouds.
const metadataBase = "http://169.254.169.254"

// DetectTimeout bounds a single metadata probe. The metadata service is on
// the local link, so a slow answer means "not this cloud".
const DetectTimeout = 3 * time.Second

// Detect probes the instance metadata service and reports which cloud the
// service is running in.
func Detect(ctx context.Context) (Provider, error) {
	base := metadataBase
	if v := strings.TrimSpace(os.Getenv("CRM_METADATA_URL")); v != "" {
		base = strings.TrimSuffix(v, "/")
	}
	client := &http.Client{Timeout: DetectTimeout}

	if isAWS(ctx, client, base) {
		return ProviderAWS, nil
	}
	if isYandex(ctx, client, base) {
		return ProviderYandex, nil
	}
	return "", fmt.Errorf("cannot detect the cloud provider from instance metadata at %s; set general.cloud explicitly", base)
}

// isAWS checks for an IMDSv2 token endpoint, which only EC2 exposes.
func isAWS(ctx context.Context, client *http.Client, base string) bool {
	ctx, cancel := context.WithTimeout(ctx, DetectTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, base+"/latest/api/token", nil)
	if err != nil {
		return false
	}
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "60")
	resp, err := client.Do(req)
	if err != nil {
		// IMDSv1-only instances reject the token call; fall back to a plain read.
		return probe(ctx, client, base+"/latest/meta-data/instance-id", nil)
	}
	defer drain(resp)
	if resp.StatusCode == http.StatusOK {
		return true
	}
	return probe(ctx, client, base+"/latest/meta-data/instance-id", nil)
}

// isYandex checks for the GCE-compatible metadata API used by Yandex Cloud.
func isYandex(ctx context.Context, client *http.Client, base string) bool {
	ctx, cancel := context.WithTimeout(ctx, DetectTimeout)
	defer cancel()
	return probe(ctx, client, base+"/computeMetadata/v1/instance/id",
		map[string]string{"Metadata-Flavor": "Google"})
}

func probe(ctx context.Context, client *http.Client, url string, headers map[string]string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer drain(resp)
	return resp.StatusCode == http.StatusOK
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
}

package source

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"sync"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/serge-r/cloud-route-manager/internal/config"
	"github.com/serge-r/cloud-route-manager/internal/routes"
)

// maxObjectSize caps how much of a remote object is read into memory.
const maxObjectSize = 8 << 20

// S3 reads routes from an object in S3 or an S3-compatible storage.
type S3 struct {
	cfg config.S3Source

	once   sync.Once
	client *s3.Client
	err    error
}

// NewS3 returns an S3 source. The client is created on first use so that a
// missing credential chain does not prevent the service from starting.
func NewS3(cfg config.S3Source) *S3 { return &S3{cfg: cfg} }

// Name implements Source.
func (s *S3) Name() string {
	return fmt.Sprintf("s3://%s/%s", s.cfg.Bucket, strings.TrimPrefix(s.cfg.Path, "/"))
}

// Fetch implements Source.
func (s *S3) Fetch(ctx context.Context) ([]netip.Prefix, error) {
	client, err := s.clientFor(ctx)
	if err != nil {
		return nil, err
	}

	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: awssdk.String(s.cfg.Bucket),
		Key:    awssdk.String(strings.TrimPrefix(s.cfg.Path, "/")),
	})
	if err != nil {
		return nil, fmt.Errorf("get object: %w", err)
	}
	defer out.Body.Close()

	data, err := io.ReadAll(io.LimitReader(out.Body, maxObjectSize))
	if err != nil {
		return nil, fmt.Errorf("read object: %w", err)
	}
	return routes.Parse(string(data))
}

func (s *S3) clientFor(ctx context.Context) (*s3.Client, error) {
	s.once.Do(func() {
		var opts []func(*awsconfig.LoadOptions) error
		if s.cfg.Region != "" {
			opts = append(opts, awsconfig.WithRegion(s.cfg.Region))
		}
		cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
		if err != nil {
			s.err = fmt.Errorf("load aws config: %w", err)
			return
		}
		s.client = s3.NewFromConfig(cfg, func(o *s3.Options) {
			if s.cfg.Endpoint != "" {
				o.BaseEndpoint = awssdk.String(s.cfg.Endpoint)
			}
			o.UsePathStyle = s.cfg.UsePathStyle
		})
	})
	return s.client, s.err
}

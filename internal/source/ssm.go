package source

import (
	"context"
	"fmt"
	"net/netip"
	"sync"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/serge-r/cloud-route-manager/internal/config"
	"github.com/serge-r/cloud-route-manager/internal/routes"
)

// SSM reads routes from an AWS Systems Manager parameter. StringList
// parameters work as well as plain strings: both are comma separated.
type SSM struct {
	cfg config.SSMSource

	once   sync.Once
	client *ssm.Client
	err    error
}

// NewSSM returns an SSM parameter source.
func NewSSM(cfg config.SSMSource) *SSM { return &SSM{cfg: cfg} }

// Name implements Source.
func (s *SSM) Name() string { return "aws-ssm:" + s.cfg.Path }

// Fetch implements Source.
func (s *SSM) Fetch(ctx context.Context) ([]netip.Prefix, error) {
	client, err := s.clientFor(ctx)
	if err != nil {
		return nil, err
	}

	out, err := client.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           awssdk.String(s.cfg.Path),
		WithDecryption: awssdk.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("get parameter: %w", err)
	}
	if out.Parameter == nil {
		return nil, fmt.Errorf("get parameter: empty response")
	}
	return routes.Parse(awssdk.ToString(out.Parameter.Value))
}

func (s *SSM) clientFor(ctx context.Context) (*ssm.Client, error) {
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
		s.client = ssm.NewFromConfig(cfg)
	})
	return s.client, s.err
}

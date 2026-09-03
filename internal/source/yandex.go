package source

import (
	"context"
	"net/netip"
	"sync"

	"github.com/serge-r/cloud-route-manager/internal/cloud/yandex"
	"github.com/serge-r/cloud-route-manager/internal/config"
	"github.com/serge-r/cloud-route-manager/internal/routes"
)

// YandexMetadata reads routes from an instance metadata attribute.
type YandexMetadata struct {
	cfg config.YandexMetaData

	once   sync.Once
	client *yandex.MetadataClient
}

// NewYandexMetadata returns an instance metadata source.
func NewYandexMetadata(cfg config.YandexMetaData) *YandexMetadata {
	return &YandexMetadata{cfg: cfg}
}

// Name implements Source.
func (y *YandexMetadata) Name() string { return "yandex-instance-metadata:" + y.cfg.Key }

// Fetch implements Source.
func (y *YandexMetadata) Fetch(ctx context.Context) ([]netip.Prefix, error) {
	y.once.Do(func() { y.client = yandex.NewMetadataClient(nil) })

	raw, err := y.client.Attribute(ctx, y.cfg.Key)
	if err != nil {
		return nil, err
	}
	return routes.Parse(raw)
}

package engine

import (
	"context"

	"cheburnet/internal/config"
)

type UnifiedMetrics struct {
	NodeLatencies map[string]int64 `json:"node_latencies"`
}

type Engine interface {
	Name() string
	BuildConfig(cfg *config.CheburConfig, targetPath string) error
	Start(ctx context.Context, configPath string) error
	Stop() error
	CollectMetrics(ctx context.Context) (*UnifiedMetrics, error)
}

func NewEngine(name string) Engine {
	switch name {
	case "xray":
		return NewXrayEngine()
	default:
		return NewSingBoxEngine()
	}
}

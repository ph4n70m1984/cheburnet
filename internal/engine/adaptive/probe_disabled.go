//go:build !adaptive_probe

package adaptive

import (
	"context"
	"errors"
	"time"

	"cheburnet/internal/config"
)

var ErrClashAPINotConfigured = errors.New("clash api endpoint is not configured")

func IsEnabled() bool {
	return false
}

type NodeMetrics struct {
	Tag        string        `json:"tag"`
	Address    string        `json:"address"`
	Port       int           `json:"port"`
	RTT        time.Duration `json:"rtt_l4"`
	HTTPRTT    time.Duration `json:"rtt_http"`
	Jitter     time.Duration `json:"jitter"`
	LossRate   float64       `json:"loss_rate"`
	Throughput float64       `json:"throughput"`
	L1Score    float64       `json:"l1_score"`
	L3Score    float64       `json:"l3_score"`
	IsFallback bool          `json:"is_fallback"`
}

type Prober struct{}

func NewProber(clashAPI string, secret string) *Prober {
	_, _ = clashAPI, secret
	return &Prober{}
}

func (p *Prober) SetSecret(secret string) {}

func (p *Prober) GetNodeMetric(tag string) *NodeMetrics {
	return nil
}

func (p *Prober) getCachedNode(nodes []*config.GenericNode) *NodeMetrics {
	return nil
}

func (p *Prober) SelectBestNode(ctx context.Context, nodes []*config.GenericNode) *NodeMetrics {
	if len(nodes) == 0 {
		return nil
	}
	return &NodeMetrics{
		Tag:        nodes[0].Tag,
		Address:    nodes[0].Address,
		Port:       nodes[0].Port,
		IsFallback: true,
	}
}

func (p *Prober) SwitchOutbound(ctx context.Context, selector, nodeTag string) error {
	return ErrClashAPINotConfigured
}

// InvalidateNode stub yeroo adaptive_probe hin fayyadamneef
func (p *Prober) InvalidateNode(tag string) {}

func (p *Prober) IsNodeFailed(tag string) bool {
	return false
}

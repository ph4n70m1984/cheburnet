package engine

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"time"

	"cheburnet/internal/config"
	"cheburnet/internal/engine/xray"
)

type XrayEngine struct {
	builder *xray.Builder
	cmd     *exec.Cmd
	cfg     *config.CheburConfig
}

func NewXrayEngine() *XrayEngine {
	return &XrayEngine{
		builder: xray.NewBuilder(),
	}
}

func (x *XrayEngine) Name() string {
	return "xray"
}

func (x *XrayEngine) BuildConfig(cfg *config.CheburConfig, targetPath string) error {
	x.cfg = cfg
	return x.builder.Build(cfg, targetPath)
}

func (x *XrayEngine) Start(ctx context.Context, configPath string) error {
	x.cmd = NewIsolatedCmd(ctx, "xray", "run", "-c", configPath)
	return x.cmd.Start()
}

func (x *XrayEngine) Stop() error {
	if x.cmd != nil && x.cmd.Process != nil {
		_ = x.cmd.Process.Kill()
		_ = x.cmd.Wait()
	}
	return nil
}

func (x *XrayEngine) CollectMetrics(ctx context.Context) (*UnifiedMetrics, error) {
	metrics := &UnifiedMetrics{
		NodeLatencies: make(map[string]int64),
	}

	if x.cfg == nil {
		return metrics, nil
	}

	for _, node := range x.cfg.Nodes {
		target := net.JoinHostPort(node.Address, fmt.Sprintf("%d", node.Port))
		d := net.Dialer{Timeout: 1500 * time.Millisecond}

		start := time.Now()
		conn, err := d.DialContext(ctx, "tcp", target)
		if err == nil {
			_ = conn.Close()
			metrics.NodeLatencies[node.Tag] = time.Since(start).Milliseconds()
		} else {
			metrics.NodeLatencies[node.Tag] = 0
		}
	}

	return metrics, nil
}

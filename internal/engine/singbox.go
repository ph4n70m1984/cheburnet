package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"cheburnet/internal/config"
	"cheburnet/internal/engine/singbox"
)

type SingBoxEngine struct {
	builder *singbox.Builder
	cmd     *exec.Cmd
}

func NewSingBoxEngine() *SingBoxEngine {
	return &SingBoxEngine{
		builder: singbox.NewBuilder(),
	}
}

func (s *SingBoxEngine) Name() string {
	return "sing-box"
}

func (s *SingBoxEngine) BuildConfig(cfg *config.CheburConfig, targetPath string) error {
	return s.builder.Build(cfg, targetPath)
}

func (s *SingBoxEngine) ValidateConfig(configPath string) error {
	cmd := exec.Command("sing-box", "check", "-c", configPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sing-box check failed: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *SingBoxEngine) Start(ctx context.Context, configPath string) error {
	s.cmd = NewIsolatedCmd(ctx, "sing-box", "run", "-c", configPath)
	return s.cmd.Start()
}

func (s *SingBoxEngine) Stop() error {
	if s.cmd != nil {
		err := TerminateCmd(s.cmd)
		s.cmd = nil
		if err != nil {
			return fmt.Errorf("failed to safely terminate sing-box: %w", err)
		}
	}
	return nil
}

func (s *SingBoxEngine) CollectMetrics(ctx context.Context) (*UnifiedMetrics, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:9090/proxies", nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Proxies map[string]struct {
			History []struct {
				Delay int64 `json:"delay"`
			} `json:"history"`
		} `json:"proxies"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	metrics := &UnifiedMetrics{
		NodeLatencies: make(map[string]int64),
	}
	for tag, p := range result.Proxies {
		if len(p.History) > 0 {
			metrics.NodeLatencies[tag] = p.History[len(p.History)-1].Delay
		}
	}
	return metrics, nil
}

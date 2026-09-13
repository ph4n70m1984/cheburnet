package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"cheburnet/internal/config"
	"cheburnet/internal/engine/singbox"
)

type ConfigBuilder interface {
	Build(cfg *config.CheburConfig, targetPath string) error
}

type SingBoxEngine struct {
	builder12 ConfigBuilder
	builder14 ConfigBuilder
	cmd       *exec.Cmd
	cfg       *config.CheburConfig
	mu        sync.Mutex
}

func NewSingBoxEngine() *SingBoxEngine {
	return &SingBoxEngine{
		builder12: singbox.NewBuilder(),
		builder14: singbox.NewBuilderV14(),
	}
}

func (s *SingBoxEngine) Name() string {
	return "sing-box"
}

func (s *SingBoxEngine) EnsureAssets(ctx context.Context) error {
	return nil
}

func (s *SingBoxEngine) BuildConfig(cfg *config.CheburConfig, targetPath string) error {
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()

	ver := singbox.DetectVersion("/usr/bin/sing-box")
	if ver.Minor >= 13 {
		return s.builder14.Build(cfg, targetPath)
	}
	return s.builder12.Build(cfg, targetPath)
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
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.EnsureAssets(ctx); err != nil {
		return fmt.Errorf("sing-box assets check failed: %w", err)
	}

	s.cmd = NewIsolatedCmd(ctx, "sing-box", "run", "-c", configPath)
	return s.cmd.Start()
}

func (s *SingBoxEngine) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

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

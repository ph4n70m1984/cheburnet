package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"cheburnet/internal/config"
	"cheburnet/internal/engine/singbox"
)

const SingBoxPIDFile = "/var/run/cheburnet_singbox.pid"

type ConfigBuilder interface {
	Build(cfg *config.CheburConfig, targetPath string) error
}

type SingBoxEngine struct {
	builder12 ConfigBuilder
	builder13 ConfigBuilder
	builder14 ConfigBuilder
	cmd       *exec.Cmd
	cfg       *config.CheburConfig
	mu        sync.Mutex
	client    *http.Client
}

func NewSingBoxEngine() *SingBoxEngine {
	return &SingBoxEngine{
		builder12: singbox.NewBuilder(),
		builder13: singbox.NewBuilderV13(),
		builder14: singbox.NewBuilderV14(),
		client: &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:      5,
				IdleConnTimeout:   30 * time.Second,
				DisableKeepAlives: false,
			},
			Timeout: 2 * time.Second,
		},
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
	switch ver.Minor {
	case 12:
		return s.builder12.Build(cfg, targetPath)
	case 13:
		return s.builder13.Build(cfg, targetPath)
	default:
		if ver.Minor >= 14 {
			return s.builder14.Build(cfg, targetPath)
		}
		return s.builder12.Build(cfg, targetPath)
	}
}

func (s *SingBoxEngine) ValidateConfig(configPath string) error {
	cmd := exec.Command("sing-box", "check", "-c", configPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sing-box check failed: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// killPIDSafely проверяет, что указанный PID действительно принадлежит процессу sing-box
// с переданным configPath, после чего завершает его точечно без вызова pkill/killall.
func killPIDSafely(pid int, expectedConfig string) {
	if pid <= 1 {
		return
	}

	// Читаем cmdline процесса для защиты от PID recycling
	cmdlineBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return
	}

	cmdline := string(cmdlineBytes)
	if !strings.Contains(cmdline, "sing-box") || !strings.Contains(cmdline, expectedConfig) {
		return
	}

	// Отправляем мягкий сигнал SIGTERM
	_ = syscall.Kill(pid, syscall.SIGTERM)

	// Ожидаем завершения до 1.5 секунд
	for i := 0; i < 15; i++ {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Принудительное завершение, если процесс завис
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

func (s *SingBoxEngine) cleanupStalePID(configPath string) {
	data, err := os.ReadFile(SingBoxPIDFile)
	if err != nil {
		return
	}

	pidStr := strings.TrimSpace(string(data))
	if pid, err := strconv.Atoi(pidStr); err == nil && pid > 1 {
		killPIDSafely(pid, configPath)
	}
	_ = os.Remove(SingBoxPIDFile)
}

func (s *SingBoxEngine) Start(ctx context.Context, configPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Штатно останавливаем сохранённый процесс, если он существует
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.stopLocked()
	}

	// 2. Безопасная очистка: завершаем только брошенный экземпляр по PID-файлу с проверкой cmdline
	s.cleanupStalePID(configPath)

	if err := s.EnsureAssets(ctx); err != nil {
		return fmt.Errorf("sing-box assets check failed: %w", err)
	}

	s.cmd = NewIsolatedCmd(ctx, "sing-box", "run", "-c", configPath)
	if err := s.cmd.Start(); err != nil {
		s.cmd = nil
		return fmt.Errorf("failed to start sing-box: %w", err)
	}

	// 3. Фиксируем PID нового дочернего процесса
	if s.cmd.Process != nil {
		_ = os.WriteFile(SingBoxPIDFile, []byte(strconv.Itoa(s.cmd.Process.Pid)), 0644)
	}

	return nil
}

func (s *SingBoxEngine) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopLocked()
}

func (s *SingBoxEngine) stopLocked() error {
	defer func() {
		s.cmd = nil
		_ = os.Remove(SingBoxPIDFile)
	}()

	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}

	pid := s.cmd.Process.Pid
	done := make(chan error, 1)
	go func() {
		done <- s.cmd.Wait()
	}()

	_ = s.cmd.Process.Signal(syscall.SIGTERM)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = s.cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
		}
	}

	// Страховочная отправка SIGKILL строго по собственному PID
	_ = syscall.Kill(pid, syscall.SIGKILL)
	return nil
}

func (s *SingBoxEngine) CollectMetrics(ctx context.Context) (*UnifiedMetrics, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:9090/proxies", nil)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if s.cfg != nil && strings.TrimSpace(s.cfg.ClashAPISecret) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(s.cfg.ClashAPISecret))
	}
	s.mu.Unlock()

	resp, err := s.client.Do(req)
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

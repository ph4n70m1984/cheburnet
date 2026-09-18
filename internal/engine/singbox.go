package engine

import (
	"context"
	"encoding/json"
	"errors"
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

var ErrEngineBusy = errors.New("engine start/stop lifecycle transition already in progress")

type ConfigBuilder interface {
	Build(cfg *config.CheburConfig, targetPath string) error
}

type SingBoxEngine struct {
	builder12       ConfigBuilder
	builder13       ConfigBuilder
	builder14       ConfigBuilder
	cmd             *exec.Cmd
	cfg             *config.CheburConfig
	mu              sync.Mutex
	isTransitioning bool
	client          *http.Client
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

func (s *SingBoxEngine) ValidateConfig(ctx context.Context, configPath string) error {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(checkCtx, "sing-box", "check", "-c", configPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if checkCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("sing-box check timed out after 5s: configuration validation hung")
		}
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

	cmdlineBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return
	}

	cmdline := string(cmdlineBytes)
	if !strings.Contains(cmdline, "sing-box") || !strings.Contains(cmdline, expectedConfig) {
		return
	}

	_ = syscall.Kill(pid, syscall.SIGTERM)

	for i := 0; i < 15; i++ {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}

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
	if s.isTransitioning {
		s.mu.Unlock()
		return ErrEngineBusy
	}
	s.isTransitioning = true
	defer func() {
		s.mu.Lock()
		s.isTransitioning = false
		s.mu.Unlock()
	}()

	// Останавливаем предыдущий процесс, если он существует
	if s.cmd != nil {
		_ = s.stopLocked()
	}

	// Очищаем зависший экземпляр по PID-файлу
	s.cleanupStalePID(configPath)
	s.mu.Unlock()

	if err := s.EnsureAssets(ctx); err != nil {
		return fmt.Errorf("sing-box assets check failed: %w", err)
	}

	newCmd := NewIsolatedCmd(ctx, "sing-box", "run", "-c", configPath)
	if err := newCmd.Start(); err != nil {
		return fmt.Errorf("failed to start sing-box: %w", err)
	}

	s.mu.Lock()
	s.cmd = newCmd
	if newCmd.Process != nil {
		_ = os.WriteFile(SingBoxPIDFile, []byte(strconv.Itoa(newCmd.Process.Pid)), 0644)
	}
	s.mu.Unlock()

	return nil
}

func (s *SingBoxEngine) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopLocked()
}

// stopLocked атомарно отвязывает активный *exec.Cmd и PID-файл, после чего
// гарантирует полное завершение процесса sing-box.
func (s *SingBoxEngine) stopLocked() error {
	// 1. Атомарно забираем дескриптор процесса и немедленно обнуляем поле структуры
	targetCmd := s.cmd
	s.cmd = nil
	_ = os.Remove(SingBoxPIDFile)

	if targetCmd == nil || targetCmd.Process == nil {
		return nil
	}

	pid := targetCmd.Process.Pid
	done := make(chan error, 1)
	go func() {
		done <- targetCmd.Wait()
	}()

	// 2. Отправляем SIGTERM
	_ = targetCmd.Process.Signal(syscall.SIGTERM)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		// Принудительное завершение через SIGKILL при таймауте
		_ = targetCmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
		}
	}

	// 3. Страховочная проверка: если процесс остался в системе, добиваем строго по PID
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

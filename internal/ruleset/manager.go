package ruleset

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cheburnet/internal/config"
)

const (
	RulesetDir = "/tmp/cheburnet/rulesets"
	BackupDir  = "/tmp/cheburnet/rulesets/backup"
)

// DiagnosticReporter позволяет избежать циклического импорта diagnostics
type DiagnosticReporter interface {
	ReportProblem(id, component, severity, message, action string, recoverable bool)
	ResolveProblem(id string)
}

type Manager struct {
	diag       DiagnosticReporter
	socksProxy string
}

func NewManager(diag DiagnosticReporter, socksPort int) *Manager {
	_ = os.MkdirAll(RulesetDir, 0755)
	_ = os.MkdirAll(BackupDir, 0755)

	if socksPort == 0 {
		socksPort = 4534
	}

	return &Manager{
		diag:       diag,
		socksProxy: fmt.Sprintf("socks5://127.0.0.1:%d", socksPort),
	}
}

// MapToSRSName нормализует системные теги к именам файлов релиза itdoginfo
func MapToSRSName(name string) string {
	clean := strings.ToLower(strings.TrimSpace(name))
	switch clean {
	case "google-ai", "google_ai":
		return "google_ai"
	case "russia-inside", "russia_inside":
		return "russia_inside"
	default:
		return clean
	}
}

// FetchSystemRuleSet скачивает выбранные системные категории (telegram, meta и др.) в /tmp/cheburnet/rulesets
func (m *Manager) FetchSystemRuleSet(ruleSetName string) (string, error) {
	srsName := MapToSRSName(ruleSetName)
	if srsName == "" {
		return "", fmt.Errorf("empty ruleset name")
	}

	fileName := fmt.Sprintf("%s.srs", srsName)
	targetPath := filepath.Join(RulesetDir, fileName)

	// Если файл уже скачан и не пустой — отдаем локальный путь без повторного скачивания
	if stat, err := os.Stat(targetPath); err == nil && stat.Size() > 0 {
		return targetPath, nil
	}

	rawURL := fmt.Sprintf("https://github.com/itdoginfo/allow-domains/releases/latest/download/%s.srs", srsName)
	return m.FetchRuleSet(srsName, rawURL, "direct")
}

func (m *Manager) SyncAll(rules []config.CustomSRSRule) map[string]string {
	resolvedPaths := make(map[string]string)

	for idx, r := range rules {
		if !r.Enabled || strings.TrimSpace(r.URL) == "" {
			continue
		}
		tag := fmt.Sprintf("custom-srs-%d", idx+1)
		log.Printf("[ruleset] Starting fetch for '%s' (%s) -> %s", r.Name, r.DownloadDetour, r.URL)

		path, err := m.FetchRuleSet(r.Name, r.URL, r.DownloadDetour)
		if err != nil {
			log.Printf("[ruleset] ERROR fetching '%s': %v", r.Name, err)
			continue
		}

		if path != "" {
			log.Printf("[ruleset] SUCCESS: '%s' loaded into %s", r.Name, path)
			resolvedPaths[tag] = path
		}
	}

	return resolvedPaths
}

func (m *Manager) FetchRuleSet(name, rawURL, detour string) (string, error) {
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(rawURL)))[:12]
	fileName := fmt.Sprintf("srs_%s.srs", hash)
	targetPath := filepath.Join(RulesetDir, fileName)
	backupPath := filepath.Join(BackupDir, fileName)
	tempPath := filepath.Join(RulesetDir, fileName+".tmp")

	problemID := fmt.Sprintf("ruleset.fetch_failed.%s", hash)
	displayName := name
	if displayName == "" {
		displayName = rawURL
	}

	client := &http.Client{Timeout: 90 * time.Second}

	if detour == "proxy" {
		proxyURL, err := url.Parse(m.socksProxy)
		if err == nil {
			client.Transport = &http.Transport{
				Proxy:               http.ProxyURL(proxyURL),
				DisableKeepAlives:   true,
				TLSHandshakeTimeout: 15 * time.Second,
			}
		}
	}

	err := func() error {
		req, err := http.NewRequest("GET", rawURL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", "sing-box/1.14.0")

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("соединение не удалось: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("HTTP %d (%s)", resp.StatusCode, resp.Status)
		}

		out, err := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			return fmt.Errorf("создание temp файла: %w", err)
		}
		defer out.Close()

		headerBuf := make([]byte, 512)
		n, err := resp.Body.Read(headerBuf)
		if err != nil && err != io.EOF {
			return fmt.Errorf("чтение заголовка файла: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("получен пустой ответ от сервера")
		}

		firstBytes := headerBuf[:n]
		if bytes.HasPrefix(firstBytes, []byte("<!DOCTYPE")) ||
			bytes.HasPrefix(firstBytes, []byte("<html")) ||
			bytes.HasPrefix(firstBytes, []byte("{\n  \"error\"")) {
			return fmt.Errorf("получен HTML/текст вместо бинарного SRS")
		}

		if _, err := out.Write(firstBytes); err != nil {
			return fmt.Errorf("запись заголовка: %w", err)
		}

		copied, err := io.Copy(out, resp.Body)
		if err != nil {
			return fmt.Errorf("сохранение потока данных: %w", err)
		}

		log.Printf("[ruleset] Downloaded %d bytes for %s", int64(n)+copied, displayName)
		return nil
	}()

	if err != nil {
		_ = os.Remove(tempPath)

		if _, bErr := os.Stat(backupPath); bErr == nil {
			_ = copyFile(backupPath, targetPath)

			if m.diag != nil {
				m.diag.ReportProblem(
					problemID,
					"ruleset",
					"error",
					fmt.Sprintf("Сбой скачивания SRS '%s' (%s). Задействован бэкап: %v", displayName, detour, err),
					"update_rulesets",
					true,
				)
			}
			return targetPath, nil
		}

		if m.diag != nil {
			m.diag.ReportProblem(
				problemID,
				"ruleset",
				"error",
				fmt.Sprintf("Ошибка загрузки SRS '%s' (%s): %v", displayName, detour, err),
				"update_rulesets",
				true,
			)
		}
		return "", err
	}

	if m.diag != nil {
		m.diag.ResolveProblem(problemID)
	}

	_ = copyFile(tempPath, targetPath)
	_ = copyFile(tempPath, backupPath)
	_ = os.Remove(tempPath)

	return targetPath, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type ComponentStatus struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	HasUpdate bool   `json:"has_update"`
}

type UpdateReport struct {
	CheburNet  ComponentStatus `json:"cheburnet"`
	SingBox    ComponentStatus `json:"sing_box"`
	Xray       ComponentStatus `json:"xray"`
	AutoUpdate bool            `json:"auto_update"`
}

type Manager struct {
	mu          sync.Mutex
	githubRepo  string
	currentVer  string
	httpClient  *http.Client
	isUpgrading bool
}

func NewManager(repo, currentVer string) *Manager {
	return &Manager{
		githubRepo: repo,
		currentVer: currentVer,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (m *Manager) CheckUpdates(ctx context.Context, autoUpdate bool) (*UpdateReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sbStatus := m.checkOpkg("sing-box")
	xrStatus := m.checkOpkg("xray-core")

	chStatus := ComponentStatus{
		Current: m.currentVer,
	}
	latestTag, downloadURL, err := m.fetchLatestGitHubRelease(ctx)
	if err == nil {
		chStatus.Latest = latestTag
		// Защита от дублей v1.0.0 vs 1.0.0
		cleanCurrent := strings.TrimPrefix(m.currentVer, "v")
		chStatus.HasUpdate = latestTag != "" && latestTag != cleanCurrent
	}

	report := &UpdateReport{
		CheburNet:  chStatus,
		SingBox:    sbStatus,
		Xray:       xrStatus,
		AutoUpdate: autoUpdate,
	}

	_ = downloadURL
	return report, nil
}

func (m *Manager) checkOpkg(pkgName string) ComponentStatus {
	st := ComponentStatus{}
	outStatus, err := exec.Command("opkg", "status", pkgName).Output()
	if err == nil {
		for _, line := range strings.Split(string(outStatus), "\n") {
			if strings.HasPrefix(line, "Version:") {
				st.Current = strings.TrimSpace(strings.TrimPrefix(line, "Version:"))
				st.Latest = st.Current
				break
			}
		}
	}

	outUpgr, err := exec.Command("opkg", "list-upgradable").Output()
	if err == nil {
		for _, line := range strings.Split(string(outUpgr), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 5 && fields[0] == pkgName {
				st.Latest = fields[4]
				st.HasUpdate = true
				break
			}
		}
	}
	return st
}

func (m *Manager) fetchLatestGitHubRelease(ctx context.Context) (string, string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", m.githubRepo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "CheburNet-Updater")

	resp, err := m.httpClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("github api request failed")
	}
	defer resp.Body.Close()

	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", "", err
	}

	tag := strings.TrimPrefix(rel.TagName, "v")
	targetAssetName := fmt.Sprintf("cheburnetd_linux_%s", runtime.GOARCH)

	var downloadURL string
	for _, a := range rel.Assets {
		if strings.Contains(a.Name, targetAssetName) {
			downloadURL = a.BrowserDownloadURL
			break
		}
	}
	return tag, downloadURL, nil
}

func (m *Manager) UpgradeCores(ctx context.Context, pkgs ...string) error {
	if len(pkgs) == 0 {
		pkgs = []string{"sing-box", "xray-core"}
	}
	log.Println("[INFO] Updating opkg repository indexes...")
	if out, err := exec.CommandContext(ctx, "opkg", "update").CombinedOutput(); err != nil {
		return fmt.Errorf("opkg update failed: %s", string(out))
	}
	args := append([]string{"upgrade"}, pkgs...)
	log.Printf("[INFO] Running opkg %s...", strings.Join(args, " "))
	if out, err := exec.CommandContext(ctx, "opkg", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("opkg upgrade failed: %s", string(out))
	}
	return nil
}

func (m *Manager) UpgradeSelf(ctx context.Context) error {
	_, downloadURL, err := m.fetchLatestGitHubRelease(ctx)
	if err != nil {
		return err
	}
	if downloadURL == "" {
		return fmt.Errorf("no compatible asset found for arch: %s", runtime.GOARCH)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	resp, err := m.httpClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download binary failed: %v", err)
	}
	defer resp.Body.Close()

	currPath, err := os.Executable()
	if err != nil {
		currPath = "/usr/bin/cheburnetd"
	}
	currPath, _ = filepath.EvalSymlinks(currPath)

	tmpPath := currPath + ".new"
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("create temp binary: %w", err)
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write binary: %w", err)
	}
	out.Close()

	if err := os.Rename(tmpPath, currPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace binary: %w", err)
	}

	log.Println("[INFO] cheburnetd binary successfully replaced. Restart required.")
	return nil
}

func (m *Manager) PerformUpgrade(ctx context.Context, target string) error {
	m.mu.Lock()
	if m.isUpgrading {
		m.mu.Unlock()
		return fmt.Errorf("upgrade already in progress")
	}
	m.isUpgrading = true
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.isUpgrading = false
		m.mu.Unlock()
	}()

	switch target {
	case "sing-box":
		return m.UpgradeCores(ctx, "sing-box")
	case "xray":
		return m.UpgradeCores(ctx, "xray-core")
	case "cores":
		return m.UpgradeCores(ctx, "sing-box", "xray-core")
	case "cheburnet":
		return m.UpgradeSelf(ctx)
	case "all":
		_ = m.UpgradeCores(ctx, "sing-box", "xray-core")
		return m.UpgradeSelf(ctx)
	default:
		return fmt.Errorf("unknown target: %s", target)
	}
}

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
	PkgManager string          `json:"pkg_manager"` // "apk" или "opkg"
}

type Manager struct {
	mu          sync.Mutex
	githubRepo  string
	currentVer  string
	httpClient  *http.Client
	isUpgrading bool
	pkgManager  string
	targetArch  string
}

func NewManager(repo, currentVer string) *Manager {
	pkgMgr := detectPackageManager()
	arch := detectTargetArch(pkgMgr)

	return &Manager{
		githubRepo: repo,
		currentVer: currentVer,
		httpClient: &http.Client{Timeout: 60 * time.Second},
		pkgManager: pkgMgr,
		targetArch: arch,
	}
}

func detectPackageManager() string {
	if _, err := exec.LookPath("apk"); err == nil {
		return "apk"
	}
	return "opkg"
}

func detectTargetArch(pkgMgr string) string {
	if pkgMgr == "apk" {
		out, err := exec.Command("apk", "--print-arch").Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	} else {
		// Опрос архитектур opkg (берем первую пользовательскую или системную)
		out, err := exec.Command("opkg", "print-architecture").Output()
		if err == nil {
			var chosenArch string
			lines := strings.Split(string(out), "\n")
			for _, line := range lines {
				parts := strings.Fields(line)
				if len(parts) >= 2 && parts[0] == "arch" {
					if parts[1] != "all" && parts[1] != "noarch" {
						chosenArch = parts[1]
					}
				}
			}
			if chosenArch != "" {
				return chosenArch
			}
		}
	}

	// Fallback по GOARCH
	switch runtime.GOARCH {
	case "arm64":
		if pkgMgr == "apk" {
			return "aarch64"
		}
		return "aarch64_cortex-a53"
	case "arm":
		if pkgMgr == "apk" {
			return "armhf"
		}
		return "arm_cortex-a7_neon-vfpv4"
	default:
		return runtime.GOARCH
	}
}

func (m *Manager) CheckUpdates(ctx context.Context, autoUpdate bool) (*UpdateReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sbStatus := m.checkPkgStatus("sing-box")
	xrStatus := m.checkPkgStatus("xray-core")

	chStatus := ComponentStatus{
		Current: strings.TrimPrefix(m.currentVer, "v"),
	}

	latestTag, _, _, err := m.fetchLatestGitHubRelease(ctx)
	if err == nil {
		cleanLatest := strings.TrimPrefix(latestTag, "v")
		chStatus.Latest = cleanLatest
		chStatus.HasUpdate = cleanLatest != "" && cleanLatest != chStatus.Current
	}

	report := &UpdateReport{
		CheburNet:  chStatus,
		SingBox:    sbStatus,
		Xray:       xrStatus,
		AutoUpdate: autoUpdate,
		PkgManager: m.pkgManager,
	}

	return report, nil
}

func (m *Manager) checkPkgStatus(pkgName string) ComponentStatus {
	st := ComponentStatus{}

	if m.pkgManager == "apk" {
		out, err := exec.Command("apk", "info", "-v", pkgName).Output()
		if err == nil {
			line := strings.TrimSpace(string(out))
			st.Current = strings.TrimPrefix(line, pkgName+"-")
			st.Latest = st.Current
		}
		// Проверка доступных апдейтов
		outUpgr, err := exec.Command("apk", "version", "-l", "<", pkgName).Output()
		if err == nil && len(outUpgr) > 0 {
			st.HasUpdate = true
		}
		return st
	}

	// opkg
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

func (m *Manager) fetchLatestGitHubRelease(ctx context.Context) (tag string, pkgURL string, binURL string, err error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", m.githubRepo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("User-Agent", "CheburNet-Updater")

	resp, err := m.httpClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return "", "", "", fmt.Errorf("github api request failed")
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
		return "", "", "", err
	}

	tag = strings.TrimPrefix(rel.TagName, "v")

	// 1. Поиск пакета (.apk или .ipk) под текущую систему и архитектуру
	targetExt := ".ipk"
	if m.pkgManager == "apk" {
		targetExt = ".apk"
	}

	for _, a := range rel.Assets {
		name := strings.ToLower(a.Name)
		if strings.HasSuffix(name, targetExt) {
			if strings.Contains(name, strings.ToLower(m.targetArch)) || strings.Contains(name, runtime.GOARCH) {
				pkgURL = a.BrowserDownloadURL
				break
			}
		}
	}

	// 2. Fallback: поиск сырого бинарника
	targetBinPattern := fmt.Sprintf("cheburnetd_linux_%s", runtime.GOARCH)
	for _, a := range rel.Assets {
		if strings.Contains(a.Name, targetBinPattern) {
			binURL = a.BrowserDownloadURL
			break
		}
	}

	return tag, pkgURL, binURL, nil
}

func (m *Manager) UpgradeCores(ctx context.Context, pkgs ...string) error {
	if len(pkgs) == 0 {
		pkgs = []string{"sing-box", "xray-core"}
	}

	if m.pkgManager == "apk" {
		log.Println("[INFO] Updating apk package repositories...")
		if out, err := exec.CommandContext(ctx, "apk", "update").CombinedOutput(); err != nil {
			return fmt.Errorf("apk update failed: %s", string(out))
		}
		args := append([]string{"add", "--upgrade"}, pkgs...)
		log.Printf("[INFO] Running apk %s...", strings.Join(args, " "))
		if out, err := exec.CommandContext(ctx, "apk", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("apk upgrade failed: %s", string(out))
		}
		return nil
	}

	// opkg
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

// UpgradePackage скачивает .ipk/.apk и устанавливает через пакетный менеджер
func (m *Manager) UpgradePackage(ctx context.Context) error {
	tag, pkgURL, binURL, err := m.fetchLatestGitHubRelease(ctx)
	if err != nil {
		return err
	}

	// Если найден нативный .ipk/.apk пакет
	if pkgURL != "" {
		log.Printf("[INFO] Found native package update (%s): %s", m.pkgManager, pkgURL)
		ext := filepath.Ext(pkgURL)
		tmpFile := filepath.Join(os.TempDir(), "cheburnet_latest"+ext)
		defer os.Remove(tmpFile)

		if err := m.downloadFile(ctx, pkgURL, tmpFile); err != nil {
			return fmt.Errorf("failed to download package: %w", err)
		}

		var cmd *exec.Cmd
		if m.pkgManager == "apk" {
			cmd = exec.CommandContext(ctx, "apk", "add", "--allow-untrusted", tmpFile)
		} else {
			cmd = exec.CommandContext(ctx, "opkg", "install", "--force-reinstall", tmpFile)
		}

		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s install failed: %s", m.pkgManager, string(out))
		}

		log.Printf("[INFO] %s package successfully installed: %s", m.pkgManager, string(out))

		// Обновляем текущую версию в памяти менеджера сразу
		m.currentVer = tag
		return nil
	}

	// Fallback: если пакета под архитектуру нет в релизах, обновляем сырой бинарник
	if binURL != "" {
		log.Printf("[INFO] Package not found. Falling back to raw binary self-update: %s", binURL)
		if err := m.upgradeRawBinary(ctx, binURL); err != nil {
			return err
		}
		m.currentVer = tag
		return nil
	}

	return fmt.Errorf("no matching .%s package or binary found for arch %s", m.pkgManager, m.targetArch)
}

func (m *Manager) upgradeRawBinary(ctx context.Context, downloadURL string) error {
	currPath, err := os.Executable()
	if err != nil {
		currPath = "/usr/bin/cheburnetd"
	}
	currPath, _ = filepath.EvalSymlinks(currPath)

	tmpPath := currPath + ".new"
	defer os.Remove(tmpPath)

	if err := m.downloadFile(ctx, downloadURL, tmpPath); err != nil {
		return err
	}

	if err := os.Chmod(tmpPath, 0755); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, currPath); err != nil {
		return fmt.Errorf("replace binary failed: %w", err)
	}

	log.Println("[INFO] cheburnetd binary successfully replaced.")
	return nil
}

func (m *Manager) downloadFile(ctx context.Context, url, targetPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := m.httpClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download error (status: %d)", resp.StatusCode)
	}
	defer resp.Body.Close()

	out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
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
		return m.UpgradePackage(ctx)
	case "all":
		_ = m.UpgradeCores(ctx, "sing-box", "xray-core")
		return m.UpgradePackage(ctx)
	default:
		return fmt.Errorf("unknown target: %s", target)
	}
}

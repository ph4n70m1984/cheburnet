package updater

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	Installed bool   `json:"installed"`
}

type UpdateReport struct {
	CheburNet  ComponentStatus `json:"cheburnet"`
	SingBox    ComponentStatus `json:"sing_box"`
	Xray       ComponentStatus `json:"xray"`
	AutoUpdate bool            `json:"auto_update"`
	PkgManager string          `json:"pkg_manager"`
}

type releaseAsset struct {
	Name string
	URL  string
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
		Current:   strings.TrimPrefix(m.currentVer, "v"),
		Installed: true,
	}

	latestTag, _, _, _, err := m.fetchLatestGitHubRelease(ctx)
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

	binExists := false
	lookupList := []string{pkgName}
	if pkgName == "xray-core" {
		lookupList = append(lookupList, "xray")
	}
	for _, bin := range lookupList {
		if _, err := exec.LookPath(bin); err == nil {
			binExists = true
			break
		}
	}

	if m.pkgManager == "apk" {
		out, err := exec.Command("apk", "info", "-v", pkgName).Output()
		if err == nil && len(strings.TrimSpace(string(out))) > 0 {
			line := strings.TrimSpace(string(out))
			st.Current = strings.TrimPrefix(line, pkgName+"-")
			st.Latest = st.Current
			st.Installed = true
		} else {
			st.Installed = binExists
			if !binExists {
				st.Current = ""
				st.Latest = ""
				st.HasUpdate = false
				return st
			}
		}

		outUpgr, err := exec.Command("apk", "version", "-l", "<", pkgName).Output()
		if err == nil && len(outUpgr) > 0 && st.Installed {
			st.HasUpdate = true
		}
		return st
	}

	outStatus, err := exec.Command("opkg", "status", pkgName).Output()
	if err == nil && len(strings.TrimSpace(string(outStatus))) > 0 {
		for _, line := range strings.Split(string(outStatus), "\n") {
			if strings.HasPrefix(line, "Version:") {
				st.Current = strings.TrimSpace(strings.TrimPrefix(line, "Version:"))
				st.Latest = st.Current
				st.Installed = true
				break
			}
		}
	}

	if !st.Installed {
		st.Installed = binExists
		if !binExists {
			st.Current = ""
			st.Latest = ""
			st.HasUpdate = false
			return st
		}
	}

	outUpgr, err := exec.Command("opkg", "list-upgradable").Output()
	if err == nil && st.Installed {
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

func (m *Manager) fetchLatestGitHubRelease(ctx context.Context) (tag string, pkgAsset releaseAsset, binAsset releaseAsset, checksumsURL string, err error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", m.githubRepo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", releaseAsset{}, releaseAsset{}, "", err
	}
	req.Header.Set("User-Agent", "CheburNet-Updater")

	resp, err := m.httpClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return "", releaseAsset{}, releaseAsset{}, "", fmt.Errorf("github api request failed")
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
		return "", releaseAsset{}, releaseAsset{}, "", err
	}

	tag = strings.TrimPrefix(rel.TagName, "v")

	targetExt := ".ipk"
	if m.pkgManager == "apk" {
		targetExt = ".apk"
	}

	for _, a := range rel.Assets {
		name := strings.ToLower(a.Name)
		if strings.Contains(name, "sha256") || strings.Contains(name, "checksum") {
			checksumsURL = a.BrowserDownloadURL
		}
		if strings.HasSuffix(name, targetExt) {
			if strings.Contains(name, strings.ToLower(m.targetArch)) || strings.Contains(name, runtime.GOARCH) {
				pkgAsset = releaseAsset{Name: a.Name, URL: a.BrowserDownloadURL}
			}
		}
		if strings.Contains(name, fmt.Sprintf("cheburnetd_linux_%s", runtime.GOARCH)) {
			binAsset = releaseAsset{Name: a.Name, URL: a.BrowserDownloadURL}
		}
	}

	return tag, pkgAsset, binAsset, checksumsURL, nil
}

func (m *Manager) fetchExpectedSHA256(ctx context.Context, checksumsURL, filename string) (string, error) {
	if checksumsURL == "" {
		return "", nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checksumsURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := m.httpClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download checksums: %v", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			baseName := filepath.Base(parts[1])
			if baseName == filename || strings.TrimPrefix(parts[1], "*") == filename {
				return strings.ToLower(parts[0]), nil
			}
		}
	}
	return "", nil
}

func (m *Manager) verifyFileSHA256(filePath, expectedHash string) error {
	if expectedHash == "" {
		return nil
	}

	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return err
	}

	actualHash := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(actualHash, expectedHash) {
		return fmt.Errorf("sha256 mismatch: expected %s, got %s", expectedHash, actualHash)
	}

	log.Printf("[INFO] SHA256 verified successfully (%s)", actualHash)
	return nil
}

func (m *Manager) UpgradeCores(ctx context.Context, pkgs ...string) error {
	if len(pkgs) == 0 {
		pkgs = []string{"sing-box", "xray-core"}
	}

	var targetPkgs []string
	for _, p := range pkgs {
		st := m.checkPkgStatus(p)
		if st.Installed {
			targetPkgs = append(targetPkgs, p)
		}
	}

	if len(targetPkgs) == 0 {
		log.Println("[INFO] No active core packages installed to upgrade")
		return nil
	}

	if m.pkgManager == "apk" {
		log.Println("[INFO] Updating apk package repositories...")
		if out, err := exec.CommandContext(ctx, "apk", "update").CombinedOutput(); err != nil {
			return fmt.Errorf("apk update failed: %s", string(out))
		}
		args := append([]string{"add", "--upgrade"}, targetPkgs...)
		log.Printf("[INFO] Running apk %s...", strings.Join(args, " "))
		if out, err := exec.CommandContext(ctx, "apk", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("apk upgrade failed: %s", string(out))
		}
		return nil
	}

	log.Println("[INFO] Updating opkg repository indexes...")
	if out, err := exec.CommandContext(ctx, "opkg", "update").CombinedOutput(); err != nil {
		return fmt.Errorf("opkg update failed: %s", string(out))
	}
	args := append([]string{"upgrade"}, targetPkgs...)
	log.Printf("[INFO] Running opkg %s...", strings.Join(args, " "))
	if out, err := exec.CommandContext(ctx, "opkg", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("opkg upgrade failed: %s", string(out))
	}
	return nil
}

func (m *Manager) UpgradePackage(ctx context.Context) error {
	tag, pkgAsset, binAsset, checksumsURL, err := m.fetchLatestGitHubRelease(ctx)
	if err != nil {
		return err
	}

	const uciCfgPath = "/etc/config/cheburnet"
	backupCfgPath := filepath.Join(os.TempDir(), "cheburnet_uci_preserved.bak")

	savedConfig := false
	if cfgData, readErr := os.ReadFile(uciCfgPath); readErr == nil && len(cfgData) > 0 {
		if writeErr := os.WriteFile(backupCfgPath, cfgData, 0644); writeErr == nil {
			savedConfig = true
			defer os.Remove(backupCfgPath)
		}
	}

	restoreConfigIfNeeded := func() {
		if !savedConfig {
			return
		}
		backupData, readErr := os.ReadFile(backupCfgPath)
		if readErr != nil || len(backupData) == 0 {
			return
		}
		currData, currErr := os.ReadFile(uciCfgPath)
		if currErr != nil || len(currData) != len(backupData) {
			_ = os.WriteFile(uciCfgPath, backupData, 0644)
			log.Println("[INFO] User configuration /etc/config/cheburnet restored")
		}
	}

	if pkgAsset.URL != "" {
		log.Printf("[INFO] Downloading %s package: %s", m.pkgManager, pkgAsset.URL)
		tmpFile := filepath.Join(os.TempDir(), pkgAsset.Name)
		defer os.Remove(tmpFile)

		if err := m.downloadFile(ctx, pkgAsset.URL, tmpFile); err != nil {
			return fmt.Errorf("download package failed: %w", err)
		}

		expectedHash, _ := m.fetchExpectedSHA256(ctx, checksumsURL, pkgAsset.Name)
		if err := m.verifyFileSHA256(tmpFile, expectedHash); err != nil {
			return fmt.Errorf("security verification failed: %w", err)
		}

		var cmd *exec.Cmd
		if m.pkgManager == "apk" {
			cmd = exec.CommandContext(ctx, "apk", "add", "--allow-untrusted", tmpFile)
		} else {
			cmd = exec.CommandContext(ctx, "opkg", "install", "--force-reinstall", tmpFile)
		}

		out, err := cmd.CombinedOutput()
		restoreConfigIfNeeded()

		if err != nil {
			return fmt.Errorf("%s install failed: %s", m.pkgManager, string(out))
		}

		log.Printf("[INFO] %s package successfully updated", m.pkgManager)
		m.currentVer = tag
		return nil
	}

	if binAsset.URL != "" {
		log.Printf("[INFO] Package not found. Fallback to raw binary: %s", binAsset.URL)
		tmpBin := filepath.Join(os.TempDir(), binAsset.Name)
		defer os.Remove(tmpBin)

		if err := m.downloadFile(ctx, binAsset.URL, tmpBin); err != nil {
			return err
		}

		expectedHash, _ := m.fetchExpectedSHA256(ctx, checksumsURL, binAsset.Name)
		if err := m.verifyFileSHA256(tmpBin, expectedHash); err != nil {
			return fmt.Errorf("security verification failed: %w", err)
		}

		currPath, err := os.Executable()
		if err != nil {
			currPath = "/usr/bin/cheburnetd"
		}
		currPath, _ = filepath.EvalSymlinks(currPath)

		_ = os.Chmod(tmpBin, 0755)
		if err := os.Rename(tmpBin, currPath); err != nil {
			return fmt.Errorf("replace binary failed: %w", err)
		}

		restoreConfigIfNeeded()
		m.currentVer = tag
		return nil
	}

	return fmt.Errorf("no matching release asset found for arch %s", m.targetArch)
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

// StartAutoUpdateLoop запускает периодический фоновый опрос релизов и установку обновлений
func (m *Manager) StartAutoUpdateLoop(ctx context.Context, isAutoUpdateEnabled func() bool, onUpdateSuccess func()) {
	// Первая проверка через 5 минут после старта устройства (чтобы дать подняться сети и прокси)
	// Далее проверяем каждые 6 часов
	initialDelay := 5 * time.Minute
	checkInterval := 6 * time.Hour

	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(initialDelay):
		}

		ticker := time.NewTicker(checkInterval)
		defer ticker.Stop()

		checkAndUpgrade := func() {
			if !isAutoUpdateEnabled() {
				return
			}

			checkCtx, checkCancel := context.WithTimeout(ctx, 45*time.Second)
			report, err := m.CheckUpdates(checkCtx, true)
			checkCancel()

			if err != nil {
				log.Printf("[updater] Auto-check failed: %v", err)
				return
			}

			needUpgrade := report.CheburNet.HasUpdate || report.SingBox.HasUpdate || report.Xray.HasUpdate
			if !needUpgrade {
				return
			}

			log.Printf("[updater] Auto-update triggered! Components: CheburNet=%v, SingBox=%v, Xray=%v",
				report.CheburNet.HasUpdate, report.SingBox.HasUpdate, report.Xray.HasUpdate)

			upgCtx, upgCancel := context.WithTimeout(ctx, 5*time.Minute)
			defer upgCancel()

			if err := m.PerformUpgrade(upgCtx, "all"); err != nil {
				log.Printf("[updater] Auto-upgrade failed: %v", err)
				return
			}

			log.Println("[updater] Auto-upgrade completed successfully.")
			if onUpdateSuccess != nil {
				onUpdateSuccess()
			}
		}

		// Выполняем первую проверку
		checkAndUpgrade()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				checkAndUpgrade()
			}
		}
	}()
}

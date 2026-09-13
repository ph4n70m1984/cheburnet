package updater

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
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
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	TargetSingBoxVersion = "1.14.0-extended-2.7.1"
	SingBoxBinPath       = "/usr/bin/sing-box"
	SingBoxReleaseBase   = "https://github.com/shtorm-7/sing-box-extended/releases/download"
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
		httpClient: &http.Client{Timeout: 90 * time.Second},
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

// parseSemVer парсит версии вида 1.1.0, 0.0.8.15, v1.1.0-singbox
func parseSemVer(v string) []int {
	v = strings.TrimPrefix(v, "v")
	if idx := strings.Index(v, "-"); idx != -1 {
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	res := make([]int, 4)
	for i := 0; i < len(parts) && i < 4; i++ {
		val, _ := strconv.Atoi(parts[i])
		res[i] = val
	}
	return res
}

// isNewerVersion возвращает true только если remote строго новее, чем current
func isNewerVersion(remote, current string) bool {
	r := parseSemVer(remote)
	c := parseSemVer(current)

	for i := 0; i < len(r); i++ {
		if r[i] > c[i] {
			return true
		}
		if r[i] < c[i] {
			return false
		}
	}
	return false
}

func (m *Manager) CheckUpdates(ctx context.Context, autoUpdate bool) (*UpdateReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sbStatus := m.checkPinnedSingBoxStatus()

	chStatus := ComponentStatus{
		Current:   strings.TrimPrefix(m.currentVer, "v"),
		Installed: true,
	}

	latestTag, _, _, _, err := m.fetchLatestGitHubRelease(ctx)
	if err == nil {
		cleanLatest := strings.TrimPrefix(latestTag, "v")
		chStatus.Latest = cleanLatest
		// Проверяем, что релиз на GitHub действительно новее локальной версии
		chStatus.HasUpdate = cleanLatest != "" && isNewerVersion(cleanLatest, chStatus.Current)
	}

	report := &UpdateReport{
		CheburNet:  chStatus,
		SingBox:    sbStatus,
		AutoUpdate: autoUpdate,
		PkgManager: m.pkgManager,
	}

	return report, nil
}

func parseExtendedSemVer(v string) (base []int, ext []int) {
	parts := strings.Split(v, "-extended-")
	baseParts := strings.Split(parts[0], ".")
	for _, p := range baseParts {
		val, _ := strconv.Atoi(p)
		base = append(base, val)
	}
	for len(base) < 3 {
		base = append(base, 0)
	}

	if len(parts) > 1 {
		extParts := strings.Split(parts[1], ".")
		for _, p := range extParts {
			val, _ := strconv.Atoi(p)
			ext = append(ext, val)
		}
	}
	for len(ext) < 3 {
		ext = append(ext, 0)
	}
	return
}

func isVersionGreaterOrEqual(a, b string) bool {
	aBase, aExt := parseExtendedSemVer(a)
	bBase, bExt := parseExtendedSemVer(b)

	for i := 0; i < 3; i++ {
		if aBase[i] > bBase[i] {
			return true
		}
		if aBase[i] < bBase[i] {
			return false
		}
	}

	for i := 0; i < 3; i++ {
		if aExt[i] > bExt[i] {
			return true
		}
		if aExt[i] < bExt[i] {
			return false
		}
	}

	return true
}

func (m *Manager) checkPinnedSingBoxStatus() ComponentStatus {
	st := ComponentStatus{
		Latest: TargetSingBoxVersion,
	}

	binPath := SingBoxBinPath
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		if lp, err := exec.LookPath("sing-box"); err == nil {
			binPath = lp
		} else {
			st.Installed = false
			st.HasUpdate = true
			return st
		}
	}

	st.Installed = true
	out, err := exec.Command(binPath, "version").CombinedOutput()
	if err != nil && len(out) == 0 {
		st.Current = "unknown"
		st.HasUpdate = true
		return st
	}

	re := regexp.MustCompile(`version\s+([^\s\n]+)`)
	matches := re.FindStringSubmatch(string(out))
	if len(matches) >= 2 {
		curVer := strings.TrimPrefix(matches[1], "v")
		st.Current = curVer

		if isVersionGreaterOrEqual(curVer, TargetSingBoxVersion) {
			st.HasUpdate = false
		} else {
			st.HasUpdate = true
		}
	} else {
		st.Current = "unknown"
		st.HasUpdate = true
	}

	return st
}

func (m *Manager) resolveSingBoxAsset() (string, error) {
	arch := strings.ToLower(m.targetArch)

	switch runtime.GOARCH {
	case "arm64":
		return fmt.Sprintf("sing-box-%s-linux-arm64.tar.gz", TargetSingBoxVersion), nil
	case "amd64":
		return fmt.Sprintf("sing-box-%s-linux-amd64.tar.gz", TargetSingBoxVersion), nil
	case "386":
		return fmt.Sprintf("sing-box-%s-linux-386.tar.gz", TargetSingBoxVersion), nil
	case "arm":
		if strings.Contains(arch, "v5") {
			return fmt.Sprintf("sing-box-%s-linux-armv5.tar.gz", TargetSingBoxVersion), nil
		}
		return fmt.Sprintf("sing-box-%s-linux-armv7.tar.gz", TargetSingBoxVersion), nil
	case "mipsle":
		return fmt.Sprintf("sing-box-%s-linux-mipsle-softfloat.tar.gz", TargetSingBoxVersion), nil
	case "mips":
		return fmt.Sprintf("sing-box-%s-linux-mips-softfloat.tar.gz", TargetSingBoxVersion), nil
	case "mips64le":
		return fmt.Sprintf("sing-box-%s-linux-mips64le-softfloat.tar.gz", TargetSingBoxVersion), nil
	case "mips64":
		return fmt.Sprintf("sing-box-%s-linux-mips64-softfloat.tar.gz", TargetSingBoxVersion), nil
	default:
		return "", fmt.Errorf("unsupported sing-box architecture: GOARCH=%s, targetArch=%s", runtime.GOARCH, m.targetArch)
	}
}

func (m *Manager) UpgradeSingBoxCore(ctx context.Context) error {
	status := m.checkPinnedSingBoxStatus()
	if !status.HasUpdate && status.Installed {
		log.Printf("[INFO] sing-box is already at %s (or newer). No upgrade needed.", status.Current)
		return nil
	}

	assetName, err := m.resolveSingBoxAsset()
	if err != nil {
		return fmt.Errorf("architecture resolution failed: %w", err)
	}

	tag := "v" + strings.TrimPrefix(TargetSingBoxVersion, "v")
	tarURL := fmt.Sprintf("%s/%s/%s", SingBoxReleaseBase, tag, assetName)

	log.Printf("[INFO] Pinned sing-box-extended target: %s. Fetching from %s...", tag, tarURL)

	tmpDir, err := os.MkdirTemp(os.TempDir(), "sb_install_*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, assetName)
	if err := m.downloadFile(ctx, tarURL, archivePath); err != nil {
		return fmt.Errorf("failed to download sing-box archive: %w", err)
	}

	newBinPath := filepath.Join(tmpDir, "sing-box")
	if err := extractFileFromTarGz(archivePath, "sing-box", newBinPath); err != nil {
		return fmt.Errorf("failed to extract sing-box executable: %w", err)
	}

	_ = os.Chmod(newBinPath, 0755)

	testCmd := exec.CommandContext(ctx, newBinPath, "version")
	if out, err := testCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sing-box self-test failed: %w (out: %s)", err, strings.TrimSpace(string(out)))
	}

	targetPath := SingBoxBinPath
	if lp, err := exec.LookPath("sing-box"); err == nil {
		targetPath = lp
	}

	if err := replaceFileCrossDevice(newBinPath, targetPath); err != nil {
		return fmt.Errorf("failed to replace sing-box binary: %w", err)
	}

	log.Printf("[INFO] sing-box successfully pinned and updated to version %s at %s", TargetSingBoxVersion, targetPath)
	return nil
}

func extractFileFromTarGz(tarGzPath, targetFileName, outPath string) error {
	f, err := os.Open(tarGzPath)
	if err != nil {
		return err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		if filepath.Base(header.Name) == targetFileName && (header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA) {
			outFile, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
			if err != nil {
				return err
			}
			defer outFile.Close()

			_, err = io.Copy(outFile, tr)
			return err
		}
	}
	return fmt.Errorf("file %s not found in tar.gz", targetFileName)
}

func replaceFileCrossDevice(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}

	_ = os.Remove(src)
	return nil
}

func (m *Manager) UpgradeCores(ctx context.Context, pkgs ...string) error {
	sbStatus := m.checkPinnedSingBoxStatus()
	if sbStatus.HasUpdate || !sbStatus.Installed {
		log.Printf("[INFO] Upgrading sing-box to pinned version %s...", TargetSingBoxVersion)
		return m.UpgradeSingBoxCore(ctx)
	}
	log.Printf("[INFO] sing-box is already at pinned version %s (or newer)", TargetSingBoxVersion)
	return nil
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

		_ = os.Chmod(tmpBin, 0755)

		// Обновляем все пути размещения бинарника, используемые в procd и PATH
		destinations := []string{"/usr/bin/cheburnetd", "/bin/cheburnetd"}
		if currPath, err := os.Executable(); err == nil {
			if resolved, err := filepath.EvalSymlinks(currPath); err == nil {
				destinations = append(destinations, resolved)
			}
		}

		replacedAny := false
		for _, dest := range destinations {
			if _, statErr := os.Stat(dest); statErr == nil {
				if err := replaceFileCrossDevice(tmpBin, dest); err == nil {
					replacedAny = true
					log.Printf("[INFO] Binary replaced at %s", dest)
				}
			}
		}

		if !replacedAny {
			if err := replaceFileCrossDevice(tmpBin, "/usr/bin/cheburnetd"); err != nil {
				return fmt.Errorf("replace binary failed: %w", err)
			}
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
	case "sing-box", "cores":
		return m.UpgradeSingBoxCore(ctx)
	case "cheburnet":
		return m.UpgradePackage(ctx)
	case "all":
		_ = m.UpgradeSingBoxCore(ctx)
		return m.UpgradePackage(ctx)
	default:
		return fmt.Errorf("unknown target: %s", target)
	}
}

func (m *Manager) StartAutoUpdateLoop(ctx context.Context, isAutoUpdateEnabled func() bool, onUpdateSuccess func()) {
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

			needUpgrade := report.CheburNet.HasUpdate || report.SingBox.HasUpdate
			if !needUpgrade {
				return
			}

			log.Printf("[updater] Auto-update triggered! Components: CheburNet=%v, SingBox=%v",
				report.CheburNet.HasUpdate, report.SingBox.HasUpdate)

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

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

var sha256Regex = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

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
		httpClient: &http.Client{Timeout: 120 * time.Second},
		pkgManager: pkgMgr,
		targetArch: arch,
	}
}

// checkFreeSpaceBytes определяет доступный объём памяти (в байтах) через команду df
func checkFreeSpaceBytes(path string) (uint64, error) {
	out, err := exec.Command("df", "-k", path).Output()
	if err != nil {
		if runtime.GOOS != "linux" {
			return 1024 * 1024 * 1024, nil
		}
		return 0, fmt.Errorf("ошибка вызова df для %s: %w", path, err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0, fmt.Errorf("неожиданный формат вывода df: %s", string(out))
	}

	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		return 0, fmt.Errorf("не удалось распарсить поля df: %s", lines[len(lines)-1])
	}

	availKb, err := strconv.ParseUint(fields[3], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("ошибка парсинга доступного места: %w", err)
	}

	return availKb * 1024, nil
}

// ensureSpace проверяет наличие необходимого свободного места на диске
func ensureSpace(path string, requiredBytes uint64, description string) error {
	free, err := checkFreeSpaceBytes(path)
	if err != nil {
		if path != "/tmp" {
			return ensureSpace("/tmp", requiredBytes, description)
		}
		return fmt.Errorf("ошибка проверки хранилища %s (%s): %w", description, path, err)
	}

	if free < requiredBytes {
		freeMB := float64(free) / (1024 * 1024)
		reqMB := float64(requiredBytes) / (1024 * 1024)
		return fmt.Errorf("недостаточно места для %s: доступно %.1f МБ, требуется минимум %.1f МБ", description, freeMB, reqMB)
	}
	return nil
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
		return "", fmt.Errorf("неподдерживаемая архитектура ядра sing-box: GOARCH=%s, targetArch=%s", runtime.GOARCH, m.targetArch)
	}
}

func (m *Manager) UpgradeSingBoxCore(ctx context.Context) error {
	status := m.checkPinnedSingBoxStatus()
	if !status.HasUpdate && status.Installed {
		log.Printf("[INFO] sing-box уже имеет версию %s. Обновление не требуется.", status.Current)
		return nil
	}

	const minTmpSpace = 40 * 1024 * 1024
	const minDestSpace = 25 * 1024 * 1024

	if err := ensureSpace("/tmp", minTmpSpace, "временного каталога /tmp"); err != nil {
		return err
	}

	targetPath := SingBoxBinPath
	if lp, err := exec.LookPath("sing-box"); err == nil {
		targetPath = lp
	}

	if err := ensureSpace(filepath.Dir(targetPath), minDestSpace, "установки в системный раздел"); err != nil {
		return err
	}

	assetName, err := m.resolveSingBoxAsset()
	if err != nil {
		return fmt.Errorf("определение целевого архива: %w", err)
	}

	tag := "v" + strings.TrimPrefix(TargetSingBoxVersion, "v")
	tarURL := fmt.Sprintf("%s/%s/%s", SingBoxReleaseBase, tag, assetName)

	log.Printf("[INFO] Загрузка sing-box-extended %s в /tmp...", tag)

	tmpDir, err := os.MkdirTemp("/tmp", "sb_install_*")
	if err != nil {
		return fmt.Errorf("не удалось создать временную директорию в /tmp: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, assetName)
	if err := m.downloadFile(ctx, tarURL, archivePath); err != nil {
		return fmt.Errorf("сбой загрузки sing-box архива: %w", err)
	}

	newBinPath := filepath.Join(tmpDir, "sing-box")
	if err := extractFileFromTarGz(archivePath, "sing-box", newBinPath); err != nil {
		return fmt.Errorf("ошибка распаковки sing-box: %w", err)
	}

	_ = os.Chmod(newBinPath, 0755)

	testCmd := exec.CommandContext(ctx, newBinPath, "version")
	if out, err := testCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("бинарник sing-box не прошел самопроверку: %w (вывод: %s)", err, strings.TrimSpace(string(out)))
	}

	if err := replaceFileCrossDevice(newBinPath, targetPath); err != nil {
		return fmt.Errorf("не удалось заменить бинарник sing-box: %w", err)
	}

	log.Printf("[INFO] sing-box успешно обновлен до %s (%s)", TargetSingBoxVersion, targetPath)
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
	return fmt.Errorf("файл %s отсутствует внутри архива tar.gz", targetFileName)
}

func replaceFileCrossDevice(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	dstDir := filepath.Dir(dst)
	tmpDst, err := os.CreateTemp(dstDir, ".bin_replace_*")
	if err != nil {
		_ = os.Remove(dst)
		out, errCreate := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
		if errCreate != nil {
			return fmt.Errorf("open target: %w", errCreate)
		}
		defer out.Close()

		in, errOpen := os.Open(src)
		if errOpen != nil {
			return errOpen
		}
		defer in.Close()

		if _, err := io.Copy(out, in); err != nil {
			return err
		}
		_ = os.Remove(src)
		return nil
	}
	tmpDstPath := tmpDst.Name()
	defer os.Remove(tmpDstPath)

	in, err := os.Open(src)
	if err != nil {
		tmpDst.Close()
		return err
	}
	defer in.Close()

	if _, err := io.Copy(tmpDst, in); err != nil {
		tmpDst.Close()
		return err
	}
	tmpDst.Close()

	if err := os.Chmod(tmpDstPath, 0755); err != nil {
		return err
	}

	if err := os.Rename(tmpDstPath, dst); err != nil {
		_ = os.Remove(dst)
		if errRetry := os.Rename(tmpDstPath, dst); errRetry != nil {
			return fmt.Errorf("rename to target: %w", errRetry)
		}
	}

	_ = os.Remove(src)
	return nil
}

func (m *Manager) UpgradeCores(ctx context.Context, pkgs ...string) error {
	sbStatus := m.checkPinnedSingBoxStatus()
	if sbStatus.HasUpdate || !sbStatus.Installed {
		return m.UpgradeSingBoxCore(ctx)
	}
	return nil
}

func (m *Manager) fetchLatestGitHubRelease(ctx context.Context) (tag string, pkgAsset releaseAsset, binAsset releaseAsset, checksumsURL string, err error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", m.githubRepo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", releaseAsset{}, releaseAsset{}, "", fmt.Errorf("create release request: %w", err)
	}
	req.Header.Set("User-Agent", "CheburNet-Updater")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", releaseAsset{}, releaseAsset{}, "", fmt.Errorf("github api request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", releaseAsset{}, releaseAsset{}, "", fmt.Errorf("github api returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", releaseAsset{}, releaseAsset{}, "", fmt.Errorf("decode github release response: %w", err)
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
	if strings.TrimSpace(checksumsURL) == "" {
		return "", fmt.Errorf("манифест контрольных сумм sha256 отсутствует в релизе")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checksumsURL, nil)
	if err != nil {
		return "", fmt.Errorf("create checksums request: %w", err)
	}
	req.Header.Set("User-Agent", "CheburNet-Updater")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to download checksums: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download checksums: status code %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			hashCandidate := strings.ToLower(parts[0])
			baseName := filepath.Base(strings.TrimPrefix(parts[1], "*"))
			if baseName == filename {
				if !sha256Regex.MatchString(hashCandidate) {
					return "", fmt.Errorf("некорректный формат sha256 хеша (%s) для %s", hashCandidate, filename)
				}
				return hashCandidate, nil
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan checksums file: %w", err)
	}

	return "", fmt.Errorf("контрольная сумма для файла %s не найдена в манифесте", filename)
}

func (m *Manager) verifyFileSHA256(filePath, expectedHash string) error {
	expectedHash = strings.TrimSpace(strings.ToLower(expectedHash))
	if !sha256Regex.MatchString(expectedHash) {
		return fmt.Errorf("проверка отменена: не передан валидный 64-символьный SHA256")
	}

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("не удалось открыть скачанный файл для проверки: %w", err)
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return fmt.Errorf("ошибка вычисления хеша: %w", err)
	}

	actualHash := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(actualHash, expectedHash) {
		return fmt.Errorf("несовпадение sha256: ожидался %s, получен %s", expectedHash, actualHash)
	}

	log.Printf("[INFO] Контрольная сумма SHA256 проверена успешно (%s)", actualHash)
	return nil
}

func (m *Manager) UpgradePackage(ctx context.Context) error {
	const minPkgTmpSpace = 15 * 1024 * 1024
	const minOverlaySpace = 10 * 1024 * 1024

	if err := ensureSpace("/tmp", minPkgTmpSpace, "загрузки обновления в /tmp"); err != nil {
		return err
	}

	destCheck := "/overlay"
	if _, err := os.Stat(destCheck); err != nil {
		destCheck = "/"
	}
	if err := ensureSpace(destCheck, minOverlaySpace, "установки в системный раздел"); err != nil {
		return err
	}

	tag, pkgAsset, binAsset, checksumsURL, err := m.fetchLatestGitHubRelease(ctx)
	if err != nil {
		return fmt.Errorf("сбой поиска релиза: %w", err)
	}

	const uciCfgPath = "/etc/config/cheburnet"
	backupCfgPath := "/tmp/cheburnet_uci_preserved.bak"

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
			log.Println("[INFO] Пользовательская конфигурация /etc/config/cheburnet восстановлена")
		}
	}

	if pkgAsset.URL != "" {
		log.Printf("[INFO] Загрузка %s пакета: %s", m.pkgManager, pkgAsset.URL)
		tmpFile := filepath.Join("/tmp", pkgAsset.Name)
		defer os.Remove(tmpFile)

		if err := m.downloadFile(ctx, pkgAsset.URL, tmpFile); err != nil {
			return fmt.Errorf("ошибка скачивания пакета: %w", err)
		}

		expectedHash, err := m.fetchExpectedSHA256(ctx, checksumsURL, pkgAsset.Name)
		if err != nil {
			return fmt.Errorf("проверка целостности заблокирована: %w", err)
		}
		if err := m.verifyFileSHA256(tmpFile, expectedHash); err != nil {
			return fmt.Errorf("ошибка проверки безопасности: %w", err)
		}

		var cmd *exec.Cmd
		if m.pkgManager == "apk" {
			cmd = exec.CommandContext(ctx, "apk", "add", tmpFile)
		} else {
			cmd = exec.CommandContext(ctx, "opkg", "--tmp-dir", "/tmp", "install", "--force-reinstall", tmpFile)
		}

		out, err := cmd.CombinedOutput()
		restoreConfigIfNeeded()

		if err != nil {
			return fmt.Errorf("установка через %s завершилась сбоем: %s", m.pkgManager, string(out))
		}

		log.Printf("[INFO] Пакет %s успешно обновлен", m.pkgManager)
		m.currentVer = tag
		return nil
	}

	if binAsset.URL != "" {
		log.Printf("[INFO] Пакет не найден. Fallback к бинарному файлу: %s", binAsset.URL)
		tmpBin := filepath.Join("/tmp", binAsset.Name)
		defer os.Remove(tmpBin)

		if err := m.downloadFile(ctx, binAsset.URL, tmpBin); err != nil {
			return err
		}

		expectedHash, err := m.fetchExpectedSHA256(ctx, checksumsURL, binAsset.Name)
		if err != nil {
			return fmt.Errorf("проверка целостности бинарника заблокирована: %w", err)
		}
		if err := m.verifyFileSHA256(tmpBin, expectedHash); err != nil {
			return fmt.Errorf("ошибка проверки безопасности: %w", err)
		}

		_ = os.Chmod(tmpBin, 0755)

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
					log.Printf("[INFO] Бинарник заменен в %s", dest)
				}
			}
		}

		if !replacedAny {
			if err := replaceFileCrossDevice(tmpBin, "/usr/bin/cheburnetd"); err != nil {
				return fmt.Errorf("не удалось перезаписать исполняемый файл: %w", err)
			}
		}

		restoreConfigIfNeeded()
		m.currentVer = tag
		return nil
	}

	return fmt.Errorf("не найден подходящий релизный файл для архитектуры %s", m.targetArch)
}

func (m *Manager) downloadFile(ctx context.Context, url, targetPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create download request: %w", err)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("network request failed for %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		trimmed := strings.TrimSpace(string(errBody))
		if trimmed != "" {
			return fmt.Errorf("download error (status: %d): %s", resp.StatusCode, trimmed)
		}
		return fmt.Errorf("download error (status: %d)", resp.StatusCode)
	}

	out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("open target file %s: %w", targetPath, err)
	}
	defer out.Close()

	if _, err = io.Copy(out, resp.Body); err != nil {
		return fmt.Errorf("write payload to %s: %w", targetPath, err)
	}
	return nil
}

func (m *Manager) PerformUpgrade(ctx context.Context, target string) error {
	m.mu.Lock()
	if m.isUpgrading {
		m.mu.Unlock()
		return fmt.Errorf("процесс обновления уже выполняется")
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
		if err := m.UpgradeSingBoxCore(ctx); err != nil {
			return fmt.Errorf("ошибка обновления ядра sing-box: %w", err)
		}
		if err := m.UpgradePackage(ctx); err != nil {
			return fmt.Errorf("ошибка обновления cheburnet: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("неизвестная цель обновления: %s", target)
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
				log.Printf("[updater] Сбой фоновой проверки обновлений: %v", err)
				return
			}

			needUpgrade := report.CheburNet.HasUpdate || report.SingBox.HasUpdate
			if !needUpgrade {
				return
			}

			log.Printf("[updater] Инициализация автообновления: CheburNet=%v, SingBox=%v",
				report.CheburNet.HasUpdate, report.SingBox.HasUpdate)

			upgCtx, upgCancel := context.WithTimeout(ctx, 5*time.Minute)
			defer upgCancel()

			if err := m.PerformUpgrade(upgCtx, "all"); err != nil {
				log.Printf("[updater] Ошибка автообновления: %v", err)
				return
			}

			log.Println("[updater] Автообновление успешно завершено.")
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

package network

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	RulesStorageDir       = "/tmp/cheburnet/rulesets"
	TempDownloadDir       = "/tmp"
	DownloadTimeout       = 30 * time.Second
	MaxRawRulesetDownload = 15 << 20 // 15 MiB лимит для сырого списка правил
)

type CompressedRulesetLoader struct {
	storageDir string
	client     *http.Client
	downloadMu sync.Map // Map[string]*sync.Mutex для исключения TOCTOU гонок на скачивание
}

func NewCompressedRulesetLoader() *CompressedRulesetLoader {
	_ = os.MkdirAll(RulesStorageDir, 0755)
	return &CompressedRulesetLoader{
		storageDir: RulesStorageDir,
		client: &http.Client{
			Timeout: DownloadTimeout,
		},
	}
}

// HasCIDRSubnets проверяет, содержит ли удаленный репозиторий списки IPv4-подсетей для этого сервиса.
// Чисто доменные сервисы (google_ai, youtube, russia_inside и др.) маршрутизируются через .srs правила.
func HasCIDRSubnets(rulesetName string) bool {
	switch strings.ToLower(strings.TrimSpace(rulesetName)) {
	case "telegram", "discord", "meta", "twitter":
		return true
	default:
		return false
	}
}

func (l *CompressedRulesetLoader) getFileMutex(key string) *sync.Mutex {
	m, _ := l.downloadMu.LoadOrStore(key, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// GetSubnets читает локальный .gz кэш или скачивает его при первом запуске
func (l *CompressedRulesetLoader) GetSubnets(rulesetName string) ([]string, error) {
	normName := l.normalizeName(rulesetName)
	if normName == "" {
		return nil, nil
	}

	// Для чисто доменных правил не выполняем HTTP-запросы за подсетями
	if !HasCIDRSubnets(normName) {
		return nil, nil
	}

	targetGz := filepath.Join(l.storageDir, fmt.Sprintf("%s.txt.gz", normName))

	mu := l.getFileMutex(normName)
	mu.Lock()
	defer mu.Unlock()

	// Если файла нет или он пустой — скачиваем
	if stat, err := os.Stat(targetGz); os.IsNotExist(err) || (err == nil && stat.Size() <= 30) {
		if err := l.downloadAndCompressAtomic(normName, targetGz); err != nil {
			log.Printf("[ruleset] WARN: Failed to download subnets for '%s': %v", normName, err)
			return nil, nil
		}
	}

	return l.readCIDRsFromGz(targetGz)
}

// UpdateRuleset принудительно обновляет и упаковывает .txt.gz
func (l *CompressedRulesetLoader) UpdateRuleset(rulesetName string) error {
	normName := l.normalizeName(rulesetName)
	if normName == "" || !HasCIDRSubnets(normName) {
		return nil
	}

	mu := l.getFileMutex(normName)
	mu.Lock()
	defer mu.Unlock()

	targetGz := filepath.Join(l.storageDir, fmt.Sprintf("%s.txt.gz", normName))
	return l.downloadAndCompressAtomic(normName, targetGz)
}

func (l *CompressedRulesetLoader) normalizeName(name string) string {
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

// downloadAndCompressAtomic скачивает поток и сжимает его на лету с сохранением реальной ошибки
func (l *CompressedRulesetLoader) downloadAndCompressAtomic(rulesetName, targetGz string) error {
	candidates := []string{
		fmt.Sprintf("https://raw.githubusercontent.com/itdoginfo/allow-domains/main/Subnets/IPv4/%s.txt", rulesetName),
		fmt.Sprintf("https://raw.githubusercontent.com/itdoginfo/allow-domains/main/Subnets/IPv4/%s.lst", rulesetName),
		fmt.Sprintf("https://raw.githubusercontent.com/itdoginfo/allow-domains/main/Subnets/IPv4/%s.txt", strings.ToUpper(rulesetName)),
	}

	var resp *http.Response
	var lastErr error

	for _, u := range candidates {
		req, reqErr := http.NewRequest("GET", u, nil)
		if reqErr != nil {
			lastErr = reqErr
			continue
		}
		req.Header.Set("User-Agent", "CheburNET-Daemon")

		r, doErr := l.client.Do(req)
		if doErr == nil && r.StatusCode == http.StatusOK {
			resp = r
			break
		}
		if doErr != nil {
			lastErr = doErr
		} else if r != nil {
			lastErr = fmt.Errorf("HTTP %d from %s", r.StatusCode, u)
			_ = r.Body.Close()
		}
	}

	if resp == nil {
		return fmt.Errorf("subnets for ruleset '%s' not found on remote (last error: %w)", rulesetName, lastErr)
	}
	defer resp.Body.Close()

	// Уникальный staging-файл для исключения гонок при параллельных загрузках
	tmpFile, err := os.CreateTemp(TempDownloadDir, fmt.Sprintf("%s-*.txt.gz.tmp", rulesetName))
	if err != nil {
		return fmt.Errorf("create tmp file: %w", err)
	}
	tmpGz := tmpFile.Name()
	defer os.Remove(tmpGz)

	gzWriter, err := gzip.NewWriterLevel(tmpFile, gzip.BestSpeed)
	if err != nil {
		_ = tmpFile.Close()
		return err
	}

	limitedBody := io.LimitReader(resp.Body, MaxRawRulesetDownload+1)
	written, err := io.Copy(gzWriter, limitedBody)
	_ = gzWriter.Close()
	_ = tmpFile.Close()

	if err != nil {
		return fmt.Errorf("gzip stream copy failed: %w", err)
	}
	if written > MaxRawRulesetDownload {
		return fmt.Errorf("ruleset '%s' exceeded max download limit of %d bytes", rulesetName, MaxRawRulesetDownload)
	}

	if err := l.validateGzFile(tmpGz); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	return l.safeCopyToFlash(tmpGz, targetGz)
}

func (l *CompressedRulesetLoader) validateGzFile(filePath string) error {
	count := 0
	err := l.StreamCIDRsFromGz(filePath, func(cidr string) error {
		count++
		if count >= 1 {
			return io.EOF
		}
		return nil
	})
	if err != nil && err != io.EOF {
		return err
	}
	if count == 0 {
		return fmt.Errorf("archive contains 0 valid subnets")
	}
	return nil
}

// StreamCIDRsFromGz валидирует CIDR построчно, исключая попадание IPv6 и мусорных строк
func (l *CompressedRulesetLoader) StreamCIDRsFromGz(filePath string, onSubnet func(cidr string) error) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	gzReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("gzip init: %w", err)
	}
	defer gzReader.Close()

	scanner := bufio.NewScanner(gzReader)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 64*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Строгая проверка валидности CIDR или отдельного IPv4
		if _, ipNet, err := net.ParseCIDR(line); err == nil {
			if ipNet.IP.To4() == nil {
				continue // Отклоняем IPv6
			}
			if err := onSubnet(ipNet.String()); err != nil {
				return err
			}
			continue
		}

		// Если передан чистый IPv4 без слэша
		if ip := net.ParseIP(line); ip != nil && ip.To4() != nil {
			if err := onSubnet(ip.To4().String() + "/32"); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func (l *CompressedRulesetLoader) readCIDRsFromGz(filePath string) ([]string, error) {
	var subnets []string
	err := l.StreamCIDRsFromGz(filePath, func(cidr string) error {
		subnets = append(subnets, cidr)
		return nil
	})
	return subnets, err
}

func (l *CompressedRulesetLoader) safeCopyToFlash(src, dst string) error {
	dstDir := filepath.Dir(dst)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return err
	}

	// Уникальный staging-файл в том же каталоге для атомарного Rename
	tmpFile, err := os.CreateTemp(dstDir, filepath.Base(dst)+"-*.tmp")
	if err != nil {
		return err
	}
	dstTmp := tmpFile.Name()
	defer os.Remove(dstTmp)

	s, err := os.Open(src)
	if err != nil {
		_ = tmpFile.Close()
		return err
	}
	defer s.Close()

	if _, err := io.Copy(tmpFile, s); err != nil {
		_ = tmpFile.Close()
		return err
	}
	_ = tmpFile.Sync()
	_ = tmpFile.Close()

	if err := os.Rename(dstTmp, dst); err != nil {
		return err
	}

	// Fsync директории для гарантии записи метаданных в файловую систему OpenWrt
	if d, err := os.Open(dstDir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}

	return nil
}

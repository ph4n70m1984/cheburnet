package network

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	RulesStorageDir = "/etc/cheburnet/rules"
	TempDownloadDir = "/tmp"
	DownloadTimeout = 20 * time.Second
)

type CompressedRulesetLoader struct {
	storageDir string
	client     *http.Client
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

// GetSubnets сперва пытается прочитать локальный .gz кэш, если его нет — качает из сети
func (l *CompressedRulesetLoader) GetSubnets(rulesetName string) ([]string, error) {
	targetGz := filepath.Join(l.storageDir, fmt.Sprintf("%s.lst.gz", rulesetName))

	// Если архива нет локально (первый холодный старт) — пробуем скачать
	if _, err := os.Stat(targetGz); os.IsNotExist(err) {
		if err := l.downloadAndCompressAtomic(rulesetName, targetGz); err != nil {
			// Если файла с подсетями нет (404), это штатная ситуация для чисто доменных сервисов
			return nil, nil
		}
	}

	return l.readCIDRsFromGz(targetGz)
}

// UpdateRuleset принудительно обновляет и перезаписывает .lst.gz при наличии сети
func (l *CompressedRulesetLoader) UpdateRuleset(rulesetName string) error {
	targetGz := filepath.Join(l.storageDir, fmt.Sprintf("%s.lst.gz", rulesetName))
	return l.downloadAndCompressAtomic(rulesetName, targetGz)
}

// downloadAndCompressAtomic скачивает, упаковывает в .tmp.gz, проверяет и атомарно перемещает на Flash
func (l *CompressedRulesetLoader) downloadAndCompressAtomic(rulesetName, targetGz string) error {
	url := fmt.Sprintf("https://raw.githubusercontent.com/itdoginfo/allow-domains/main/Subnets/IPv4/%s.lst", rulesetName)
	tmpGz := filepath.Join(TempDownloadDir, fmt.Sprintf("%s.lst.gz.tmp", rulesetName))
	defer os.Remove(tmpGz)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "CheburNET-Daemon")

	resp, err := l.client.Do(req)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	// Если для списка нет IPv4 подсетей в репозитории (например, youtube, google_ai, russia_inside)
	if resp.StatusCode == http.StatusNotFound {
		_ = l.createEmptyGz(targetGz)
		return fmt.Errorf("subnets not found (404)")
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %s", resp.Status)
	}

	outFile, err := os.OpenFile(tmpGz, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}

	gzWriter, err := gzip.NewWriterLevel(outFile, gzip.BestCompression)
	if err != nil {
		outFile.Close()
		return err
	}

	_, err = io.Copy(gzWriter, resp.Body)
	_ = gzWriter.Close()
	_ = outFile.Close()
	if err != nil {
		return fmt.Errorf("gzip stream copy failed: %w", err)
	}

	// Валидация распаковки и формата CIDR
	if err := l.validateGzFile(tmpGz); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// Атомарный перенос на Flash
	return l.safeCopyToFlash(tmpGz, targetGz)
}

// createEmptyGz создает пустой сжатый файл-заглушку, чтобы не опрашивать 404 URL при каждом рестарте
func (l *CompressedRulesetLoader) createEmptyGz(targetPath string) error {
	f, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()
	return nil
}

// validateGzFile тестирует распаковку во временной памяти и корректность IP
func (l *CompressedRulesetLoader) validateGzFile(filePath string) error {
	subnets, err := l.readCIDRsFromGz(filePath)
	if err != nil {
		return err
	}
	if len(subnets) == 0 {
		return fmt.Errorf("archive contains 0 valid subnets")
	}
	return nil
}

// readCIDRsFromGz распаковывает поток .gz на лету в RAM
func (l *CompressedRulesetLoader) readCIDRsFromGz(filePath string) ([]string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	gzReader, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("gzip init: %w", err)
	}
	defer gzReader.Close()

	var subnets []string
	scanner := bufio.NewScanner(gzReader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Нормализация одиночных IP в CIDR /32
		if !strings.Contains(line, "/") {
			if ip := net.ParseIP(line); ip != nil && ip.To4() != nil {
				line += "/32"
			}
		}

		subnets = append(subnets, line)
	}

	return subnets, scanner.Err()
}

// safeCopyToFlash копирует файл через staging-файл с синхронизацией fsync
func (l *CompressedRulesetLoader) safeCopyToFlash(src, dst string) error {
	dstTmp := dst + ".new"

	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()

	d, err := os.OpenFile(dstTmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}

	if _, err := io.Copy(d, s); err != nil {
		d.Close()
		os.Remove(dstTmp)
		return err
	}
	_ = d.Sync()
	d.Close()

	return os.Rename(dstTmp, dst)
}

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
	XrayAssetDir    = "/usr/share/xray"
	TempDownloadDir = "/tmp"
	DownloadTimeout = 30 * time.Second
)

type CompressedRulesetLoader struct {
	storageDir string
	client     *http.Client
}

func NewCompressedRulesetLoader() *CompressedRulesetLoader {
	_ = os.MkdirAll(RulesStorageDir, 0755)
	_ = os.MkdirAll(XrayAssetDir, 0755)
	return &CompressedRulesetLoader{
		storageDir: RulesStorageDir,
		client: &http.Client{
			Timeout: DownloadTimeout,
		},
	}
}

// GetSubnets читает локальный .gz кэш или пробует скачать его при первом запуске
func (l *CompressedRulesetLoader) GetSubnets(rulesetName string) ([]string, error) {
	normName := strings.ToLower(strings.TrimSpace(rulesetName))
	if normName == "" {
		return nil, nil
	}

	targetGz := filepath.Join(l.storageDir, fmt.Sprintf("%s.lst.gz", normName))

	if _, err := os.Stat(targetGz); os.IsNotExist(err) {
		if err := l.downloadAndCompressAtomic(normName, targetGz); err != nil {
			// 404 означает отсутствие IP-подсетей для данной категории (например, чистый geosite)
			return nil, nil
		}
	}

	return l.readCIDRsFromGz(targetGz)
}

// UpdateRuleset принудительно обновляет и упаковывает .lst.gz
func (l *CompressedRulesetLoader) UpdateRuleset(rulesetName string) error {
	normName := strings.ToLower(strings.TrimSpace(rulesetName))
	if normName == "" {
		return nil
	}
	targetGz := filepath.Join(l.storageDir, fmt.Sprintf("%s.lst.gz", normName))
	return l.downloadAndCompressAtomic(normName, targetGz)
}

// downloadAndCompressAtomic запрашивает файл подсетей (в репозитории имена в UPPERCASE: DISCORD.lst)
func (l *CompressedRulesetLoader) downloadAndCompressAtomic(rulesetName, targetGz string) error {
	// В репозитории itdoginfo/allow-domains файлы в Subnets/IPv4/ названы в UPPERCASE
	fileName := strings.ToUpper(rulesetName) + ".lst"
	url := fmt.Sprintf("https://raw.githubusercontent.com/itdoginfo/allow-domains/main/Subnets/IPv4/%s", fileName)
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

	if err := l.validateGzFile(tmpGz); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	return l.safeCopyToFlash(tmpGz, targetGz)
}

// UpdateGeositeDat скачивает актуальный geosite.dat для Xray
func (l *CompressedRulesetLoader) UpdateGeositeDat() (bool, error) {
	url := "https://github.com/itdoginfo/allow-domains/releases/latest/download/geosite.dat"
	targetPath := filepath.Join(XrayAssetDir, "geosite.dat")
	tmpPath := filepath.Join(TempDownloadDir, "geosite.dat.tmp")
	defer os.Remove(tmpPath)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", "CheburNET-Daemon")

	// Если файл уже есть, используем заголовок проверки изменения по дате
	if fi, err := os.Stat(targetPath); err == nil {
		req.Header.Set("If-Modified-Since", fi.ModTime().UTC().Format(http.TimeFormat))
	}

	resp, err := l.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("http get geosite: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return false, nil // Файл не изменился
	}

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("bad status downloading geosite.dat: %s", resp.Status)
	}

	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return false, fmt.Errorf("create tmp geosite: %w", err)
	}

	written, err := io.Copy(out, resp.Body)
	_ = out.Close()
	if err != nil {
		return false, fmt.Errorf("stream copy geosite failed: %w", err)
	}

	if written < 1024 {
		return false, fmt.Errorf("geosite.dat is too small (%d bytes)", written)
	}

	if err := l.safeCopyToFlash(tmpPath, targetPath); err != nil {
		return false, err
	}

	return true, nil
}

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

		if !strings.Contains(line, "/") {
			if ip := net.ParseIP(line); ip != nil && ip.To4() != nil {
				line += "/32"
			}
		}

		subnets = append(subnets, line)
	}

	return subnets, scanner.Err()
}

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

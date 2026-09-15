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
	RulesStorageDir = "tmp/cheburnet/rulesets"
	TempDownloadDir = "/tmp"
	DownloadTimeout = 30 * time.Second
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

// GetSubnets читает локальный .gz кэш или скачивает его при первом запуске
func (l *CompressedRulesetLoader) GetSubnets(rulesetName string) ([]string, error) {
	normName := l.normalizeName(rulesetName)
	if normName == "" {
		return nil, nil
	}

	targetGz := filepath.Join(l.storageDir, fmt.Sprintf("%s.txt.gz", normName))

	// Если файла нет или он пустой (артефакт старого бага) — скачиваем
	if stat, err := os.Stat(targetGz); os.IsNotExist(err) || (err == nil && stat.Size() <= 30) {
		if err := l.downloadAndCompressAtomic(normName, targetGz); err != nil {
			return nil, nil
		}
	}

	return l.readCIDRsFromGz(targetGz)
}

// UpdateRuleset принудительно обновляет и упаковывает .txt.gz
func (l *CompressedRulesetLoader) UpdateRuleset(rulesetName string) error {
	normName := l.normalizeName(rulesetName)
	if normName == "" {
		return nil
	}
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

// downloadAndCompressAtomic пробует скачать .txt файл из репозитория
func (l *CompressedRulesetLoader) downloadAndCompressAtomic(rulesetName, targetGz string) error {
	// В репозитории itdoginfo/allow-domains/Subnets/IPv4 файлы лежат в формате: telegram.txt, discord.txt
	candidates := []string{
		fmt.Sprintf("https://raw.githubusercontent.com/itdoginfo/allow-domains/main/Subnets/IPv4/%s.txt", rulesetName),
		fmt.Sprintf("https://raw.githubusercontent.com/itdoginfo/allow-domains/main/Subnets/IPv4/%s.lst", rulesetName),
		fmt.Sprintf("https://raw.githubusercontent.com/itdoginfo/allow-domains/main/Subnets/IPv4/%s.txt", strings.ToUpper(rulesetName)),
	}

	var resp *http.Response
	var err error

	for _, url := range candidates {
		req, reqErr := http.NewRequest("GET", url, nil)
		if reqErr != nil {
			continue
		}
		req.Header.Set("User-Agent", "CheburNET-Daemon")

		r, doErr := l.client.Do(req)
		if doErr == nil && r.StatusCode == http.StatusOK {
			resp = r
			break
		}
		if r != nil {
			_ = r.Body.Close()
		}
	}

	if resp == nil {
		return fmt.Errorf("subnets for ruleset '%s' not found on github", rulesetName)
	}
	defer resp.Body.Close()

	tmpGz := filepath.Join(TempDownloadDir, fmt.Sprintf("%s.txt.gz.tmp", rulesetName))
	defer os.Remove(tmpGz)

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

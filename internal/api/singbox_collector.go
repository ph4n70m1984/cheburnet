//go:build metrics

package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const userHZ = 100.0

type SingboxCollector struct {
	clashAPI string
	secret   string
	client   *http.Client

	upDesc          *prometheus.Desc
	cpuSecondsDesc  *prometheus.Desc
	memRssDesc      *prometheus.Desc
	memVmSizeDesc   *prometheus.Desc
	memInUseDesc    *prometheus.Desc
	trafficUpDesc   *prometheus.Desc
	trafficDownDesc *prometheus.Desc
	connsActiveDesc *prometheus.Desc

	mu sync.Mutex
}

func NewSingboxCollector() *SingboxCollector {
	tr := &http.Transport{
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 800 * time.Millisecond,
		DialContext: (&net.Dialer{
			Timeout: 500 * time.Millisecond,
		}).DialContext,
	}

	clashAPI := os.Getenv("CHEBURNET_CLASH_API")
	if clashAPI == "" {
		clashAPI = "127.0.0.1:9090"
	}
	secret := os.Getenv("CHEBURNET_CLASH_SECRET")

	return &SingboxCollector{
		clashAPI: clashAPI,
		secret:   secret,
		client: &http.Client{
			Transport: tr,
			Timeout:   1 * time.Second,
		},
		upDesc: prometheus.NewDesc(
			"cheburnet_singbox_up",
			"Status of sing-box core (1 = healthy and running, 0 = down).",
			nil, nil,
		),
		cpuSecondsDesc: prometheus.NewDesc(
			"cheburnet_singbox_cpu_seconds_total",
			"Total user and system CPU time spent by sing-box process in seconds.",
			[]string{"mode"}, nil,
		),
		memRssDesc: prometheus.NewDesc(
			"cheburnet_singbox_memory_rss_bytes",
			"Resident Set Size: real physical RAM used by sing-box.",
			nil, nil,
		),
		memVmSizeDesc: prometheus.NewDesc(
			"cheburnet_singbox_memory_vmsize_bytes",
			"Virtual memory size allocated for sing-box process.",
			nil, nil,
		),
		memInUseDesc: prometheus.NewDesc(
			"cheburnet_singbox_memory_inuse_bytes",
			"Go runtime heap memory currently allocated by sing-box.",
			nil, nil,
		),
		trafficUpDesc: prometheus.NewDesc(
			"cheburnet_singbox_traffic_upload_bytes_total",
			"Total bytes uploaded through proxy routes.",
			nil, nil,
		),
		trafficDownDesc: prometheus.NewDesc(
			"cheburnet_singbox_traffic_download_bytes_total",
			"Total bytes downloaded through proxy routes.",
			nil, nil,
		),
		connsActiveDesc: prometheus.NewDesc(
			"cheburnet_singbox_connections_active",
			"Current active connections in sing-box tracking table.",
			nil, nil,
		),
	}
}

func (c *SingboxCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.upDesc
	ch <- c.cpuSecondsDesc
	ch <- c.memRssDesc
	ch <- c.memVmSizeDesc
	ch <- c.memInUseDesc
	ch <- c.trafficUpDesc
	ch <- c.trafficDownDesc
	ch <- c.connsActiveDesc
}

func (c *SingboxCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	pid, configPath := findSingboxProcess()

	// 1. Если процесс не найден в системе — ядро выключено
	if pid <= 0 {
		ch <- prometheus.MustNewConstMetric(c.upDesc, prometheus.GaugeValue, 0)
		return
	}

	// 2. Системные метрики Linux (/proc/[pid]/) отдаются всегда при живом процессе
	if utime, stime, err := readProcessCPU(pid); err == nil {
		ch <- prometheus.MustNewConstMetric(c.cpuSecondsDesc, prometheus.CounterValue, utime, "user")
		ch <- prometheus.MustNewConstMetric(c.cpuSecondsDesc, prometheus.CounterValue, stime, "system")
	}
	if rss, vmSize, err := readProcessMemory(pid); err == nil {
		ch <- prometheus.MustNewConstMetric(c.memRssDesc, prometheus.GaugeValue, float64(rss))
		ch <- prometheus.MustNewConstMetric(c.memVmSizeDesc, prometheus.GaugeValue, float64(vmSize))
	}

	// 3. Автоопределение параметров Clash API из рабочего конфига sing-box
	targetAPI := c.clashAPI
	targetSecret := c.secret

	if apiEndpoint, secret := extractClashAPISettings(configPath); apiEndpoint != "" {
		targetAPI = apiEndpoint
		if secret != "" {
			targetSecret = secret
		}
	}

	baseURL := normalizeURL(targetAPI)

	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()

	// 4. Сетевые метрики из Clash REST API
	connData, err := c.fetchConnections(ctx, baseURL, targetSecret)
	if err != nil {
		// Процесс есть в ОС, но API еще не открыло порт
		ch <- prometheus.MustNewConstMetric(c.upDesc, prometheus.GaugeValue, 1)
		return
	}

	ch <- prometheus.MustNewConstMetric(c.upDesc, prometheus.GaugeValue, 1)
	ch <- prometheus.MustNewConstMetric(c.trafficUpDesc, prometheus.CounterValue, float64(connData.UploadTotal))
	ch <- prometheus.MustNewConstMetric(c.trafficDownDesc, prometheus.CounterValue, float64(connData.DownloadTotal))
	ch <- prometheus.MustNewConstMetric(c.connsActiveDesc, prometheus.GaugeValue, float64(len(connData.Connections)))

	if memData, err := c.fetchMemory(ctx, baseURL, targetSecret); err == nil {
		ch <- prometheus.MustNewConstMetric(c.memInUseDesc, prometheus.GaugeValue, float64(memData.Inuse))
	}
}

// findSingboxProcess сканирует /proc и ищет процессы sing-box или sing-box-lx, возвращая PID и путь к конфигу
func findSingboxProcess() (int, string) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, ""
	}

	myPid := os.Getpid()

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == myPid {
			continue
		}

		isSingbox := false

		// Проверка comm (имя процесса)
		if comm, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "comm")); err == nil {
			c := strings.TrimSpace(string(comm))
			if strings.Contains(c, "sing-box") {
				isSingbox = true
			}
		}

		// Чтение аргументов командной строки
		cmdlineData, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil {
			if isSingbox {
				return pid, ""
			}
			continue
		}

		args := bytes.Split(cmdlineData, []byte{0})
		var configPath string

		for i, arg := range args {
			s := string(arg)
			if strings.Contains(s, "sing-box") {
				isSingbox = true
			}
			if (s == "-c" || s == "--config") && i+1 < len(args) {
				configPath = string(args[i+1])
			}
		}

		if isSingbox {
			return pid, configPath
		}
	}

	return 0, ""
}

// extractClashAPISettings парсит адрес и секрет Clash API напрямую из JSON-конфига ядра
func extractClashAPISettings(configPath string) (string, string) {
	if configPath == "" {
		return "", ""
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", ""
	}

	var cfg struct {
		Experimental struct {
			ClashAPI struct {
				ExternalController string `json:"external_controller"`
				Secret             string `json:"secret"`
			} `json:"clash_api"`
		} `json:"experimental"`
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", ""
	}

	return cfg.Experimental.ClashAPI.ExternalController, cfg.Experimental.ClashAPI.Secret
}

func normalizeURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = "127.0.0.1:9090"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	if strings.HasPrefix(addr, "0.0.0.0:") {
		addr = "127.0.0.1" + strings.TrimPrefix(addr, "0.0.0.0")
	}
	if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
		addr = "http://" + addr
	}
	return strings.TrimRight(addr, "/")
}

func readProcessCPU(pid int) (float64, float64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, err
	}

	idx := bytes.LastIndexByte(data, ')')
	if idx == -1 || idx+2 >= len(data) {
		return 0, 0, fmt.Errorf("malformed stat")
	}

	fields := strings.Fields(string(data[idx+2:]))
	if len(fields) < 13 {
		return 0, 0, fmt.Errorf("not enough fields in stat")
	}

	utimeTicks, err1 := strconv.ParseFloat(fields[11], 64)
	stimeTicks, err2 := strconv.ParseFloat(fields[12], 64)
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("failed to parse cpu ticks")
	}

	return utimeTicks / userHZ, stimeTicks / userHZ, nil
}

func readProcessMemory(pid int) (int64, int64, error) {
	file, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()

	var rss, vmSize int64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "VmRSS:") {
			rss = parseStatusKb(line) * 1024
		} else if strings.HasPrefix(line, "VmSize:") {
			vmSize = parseStatusKb(line) * 1024
		}
		if rss > 0 && vmSize > 0 {
			break
		}
	}
	return rss, vmSize, scanner.Err()
}

func parseStatusKb(line string) int64 {
	fields := strings.Fields(line)
	if len(fields) >= 2 {
		val, _ := strconv.ParseInt(fields[1], 10, 64)
		return val
	}
	return 0
}

type connectionsResp struct {
	DownloadTotal int64         `json:"downloadTotal"`
	UploadTotal   int64         `json:"uploadTotal"`
	Connections   []interface{} `json:"connections"`
}

func (c *SingboxCollector) fetchConnections(ctx context.Context, baseURL, secret string) (*connectionsResp, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/connections", nil)
	if err != nil {
		return nil, err
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status: %d", resp.StatusCode)
	}

	var data connectionsResp
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return &data, nil
}

type memoryResp struct {
	Inuse int64 `json:"inuse"`
}

func (c *SingboxCollector) fetchMemory(ctx context.Context, baseURL, secret string) (*memoryResp, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/memory", nil)
	if err != nil {
		return nil, err
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status: %d", resp.StatusCode)
	}

	var data memoryResp
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return &data, nil
}

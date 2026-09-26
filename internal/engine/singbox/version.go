package singbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"cheburnet/internal/config"
)

type VersionInfo struct {
	Major int
	Minor int
	Patch int
}

type Capabilities struct {
	BinaryPath string
	Major      int
	Minor      int
	Patch      int
	IsLX       bool
	IsExtended bool
	HasXHTTP   bool
	HasAWG     bool
	Tags       map[string]bool
}

var (
	capsMu       sync.RWMutex
	activeBinary string
	cachedCaps   = make(map[string]*Capabilities)
)

// SetBinaryPath устанавливает путь к активному бинарнику, выбранному сервисом
func SetBinaryPath(binPath string) {
	capsMu.Lock()
	defer capsMu.Unlock()
	activeBinary = binPath
}

func resolveSingBoxBin(binPath string) string {
	if binPath != "" {
		if resolved, err := exec.LookPath(binPath); err == nil {
			return resolved
		}
		return binPath
	}

	capsMu.RLock()
	currentActive := activeBinary
	capsMu.RUnlock()

	if currentActive != "" {
		return currentActive
	}

	// Проверяем стандартный sing-box с приоритетом перед кастомными форками
	for _, candidate := range []string{"/usr/bin/sing-box", "sing-box", "/usr/bin/sing-box-lx", "sing-box-lx"} {
		if resolved, err := exec.LookPath(candidate); err == nil {
			return resolved
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "sing-box"
}

// InspectBinary производит глубокий анализ возможностей заданного ядра sing-box
func InspectBinary(binPath string) *Capabilities {
	resolved := resolveSingBoxBin(binPath)

	capsMu.RLock()
	if c, ok := cachedCaps[resolved]; ok {
		capsMu.RUnlock()
		return c
	}
	capsMu.RUnlock()

	capsMu.Lock()
	defer capsMu.Unlock()

	if c, ok := cachedCaps[resolved]; ok {
		return c
	}

	caps := &Capabilities{
		BinaryPath: resolved,
		Tags:       make(map[string]bool),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, resolved, "version")
	outBytes, err := cmd.CombinedOutput()
	if err != nil && len(outBytes) == 0 {
		cachedCaps[resolved] = caps
		return caps
	}

	output := string(outBytes)
	lowerOutput := strings.ToLower(output)

	// 1. Парсинг версии
	re := regexp.MustCompile(`version\s+v?(\d+)\.(\d+)(?:\.(\d+))?`)
	if matches := re.FindStringSubmatch(output); len(matches) >= 3 {
		caps.Major, _ = strconv.Atoi(matches[1])
		caps.Minor, _ = strconv.Atoi(matches[2])
		if len(matches) > 3 && matches[3] != "" {
			caps.Patch, _ = strconv.Atoi(matches[3])
		}
	}

	// 2. Парсинг тегов Go
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Tags:") {
			tagStr := strings.TrimSpace(strings.TrimPrefix(trimmed, "Tags:"))
			for _, t := range strings.Split(tagStr, ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					caps.Tags[t] = true
				}
			}
		}
	}

	if caps.Tags["with_xhttp"] {
		caps.HasXHTTP = true
	}
	if caps.Tags["with_awg"] || caps.Tags["with_amneziawg"] {
		caps.HasAWG = true
	}
	if caps.Tags["with_lx_command"] || caps.Tags["with_lxd"] || caps.Tags["with_lx_chain"] {
		caps.IsLX = true
	}

	// 3. Анализ имени и суффиксов
	if strings.Contains(lowerOutput, "extended") || strings.Contains(lowerOutput, "shtorm") {
		caps.IsExtended = true
	}
	if strings.Contains(lowerOutput, "-lx") || strings.Contains(lowerOutput, " lx") || strings.Contains(lowerOutput, "sing-box-lx") {
		caps.IsLX = true
	}

	// 4. Если поддержка xhttp не подтверждена тегами, выполняем dummy probe
	if !caps.HasXHTTP {
		caps.HasXHTTP = probeXHTTP(resolved)
	}

	cachedCaps[resolved] = caps
	return caps
}

func probeXHTTP(binPath string) bool {
	dummyJSON := `{"outbounds":[{"type":"vless","tag":"p","server":"127.0.0.1","server_port":443,"uuid":"00000000-0000-0000-0000-000000000000","transport":{"type":"xhttp","path":"/"}}]}`

	tmpFile, err := os.CreateTemp("", "sb-probe-*.json")
	if err != nil {
		return false
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(dummyJSON); err != nil {
		_ = tmpFile.Close()
		return false
	}
	_ = tmpFile.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, "check", "-c", tmpFile.Name())
	out, _ := cmd.CombinedOutput()

	return !strings.Contains(string(out), "unknown transport type: xhttp")
}

// SupportsXHTTP возвращает true, только если бинарник действительно поддерживает xhttp
func SupportsXHTTP(binPath string) bool {
	return InspectBinary(binPath).HasXHTTP
}

// IsLX возвращает true для сборок sing-box-lx
func IsLX(binPath string) bool {
	return InspectBinary(binPath).IsLX
}

// IsExtended возвращает true для сборок sing-box-extended
func IsExtended(binPath string) bool {
	return InspectBinary(binPath).IsExtended
}

// buildXHTTPTransport собирает спецификацию блока transport для XHTTP (SplitHTTP)
func buildXHTTPTransport(node *config.GenericNode) (map[string]interface{}, error) {
	if !SupportsXHTTP("") {
		return nil, fmt.Errorf("skipped: transport '%s' is not supported by current sing-box (requires sing-box-lx or extended with 'with_xhttp' tag)", node.Network)
	}

	mode := strings.TrimSpace(node.XHTTPMode)
	if mode == "" {
		mode = "auto"
	}

	path := strings.TrimSpace(node.Path)
	if path == "" {
		path = "/"
	}

	padding := strings.TrimSpace(node.XHTTPPadding)
	if padding == "" {
		padding = "100-1000"
	}

	transport := map[string]interface{}{
		"type":            "xhttp",
		"mode":            mode,
		"path":            path,
		"x_padding_bytes": padding,
	}

	if host := strings.TrimSpace(node.Host); host != "" {
		transport["host"] = host
	}

	if node.XHTTPNoGRPC {
		transport["no_grpc_header"] = true
	}

	if len(node.XHTTPHeaders) > 0 {
		transport["headers"] = node.XHTTPHeaders
	}

	return transport, nil
}

func DetectVersion(binPath string) VersionInfo {
	caps := InspectBinary(binPath)
	if caps.Major == 0 && caps.Minor == 0 {
		return VersionInfo{Major: 1, Minor: 14, Patch: 0}
	}
	return VersionInfo{Major: caps.Major, Minor: caps.Minor, Patch: caps.Patch}
}

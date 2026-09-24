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

var (
	flavorMu      sync.RWMutex
	cachedBinary  string
	cachedXHTTP   bool
	flavorChecked bool
)

func resolveSingBoxBin(binPath string) string {
	if binPath != "" {
		if resolved, err := exec.LookPath(binPath); err == nil {
			return resolved
		}
		return binPath
	}
	for _, candidate := range []string{"/usr/bin/sing-box-lx", "/usr/bin/sing-box", "sing-box-lx", "sing-box"} {
		if resolved, err := exec.LookPath(candidate); err == nil {
			return resolved
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "sing-box"
}

// SupportsXHTTP возвращает true, только если бинарник является форком sing-box-lx или sing-box-extended
func SupportsXHTTP(binPath string) bool {
	resolved := resolveSingBoxBin(binPath)

	flavorMu.RLock()
	if flavorChecked && cachedBinary == resolved {
		supported := cachedXHTTP
		flavorMu.RUnlock()
		return supported
	}
	flavorMu.RUnlock()

	flavorMu.Lock()
	defer flavorMu.Unlock()

	cleanPath := strings.ToLower(resolved)
	if strings.Contains(cleanPath, "-lx") || strings.Contains(cleanPath, "extended") {
		cachedBinary = resolved
		cachedXHTTP = true
		flavorChecked = true
		return true
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, resolved, "version")
	outBytes, err := cmd.CombinedOutput()
	if err != nil && len(outBytes) == 0 {
		cachedBinary = resolved
		cachedXHTTP = false
		flavorChecked = true
		return false
	}

	verStr := strings.ToLower(string(outBytes))
	isSupported := strings.Contains(verStr, "-lx") ||
		strings.Contains(verStr, " lx") ||
		strings.Contains(verStr, "extended")

	cachedBinary = resolved
	cachedXHTTP = isSupported
	flavorChecked = true
	return isSupported
}

// buildXHTTPTransport собирает спецификацию блока transport для XHTTP (SplitHTTP)
func buildXHTTPTransport(node *config.GenericNode) (map[string]interface{}, error) {
	if !SupportsXHTTP("") {
		return nil, fmt.Errorf("skipped: transport '%s' is only supported by extended/lx sing-box builds", node.Network)
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
	binPath = resolveSingBoxBin(binPath)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, "version")
	outBytes, err := cmd.CombinedOutput()
	if err != nil && len(outBytes) == 0 {
		return VersionInfo{Major: 1, Minor: 14, Patch: 0}
	}

	outStr := strings.TrimSpace(string(outBytes))

	re := regexp.MustCompile(`version\s+v?(\d+)\.(\d+)(?:\.(\d+))?`)
	matches := re.FindStringSubmatch(outStr)
	if len(matches) >= 3 {
		major, _ := strconv.Atoi(matches[1])
		minor, _ := strconv.Atoi(matches[2])
		patch := 0
		if len(matches) > 3 && matches[3] != "" {
			patch, _ = strconv.Atoi(matches[3])
		}
		return VersionInfo{Major: major, Minor: minor, Patch: patch}
	}

	return VersionInfo{Major: 1, Minor: 14, Patch: 0}
}

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
	BinaryPath         string
	Major              int
	Minor              int
	Patch              int
	IsLX               bool
	IsExtended         bool
	IsPodkop           bool
	SupportX25519MLKEM bool
	HasXHTTP           bool
	HasAWG             bool
	HasFallbacks       bool
	HasVLESSEncrypt    bool
	Tags               map[string]bool
	Features           map[string]bool
}

var (
	capsMu        sync.RWMutex
	activeBinary  string
	cachedCaps    = make(map[string]*Capabilities)
	reExtendedVer = regexp.MustCompile(`-extended-(\d+)\.(\d+)(?:\.(\d+))?`)
)

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
		if _, err := os.Stat(binPath); err == nil {
			return binPath
		}
	}

	capsMu.RLock()
	currentActive := activeBinary
	capsMu.RUnlock()

	if currentActive != "" {
		return currentActive
	}

	candidates := []string{
		"/usr/bin/podkop-engine",
		"/usr/bin/sing-box-extended",
		"/usr/bin/sing-box-lx",
		"/usr/bin/sing-box",
		"podkop-engine",
		"sing-box-extended",
		"sing-box-lx",
		"sing-box",
	}

	for _, candidate := range candidates {
		if resolved, err := exec.LookPath(candidate); err == nil {
			return resolved
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "/usr/bin/sing-box"
}

func ExtendedSupportsX25519MLKEM768(verStr string) bool {
	clean := strings.TrimSpace(verStr)
	if clean == "" {
		return false
	}

	matches := reExtendedVer.FindStringSubmatch(clean)
	if len(matches) < 3 {
		return false
	}

	major, err := strconv.Atoi(matches[1])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(matches[2])
	if err != nil {
		return false
	}
	patch := 0
	if len(matches) > 3 && matches[3] != "" {
		if p, err := strconv.Atoi(matches[3]); err == nil {
			patch = p
		}
	}

	if major > 2 {
		return true
	}
	if major == 2 {
		if minor > 7 {
			return true
		}
		if minor == 7 {
			return patch >= 2
		}
	}
	return false
}

func ResetCapabilitiesCache() {
	capsMu.Lock()
	defer capsMu.Unlock()
	cachedCaps = make(map[string]*Capabilities)
}

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
		Features:   make(map[string]bool),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, resolved, "version")
	outBytes, _ := cmd.CombinedOutput()
	output := string(outBytes)
	lowerOutput := strings.ToLower(output)

	re := regexp.MustCompile(`version\s+v?(\d+)\.(\d+)(?:\.(\d+))?`)
	if matches := re.FindStringSubmatch(output); len(matches) >= 3 {
		caps.Major, _ = strconv.Atoi(matches[1])
		caps.Minor, _ = strconv.Atoi(matches[2])
		if len(matches) > 3 && matches[3] != "" {
			caps.Patch, _ = strconv.Atoi(matches[3])
		}
	}

	if caps.Major == 0 && caps.Minor == 0 {
		if data, err := os.ReadFile("/etc/cheburnet/sing-box-version"); err == nil {
			vStr := strings.TrimSpace(string(data))
			output = vStr
			lowerOutput = strings.ToLower(vStr)
			reAlt := regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)
			if m := reAlt.FindStringSubmatch(vStr); len(m) >= 3 {
				caps.Major, _ = strconv.Atoi(m[1])
				caps.Minor, _ = strconv.Atoi(m[2])
				if len(m) > 3 && m[3] != "" {
					caps.Patch, _ = strconv.Atoi(m[3])
				}
			}
		}
	}

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
		} else if strings.HasPrefix(trimmed, "Features:") {
			featStr := strings.TrimSpace(strings.TrimPrefix(trimmed, "Features:"))
			for _, f := range strings.Split(featStr, ",") {
				f = strings.TrimSpace(f)
				if f != "" {
					caps.Features[f] = true
				}
			}
		}
	}

	if strings.Contains(lowerOutput, "pdk") || strings.Contains(lowerOutput, "podkop") || strings.Contains(resolved, "podkop") {
		caps.IsPodkop = true
	}
	if strings.Contains(lowerOutput, "extended") || strings.Contains(lowerOutput, "shtorm") {
		caps.IsExtended = true
	}
	if strings.Contains(lowerOutput, "-lx") || strings.Contains(lowerOutput, " lx") || strings.Contains(lowerOutput, "sing-box-lx") {
		caps.IsLX = true
	}

	if caps.Tags["with_xhttp"] || caps.Features["transport.xhttp"] || caps.Features["xhttp"] || caps.IsPodkop {
		caps.HasXHTTP = true
	}
	if caps.Tags["with_awg"] || caps.Tags["with_amneziawg"] {
		caps.HasAWG = true
	}
	if caps.Tags["with_lx_command"] || caps.Tags["with_lxd"] || caps.Tags["with_lx_chain"] {
		caps.IsLX = true
	}

	if caps.Features["urltest.fallbacks"] || strings.Contains(lowerOutput, "urltest.fallbacks") {
		caps.HasFallbacks = true
	}
	if caps.Features["vless-encryption"] || strings.Contains(lowerOutput, "vless-encryption") {
		caps.HasVLESSEncrypt = true
	}

	if !caps.IsPodkop {
		if ExtendedSupportsX25519MLKEM768(output) {
			caps.SupportX25519MLKEM = true
		} else if data, err := os.ReadFile("/etc/cheburnet/sing-box-version"); err == nil {
			if ExtendedSupportsX25519MLKEM768(string(data)) {
				caps.SupportX25519MLKEM = true
			}
		}
	}

	if !caps.HasXHTTP {
		if caps.Major > 1 || (caps.Major == 1 && caps.Minor >= 14) || caps.IsExtended || caps.IsLX {
			caps.HasXHTTP = true
		} else {
			caps.HasXHTTP = probeXHTTP(resolved)
		}
	}

	cachedCaps[resolved] = caps
	return caps
}

func probeXHTTP(binPath string) bool {
	dummyJSON := `{
		"log": {"level": "panic"},
		"outbounds": [{
			"type": "vless",
			"tag": "probe-xhttp",
			"server": "127.0.0.1",
			"server_port": 443,
			"uuid": "a8098c1a-f86e-11da-bd1a-00112444be1e",
			"transport": {
				"type": "xhttp",
				"path": "/",
				"x_padding_bytes": "100-1000"
			}
		}]
	}`

	tmpFile, err := os.CreateTemp("/tmp", "sb-probe-*.json")
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
	out, err := cmd.CombinedOutput()
	outStr := string(out)

	if strings.Contains(outStr, "unknown transport type: xhttp") {
		return false
	}

	return err == nil || !strings.Contains(outStr, "transport")
}

func SupportsXHTTP(binPath string) bool {
	return InspectBinary(binPath).HasXHTTP
}

func SupportsX25519MLKEM(binPath string) bool {
	caps := InspectBinary(binPath)
	return caps.SupportX25519MLKEM
}

func SupportsFallbacks(binPath string) bool {
	return InspectBinary(binPath).HasFallbacks
}

func SupportsVLESSEncryption(binPath string) bool {
	return InspectBinary(binPath).HasVLESSEncrypt
}

func IsLX(binPath string) bool {
	return InspectBinary(binPath).IsLX
}

func IsExtended(binPath string) bool {
	return InspectBinary(binPath).IsExtended
}

func IsPodkop(binPath string) bool {
	return InspectBinary(binPath).IsPodkop
}

func buildXHTTPTransport(node *config.GenericNode, binPath string) (map[string]interface{}, error) {
	if !SupportsXHTTP(binPath) {
		return nil, fmt.Errorf("skipped: transport '%s' is not supported by current core", node.Network)
	}

	tagLower := strings.ToLower(node.Tag)
	addrLower := strings.ToLower(node.Address)
	hostLower := strings.ToLower(node.Host)
	sniLower := strings.ToLower(node.SNI)

	isBridge := strings.Contains(tagLower, "bridge") ||
		strings.Contains(addrLower, "bridge") ||
		strings.Contains(hostLower, "bridge") ||
		strings.Contains(sniLower, "bridge")

	mode := strings.TrimSpace(node.XHTTPMode)
	if mode == "" || mode == "auto" {
		if isBridge {
			mode = "packet-up"
		} else {
			mode = "auto"
		}
	}

	path := strings.TrimSpace(node.Path)
	if path == "" {
		path = "/"
	}

	padding := strings.TrimSpace(node.XHTTPPadding)
	if padding == "" || padding == "0" || padding == "false" || padding == "none" {
		if isBridge {
			padding = "500-2000"
		} else {
			padding = "100-1000"
		}
	}

	headers := make(map[string]string)
	for k, v := range node.XHTTPHeaders {
		headers[k] = v
	}

	transport := map[string]interface{}{
		"type":            "xhttp",
		"mode":            mode,
		"path":            path,
		"headers":         headers,
		"x_padding_bytes": padding,
		"no_grpc_header":  node.XHTTPNoGRPC,
		"xmux": map[string]interface{}{
			"max_concurrency":     "16-32",
			"h_max_request_times": "600-900",
		},
	}

	if isBridge || mode == "packet-up" {
		transport["sc_max_each_post_bytes"] = 1000000
		transport["sc_min_posts_interval_ms"] = 30
	}

	if host := strings.TrimSpace(node.Host); host != "" {
		transport["host"] = host
	} else if node.SNI != "" {
		transport["host"] = strings.TrimSpace(node.SNI)
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

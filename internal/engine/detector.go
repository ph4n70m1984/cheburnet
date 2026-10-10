package engine

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"cheburnet/internal/engine/singbox"
)

type EngineType string

const (
	EngineSingBox EngineType = "sing-box"
)

type BinaryInfo struct {
	Path            string
	VersionRaw      string
	Major           int
	Minor           int
	Patch           int
	IsExtended      bool
	IsLX            bool
	IsPodkop        bool
	HasXHTTP        bool
	HasAWG          bool
	HasFallbacks    bool
	HasVLESSEncrypt bool
	Tags            []string
	Features        []string
}

// CheckBinary валидирует наличие, исполняемость и детальные возможности бинарника ядра
func CheckBinary(binPath string, engine EngineType) (*BinaryInfo, error) {
	fi, err := os.Stat(binPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("бинарник не найден по пути: %s", binPath)
		}
		return nil, fmt.Errorf("ошибка доступа к бинарнику: %w", err)
	}

	if fi.Mode()&0111 == 0 {
		return nil, fmt.Errorf("файл %s не имеет прав на выполнение (chmod +x)", binPath)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, "version")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("сбой выполнения '%s version': %w (вывод: %s)", binPath, err, strings.TrimSpace(out.String()))
	}

	output := out.String()
	lines := strings.Split(output, "\n")
	firstLine := ""
	if len(lines) > 0 {
		firstLine = strings.TrimSpace(lines[0])
	}

	info := &BinaryInfo{
		Path:       binPath,
		VersionRaw: firstLine,
		Tags:       make([]string, 0),
		Features:   make([]string, 0),
	}

	lowerOutput := strings.ToLower(output)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Tags:") {
			tagStr := strings.TrimSpace(strings.TrimPrefix(trimmed, "Tags:"))
			for _, t := range strings.Split(tagStr, ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					info.Tags = append(info.Tags, t)
					switch t {
					case "with_xhttp":
						info.HasXHTTP = true
					case "with_awg", "with_amneziawg":
						info.HasAWG = true
					case "with_lx_command", "with_lxd", "with_lx_chain":
						info.IsLX = true
					}
				}
			}
		} else if strings.HasPrefix(trimmed, "Features:") {
			featStr := strings.TrimSpace(strings.TrimPrefix(trimmed, "Features:"))
			for _, f := range strings.Split(featStr, ",") {
				f = strings.TrimSpace(f)
				if f != "" {
					info.Features = append(info.Features, f)
					switch f {
					case "urltest.fallbacks":
						info.HasFallbacks = true
					case "vless-encryption":
						info.HasVLESSEncrypt = true
					case "xhttp":
						info.HasXHTTP = true
					}
				}
			}
		}
	}

	if strings.Contains(lowerOutput, "podkop") || strings.Contains(binPath, "podkop") {
		info.IsPodkop = true
		info.HasFallbacks = true
		info.HasVLESSEncrypt = true
		info.HasXHTTP = true
	}
	if strings.Contains(lowerOutput, "extended") || strings.Contains(lowerOutput, "shtorm") {
		info.IsExtended = true
	}
	if strings.Contains(lowerOutput, "-lx") || strings.Contains(lowerOutput, " lx") || strings.Contains(lowerOutput, "sing-box-lx") {
		info.IsLX = true
	}

	if !info.HasXHTTP {
		info.HasXHTTP = ProbeXHTTPSupport(binPath)
	}

	re := regexp.MustCompile(`v?(\d+)\.(\d+)(?:\.(\d+))?`)
	matches := re.FindStringSubmatch(output)
	if len(matches) >= 3 {
		info.Major, _ = strconv.Atoi(matches[1])
		info.Minor, _ = strconv.Atoi(matches[2])
		if len(matches) > 3 && matches[3] != "" {
			info.Patch, _ = strconv.Atoi(matches[3])
		}
	} else {
		return nil, fmt.Errorf("не удалось определить версию ядра из вывода: %q", info.VersionRaw)
	}

	if err := validateMinimumVersion(info); err != nil {
		return nil, err
	}

	singbox.SetBinaryPath(binPath)

	return info, nil
}

func ProbeXHTTPSupport(binPath string) bool {
	dummyJSON := `{
  "outbounds": [
    {
      "type": "vless",
      "tag": "probe",
      "server": "127.0.0.1",
      "server_port": 443,
      "uuid": "00000000-0000-0000-0000-000000000000",
      "transport": {
        "type": "xhttp",
        "path": "/"
      }
    }
  ]
}`

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

	if strings.Contains(string(out), "unknown transport type: xhttp") {
		return false
	}

	return true
}

func validateMinimumVersion(info *BinaryInfo) error {
	if info.Major < 1 || (info.Major == 1 && info.Minor < 8) {
		return fmt.Errorf("версия ядра %d.%d.%d слишком старая (требуется >= 1.8.0)", info.Major, info.Minor, info.Patch)
	}
	return nil
}

func TestConfig(binPath string, configPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, "check", "-c", configPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("валидация конфига провалена: %w (вывод: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

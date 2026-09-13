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
)

type EngineType string

const (
	EngineSingBox EngineType = "sing-box"
)

type BinaryInfo struct {
	Path       string
	VersionRaw string
	Major      int
	Minor      int
	Patch      int
	IsExtended bool
}

// CheckBinary валидирует наличие, исполняемость и извлекает версию бинарника sing-box
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
	info := &BinaryInfo{
		Path:       binPath,
		VersionRaw: strings.TrimSpace(strings.Split(output, "\n")[0]),
	}

	if strings.Contains(strings.ToLower(output), "extended") || strings.Contains(strings.ToLower(output), "shtorm") {
		info.IsExtended = true
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

	return info, nil
}

func validateMinimumVersion(info *BinaryInfo) error {
	// Для формата 1.8+ и работы clash_api
	if info.Major < 1 || (info.Major == 1 && info.Minor < 8) {
		return fmt.Errorf("версия sing-box %d.%d.%d слишком старая (требуется >= 1.8.0)", info.Major, info.Minor, info.Patch)
	}
	return nil
}

// TestConfig выполняет предварительный smoke-тест сгенерированного файла конфигурации sing-box
func TestConfig(binPath string, configPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, "check", "-c", configPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("валидация конфига sing-box провалена: %w (вывод: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

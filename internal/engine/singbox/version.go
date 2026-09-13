package singbox

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type VersionInfo struct {
	Major int
	Minor int
	Patch int
}

func DetectVersion(binPath string) VersionInfo {
	if binPath == "" {
		binPath = "sing-box"
	}

	// Если передан жесткий путь, но файла там нет, ищем в PATH
	if resolved, err := exec.LookPath(binPath); err == nil {
		binPath = resolved
	} else if resolvedDefault, err := exec.LookPath("sing-box"); err == nil {
		binPath = resolvedDefault
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, "version")
	outBytes, err := cmd.CombinedOutput()
	if err != nil && len(outBytes) == 0 {
		return VersionInfo{Major: 1, Minor: 14, Patch: 0}
	}

	outStr := strings.TrimSpace(string(outBytes))

	// Ищем строго конструкцию "version 1.14" в первой строке
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

	// Безопасный дефолт под текущую систему
	return VersionInfo{Major: 1, Minor: 14, Patch: 0}
}

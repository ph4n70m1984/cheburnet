package network

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	RTTableID   = 105
	RTTableName = "cheburnet"
	FwmarkHex   = "0x100000"
	FwmarkDec   = "1048576"
)

func SetupRouting() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Добавляем таблицу cheburnet в /etc/iproute2/rt_tables
	_ = exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("grep -q '%d %s' /etc/iproute2/rt_tables 2>/dev/null || echo '%d %s' >> /etc/iproute2/rt_tables", RTTableID, RTTableName, RTTableID, RTTableName)).Run()

	// 2. Локальный роут на loopback для перехвата TProxy
	_ = exec.CommandContext(ctx, "ip", "route", "replace", "local", "0.0.0.0/0", "dev", "lo", "table", fmt.Sprintf("%d", RTTableID)).Run()

	// 3. Проверяем, существует ли правило fwmark
	out, _ := exec.CommandContext(ctx, "ip", "rule", "show").CombinedOutput()
	outStr := string(out)
	if strings.Contains(outStr, FwmarkHex) || strings.Contains(outStr, FwmarkDec) {
		return nil
	}

	// 4. Попытки добавления правила с различными синтаксисами
	attempts := [][]string{
		{"ip", "rule", "add", "fwmark", FwmarkHex, "table", fmt.Sprintf("%d", RTTableID), "priority", "105"},
		{"ip", "rule", "add", "fwmark", FwmarkDec, "table", fmt.Sprintf("%d", RTTableID), "priority", "105"},
		{"ip", "rule", "add", "fwmark", FwmarkHex, "lookup", fmt.Sprintf("%d", RTTableID), "prio", "105"},
		{"ip", "rule", "add", "fwmark", FwmarkDec, "lookup", fmt.Sprintf("%d", RTTableID), "prio", "105"},
	}

	var lastErr error
	var lastOutput string

	for _, args := range attempts {
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		cmd.Stdout = &stderr

		if err := cmd.Run(); err == nil {
			return nil
		} else {
			lastErr = err
			lastOutput = strings.TrimSpace(stderr.String())
		}
	}

	return fmt.Errorf("all ip rule attempts failed, last error (%v): %s", lastErr, lastOutput)
}

func CleanupRouting() {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	_ = exec.CommandContext(ctx, "ip", "rule", "del", "fwmark", FwmarkHex, "table", fmt.Sprintf("%d", RTTableID)).Run()
	_ = exec.CommandContext(ctx, "ip", "rule", "del", "fwmark", FwmarkDec, "table", fmt.Sprintf("%d", RTTableID)).Run()
	_ = exec.CommandContext(ctx, "ip", "route", "flush", "table", fmt.Sprintf("%d", RTTableID)).Run()
}

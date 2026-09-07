package network

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

const (
	RTTableID   = 105
	RTTableName = "cheburnet"
	FwmarkHex   = "0x100000"
	FwmarkDec   = "1048576"
)

func SetupRouting() error {
	// 1. Добавляем таблицу cheburnet в /etc/iproute2/rt_tables, если её там нет
	_ = exec.Command("sh", "-c", fmt.Sprintf("grep -q '%d %s' /etc/iproute2/rt_tables 2>/dev/null || echo '%d %s' >> /etc/iproute2/rt_tables", RTTableID, RTTableName, RTTableID, RTTableName)).Run()

	// 2. Локальный роут на loopback для перехвата TProxy
	_ = exec.Command("ip", "route", "replace", "local", "0.0.0.0/0", "dev", "lo", "table", fmt.Sprintf("%d", RTTableID)).Run()

	// 3. Проверяем, существует ли уже это правило в системе
	out, _ := exec.Command("ip", "rule", "show").CombinedOutput()
	outStr := string(out)
	if strings.Contains(outStr, FwmarkHex) || strings.Contains(outStr, FwmarkDec) {
		return nil
	}

	// 4. Последовательные попытки добавления с разными синтаксисами iproute2
	attempts := [][]string{
		{"ip", "rule", "add", "fwmark", FwmarkHex, "table", fmt.Sprintf("%d", RTTableID), "priority", "105"},
		{"ip", "rule", "add", "fwmark", FwmarkDec, "table", fmt.Sprintf("%d", RTTableID), "priority", "105"},
		{"ip", "rule", "add", "fwmark", FwmarkHex, "lookup", fmt.Sprintf("%d", RTTableID), "prio", "105"},
		{"ip", "rule", "add", "fwmark", FwmarkDec, "lookup", fmt.Sprintf("%d", RTTableID), "prio", "105"},
	}

	var lastErr error
	var lastOutput string

	for _, args := range attempts {
		cmd := exec.Command(args[0], args[1:]...)
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
	_ = exec.Command("ip", "rule", "del", "fwmark", FwmarkHex, "table", fmt.Sprintf("%d", RTTableID)).Run()
	_ = exec.Command("ip", "rule", "del", "fwmark", FwmarkDec, "table", fmt.Sprintf("%d", RTTableID)).Run()
	_ = exec.Command("ip", "route", "flush", "table", fmt.Sprintf("%d", RTTableID)).Run()
}

package service

import (
	"os/exec"
	"syscall"
)

// RestartAsync инициирует перезапуск сервиса через ubus procd RPC,
// отвязавшись от процесса демона во избежание SIGTERM-гонки
func RestartAsync() error {
	cmd := exec.Command("ubus", "call", "service", "restart", `{"name":"cheburnet"}`)

	// Setsid изолирует процесс ubus от сигналов завершения текущей группы
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}

	// Start() возвращает управление немедленно, не блокируя HTTP-хендлер
	return cmd.Start()
}

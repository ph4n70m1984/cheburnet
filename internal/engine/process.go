package engine

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// NewIsolatedCmd создает процесс с гарантированным завершением через Pdeathsig при падении родителя
func NewIsolatedCmd(ctx context.Context, binPath string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Pdeathsig: syscall.SIGKILL,
	}
	return cmd
}

// TerminateCmd гарантирует корректную остановку процесса:
// 1. Посылает SIGTERM.
// 2. Ожидает завершения до 2 секунд.
// 3. При зависании применяет жесткий SIGKILL и ожидает освобождения дескриптора.
func TerminateCmd(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	// 1. Посылаем SIGTERM для штатного закрытия сокетов
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		if err == syscall.ESRCH {
			return nil // Процесс уже завершен
		}
		_ = cmd.Process.Kill()
		return nil
	}

	// 2. Ожидаем завершения с таймаутом
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
		// 3. Процесс не ответил на SIGTERM, отправляем SIGKILL
		_ = cmd.Process.Kill()
		select {
		case <-done:
			return nil
		case <-time.After(1 * time.Second):
			return fmt.Errorf("process pid %d did not exit after SIGKILL", cmd.Process.Pid)
		}
	}
}

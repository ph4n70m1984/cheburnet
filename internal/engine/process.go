package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"syscall"
	"time"
)

// NewIsolatedCmd создает процесс с гарантированным завершением через Pdeathsig при падении родителя[cite: 9]
func NewIsolatedCmd(ctx context.Context, binPath string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Pdeathsig: syscall.SIGKILL,
	}
	return cmd
}

// TerminateCmd гарантирует корректную остановку процесса с учетом родительского контекста:
// 1. Посылает SIGTERM.
// 2. Ожидает завершения до 2 секунд (или до отмены ctx).
// 3. При зависании применяет жесткий SIGKILL и ожидает освобождения дескриптора.
func TerminateCmd(ctx context.Context, cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	pid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	// 1. Посылаем SIGTERM
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil // Процесс уже мертв
		}
		// Если SIGTERM не прошел, форсируем жесткий SIGKILL
		if killErr := cmd.Process.Kill(); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
			return fmt.Errorf("SIGTERM failed (%v), SIGKILL fallback also failed: %w", err, killErr)
		}
		select {
		case <-done:
			return nil
		case <-time.After(time.Second):
			_ = cmd.Process.Release()
			log.Printf("[CRITICAL] Process pid %d is unresponsive to fallback SIGKILL. Released handle", pid)
			return fmt.Errorf("process pid %d did not exit after fallback SIGKILL", pid)
		}
	}

	// 2. Ожидаем штатного завершения после SIGTERM
	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
		// Таймаут штатного завершения истек
	case <-ctx.Done():
		// Родительский контекст завершен (аварийный shutdown)
	}

	// 3. Форсируем завершение через SIGKILL
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, syscall.ESRCH) {
		log.Printf("[WARN] SIGKILL to pid %d failed: %v", pid, err)
	}

	select {
	case <-done:
		return nil
	case <-time.After(1 * time.Second):
		// Процесс завис в Uninterruptible Sleep (D-state)
		_ = cmd.Process.Release()
		log.Printf("[CRITICAL] Process pid %d is unresponsive to SIGKILL (likely in D-state). Released handle, process may remain as zombie until reboot", pid)
		return fmt.Errorf("process pid %d did not exit after SIGKILL (uninterruptible state)", pid)
	}
}

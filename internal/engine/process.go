package engine

import (
	"context"
	"os/exec"
	"syscall"
)

func NewIsolatedCmd(ctx context.Context, binPath string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Pdeathsig: syscall.SIGKILL, // Убивает дочернее ядро, если родительский демон завершился
	}
	return cmd
}

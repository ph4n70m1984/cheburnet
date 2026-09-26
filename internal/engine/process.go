package engine

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// RingLogBuffer реализует потокобезопасный кольцевой буфер для сбора логов процесса без переполнения памяти
type RingLogBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func NewRingLogBuffer(max int) *RingLogBuffer {
	if max <= 0 {
		max = 64 * 1024 // 64 КБ по умолчанию
	}
	return &RingLogBuffer{buf: make([]byte, 0, max), max: max}
}

func (r *RingLogBuffer) Write(p []byte) (n int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n = len(p)
	if n >= r.max {
		r.buf = append(r.buf[:0], p[n-r.max:]...)
		return n, nil
	}
	overflow := len(r.buf) + n - r.max
	if overflow > 0 {
		r.buf = r.buf[overflow:]
	}
	r.buf = append(r.buf, p...)
	return n, nil
}

func (r *RingLogBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}

// NewIsolatedCmd создает процесс с изоляцией группы процессов (Setpgid) и завершением при гибели родителя
func NewIsolatedCmd(ctx context.Context, binPath string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
	return cmd
}

// TerminateCmd гарантирует остановку всей группы процессов (PGID):
// 1. Посылает SIGTERM группе процессов (-pid).
// 2. Ожидает завершения до 2 секунд (или до отмены ctx).
// 3. При зависании применяет жесткий SIGKILL (-pid) и освобождает дескриптор.
func TerminateCmd(ctx context.Context, cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	pid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	// 1. Отправляем SIGTERM группе процессов и основному процессу
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	_ = cmd.Process.Signal(syscall.SIGTERM)

	// 2. Ожидаем штатного завершения
	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
	}

	// 3. Форсируем SIGKILL группе процессов
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()

	select {
	case <-done:
		return nil
	case <-time.After(1 * time.Second):
		_ = cmd.Process.Release()
		log.Printf("[CRITICAL] Process pid %d is unresponsive to SIGKILL. Released handle.", pid)
		return fmt.Errorf("process pid %d did not exit after SIGKILL", pid)
	}
}

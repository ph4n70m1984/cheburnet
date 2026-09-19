package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"cheburnet/internal/config"
)

var (
	// ErrReloadInProgress возвращается при попытке параллельного запуска перезагрузки
	ErrReloadInProgress = errors.New("engine reload is already in progress")

	// reloadGateMu гарантирует атомарность фаз сборки, перезапуска и отката ядра
	reloadGateMu sync.Mutex
)

// SafeReload атомарно генерирует новый конфиг, валидирует его силами sing-box check,
// бэкапит рабочий конфиг, перезапускает процесс,
// проводит проверку здоровья и выполняет автоматический откат при сбое.
func SafeReload(ctx context.Context, eng Engine, cfg *config.CheburConfig, targetPath string) error {
	if !reloadGateMu.TryLock() {
		log.Printf("[engine-reload] Rejecting concurrent reload request: operation already in progress")
		return ErrReloadInProgress
	}
	defer reloadGateMu.Unlock()

	stagingPath := strings.TrimSuffix(targetPath, ".json") + ".new.json"
	backupPath := strings.TrimSuffix(targetPath, ".json") + ".bak.json"

	defer os.Remove(stagingPath)

	// 1. Pre-flight: сборка конфигурации во временный файл
	if err := eng.BuildConfig(cfg, stagingPath); err != nil {
		return fmt.Errorf("build config failed for %s: %w (active process untouched)", eng.Name(), err)
	}

	// 2. Валидация бинарником (sing-box check) с передачей контекста
	if err := eng.ValidateConfig(ctx, stagingPath); err != nil {
		return fmt.Errorf("binary validation failed for %s: %w (active process untouched)", eng.Name(), err)
	}

	// 3. Бэкап текущего рабочего конфига
	hasBackup := false
	if _, err := os.Stat(targetPath); err == nil {
		if err := copyFile(targetPath, backupPath); err != nil {
			return fmt.Errorf("aborting reload: failed to create safety backup of current config: %w (active process untouched)", err)
		}
		hasBackup = true
	}

	// 4. Атомарная замена рабочего файла валидированным
	if err := os.Rename(stagingPath, targetPath); err != nil {
		if hasBackup {
			_ = os.Remove(backupPath)
		}
		return fmt.Errorf("failed to commit staging config: %w", err)
	}

	// 5. Остановка текущего процесса ядра и пауза для освобождения портов и памяти ОС
	if err := eng.Stop(); err != nil {
		log.Printf("[engine-reload] warning: stop returned error: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// 6. Запуск обновленного ядра (используем context.Background, чтобы отмена родительского
	// reloadCtx по таймауту не убила работающий процесс ядра)
	startCtx := context.Background()
	if err := eng.Start(startCtx, targetPath); err != nil {
		return triggerRollback(startCtx, eng, targetPath, backupPath, hasBackup, fmt.Errorf("engine start failed: %w", err))
	}

	// 7. Пост-старт верификация локальных портов (выжидаем 2 секунды для инициализации структуры нод)
	time.Sleep(2 * time.Second)

	healthCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	if err := VerifyEngineAlive(healthCtx, cfg); err != nil {
		log.Printf("[engine-reload] CRITICAL: Engine %s started but health check failed: %v", eng.Name(), err)
		return triggerRollback(startCtx, eng, targetPath, backupPath, hasBackup, fmt.Errorf("health check failed: %w", err))
	}

	if hasBackup {
		_ = os.Remove(backupPath)
	}
	return nil
}

func triggerRollback(ctx context.Context, eng Engine, targetPath, backupPath string, hasBackup bool, originalErr error) error {
	log.Printf("[engine-reload] Initiating ROLLBACK due to: %v", originalErr)

	if stopErr := eng.Stop(); stopErr != nil {
		log.Printf("[engine-reload] warning: stop failed during rollback cleanup: %v", stopErr)
	}
	time.Sleep(300 * time.Millisecond)

	if !hasBackup {
		return fmt.Errorf("%w; rollback impossible: no previous backup exists", originalErr)
	}

	if _, statErr := os.Stat(backupPath); statErr != nil {
		return fmt.Errorf("%w; rollback failed: backup file missing: %v", originalErr, statErr)
	}

	// Восстанавливаем резервный рабочий конфиг
	if err := os.Rename(backupPath, targetPath); err != nil {
		if copyErr := copyFile(backupPath, targetPath); copyErr != nil {
			return fmt.Errorf("%w; rollback failed to restore file: %v (copy fallback error: %v)", originalErr, err, copyErr)
		}
		_ = os.Remove(backupPath)
	}

	// Запускаем проверенную рабочую конфигурацию
	if rbErr := eng.Start(ctx, targetPath); rbErr != nil {
		log.Printf("[engine-reload] FATAL: rollback start failed: %v", rbErr)
		return fmt.Errorf("%w; rollback start also failed: %v", originalErr, rbErr)
	}

	log.Printf("[engine-reload] Rollback successful: %s restored from backup config", eng.Name())
	return fmt.Errorf("rolled back to previous config: %w", originalErr)
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

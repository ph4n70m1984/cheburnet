package engine

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"cheburnet/internal/config"
)

// SafeReload атомарно генерирует новый конфиг, валидирует его силами ядра,
// гарантированно бэкапит рабочий конфиг, перезапускает процесс,
// проводит проверку здоровья и выполняет автоматический откат при сбое.
func SafeReload(ctx context.Context, eng Engine, cfg *config.CheburConfig, targetPath string) error {
	// Сохраняем расширение .json, чтобы Xray и Sing-box корректно определяли формат
	stagingPath := strings.TrimSuffix(targetPath, ".json") + ".new.json"
	backupPath := strings.TrimSuffix(targetPath, ".json") + ".bak.json"

	defer os.Remove(stagingPath)

	// 1. Pre-flight: сборка конфигурации во временный файл
	if err := eng.BuildConfig(cfg, stagingPath); err != nil {
		return fmt.Errorf("build config failed for %s: %w (active process untouched)", eng.Name(), err)
	}

	// 2. Валидация бинарником (sing-box check или xray -test)
	if err := eng.ValidateConfig(stagingPath); err != nil {
		return fmt.Errorf("binary validation failed for %s: %w (active process untouched)", eng.Name(), err)
	}

	// 3. Строгий бэкап текущего рабочего конфига.
	// Если рабочий файл есть, но бэкап не удался — НЕМЕДЛЕННЫЙ ABORT.
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

	// 5. Остановка текущего процесса ядра
	if err := eng.Stop(); err != nil {
		log.Printf("[engine-reload] warning: stop returned error: %v", err)
	}

	// 6. Запуск обновленного ядра
	if err := eng.Start(ctx, targetPath); err != nil {
		return triggerRollback(ctx, eng, targetPath, backupPath, hasBackup, fmt.Errorf("engine start failed: %w", err))
	}

	// 7. Пост-старт верификация локальных портов и DNS (1 сек на bind сокетов)
	time.Sleep(1 * time.Second)

	healthCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if err := VerifyEngineAlive(healthCtx, cfg); err != nil {
		log.Printf("[engine-reload] CRITICAL: Engine %s started but health check failed: %v", eng.Name(), err)
		return triggerRollback(ctx, eng, targetPath, backupPath, hasBackup, fmt.Errorf("health check failed: %w", err))
	}

	// Успешный запуск и прохождение верификации — удаляем бэкап
	if hasBackup {
		_ = os.Remove(backupPath)
	}
	return nil
}

func triggerRollback(ctx context.Context, eng Engine, targetPath, backupPath string, hasBackup bool, originalErr error) error {
	log.Printf("[engine-reload] Initiating ROLLBACK due to: %v", originalErr)

	if !hasBackup {
		return fmt.Errorf("%w; rollback impossible: no previous backup exists", originalErr)
	}

	if _, statErr := os.Stat(backupPath); statErr != nil {
		return fmt.Errorf("%w; rollback failed: backup file missing: %v", originalErr, statErr)
	}

	// Восстанавливаем предыдущий проверенный конфиг поверх сбойного
	if err := os.Rename(backupPath, targetPath); err != nil {
		return fmt.Errorf("%w; rollback failed to restore file: %v", originalErr, err)
	}

	// Поднимаем стабильную версию
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

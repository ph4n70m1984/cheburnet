package engine

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"cheburnet/internal/config"
)

// SafeReload атомарно генерирует новый конфиг, валидирует его силами ядра,
// перезапускает процесс, проводит проверку здоровья и выполняет автоматический откат при сбое.
func SafeReload(ctx context.Context, eng Engine, cfg *config.CheburConfig, targetPath string) error {
	stagingPath := targetPath + ".new"
	backupPath := targetPath + ".bak"

	defer os.Remove(stagingPath)

	// 1. Pre-flight: сборка конфигурации во временный файл
	if err := eng.BuildConfig(cfg, stagingPath); err != nil {
		return fmt.Errorf("build config failed for %s: %w (active process untouched)", eng.Name(), err)
	}

	// 2. Валидация бинарником (sing-box check или xray -test)
	if err := eng.ValidateConfig(stagingPath); err != nil {
		return fmt.Errorf("binary validation failed for %s: %w (active process untouched)", eng.Name(), err)
	}

	// 3. Резервная копия текущего рабочего файла (если он существует)
	if _, err := os.Stat(targetPath); err == nil {
		if err := copyFile(targetPath, backupPath); err != nil {
			log.Printf("[engine-reload] warning: failed to create backup config: %v", err)
		}
	}

	// 4. Атомарная замена рабочего файла валидированным
	if err := os.Rename(stagingPath, targetPath); err != nil {
		return fmt.Errorf("failed to commit staging config: %w", err)
	}

	// 5. Остановка текущего процесса ядра
	if err := eng.Stop(); err != nil {
		log.Printf("[engine-reload] warning: stop returned error: %v", err)
	}

	// 6. Запуск обновленного ядра
	if err := eng.Start(ctx, targetPath); err != nil {
		return triggerRollback(ctx, eng, targetPath, backupPath, fmt.Errorf("engine start failed: %w", err))
	}

	// 7. Пост-старт верификация локальных портов (даем процессу 1 секунду на bind сокетов)
	time.Sleep(1 * time.Second)

	healthCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if err := VerifyEngineAlive(healthCtx, cfg); err != nil {
		log.Printf("[engine-reload] CRITICAL: Engine %s started but health check failed: %v", eng.Name(), err)
		return triggerRollback(ctx, eng, targetPath, backupPath, fmt.Errorf("health check failed: %w", err))
	}

	// Успешный запуск и прохождение проверки — очищаем бэкап
	_ = os.Remove(backupPath)
	return nil
}

func triggerRollback(ctx context.Context, eng Engine, targetPath, backupPath string, originalErr error) error {
	log.Printf("[engine-reload] Initiating ROLLBACK due to: %v", originalErr)

	if _, statErr := os.Stat(backupPath); statErr == nil {
		_ = os.Rename(backupPath, targetPath)
		if rbErr := eng.Start(ctx, targetPath); rbErr != nil {
			log.Printf("[engine-reload] FATAL: rollback start failed: %v", rbErr)
			return fmt.Errorf("%w; rollback also failed: %v", originalErr, rbErr)
		}
		log.Printf("[engine-reload] Rollback successful: %s restored from backup config", eng.Name())
	}

	return fmt.Errorf("rolled back to previous config: %w", originalErr)
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

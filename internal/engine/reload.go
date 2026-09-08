package engine

import (
	"context"
	"fmt"
	"log"
	"os"

	"cheburnet/internal/config"
)

// SafeReload атомарно генерирует новый конфиг, валидирует его и перезапускает ядро с автоматическим откатом
func SafeReload(ctx context.Context, eng Engine, cfg *config.CheburConfig, targetPath string) error {
	stagingPath := targetPath + ".new"
	backupPath := targetPath + ".bak"

	defer os.Remove(stagingPath)

	// 1. Pre-flight: генерируем конфигурацию в staging-файл
	if err := eng.BuildConfig(cfg, stagingPath); err != nil {
		return fmt.Errorf("pre-flight build config failed for %s: %w (engine kept untouched)", eng.Name(), err)
	}

	// 2. Бэкапим текущий рабочий конфиг (если он существует)
	if _, err := os.Stat(targetPath); err == nil {
		if err := copyFile(targetPath, backupPath); err != nil {
			log.Printf("[engine-reload] warning: failed to create backup config: %v", err)
		}
	}

	// 3. Атомарно активируем новый конфигурационный файл
	if err := os.Rename(stagingPath, targetPath); err != nil {
		return fmt.Errorf("failed to commit staging config: %w", err)
	}

	// 4. Останавливаем текущий процесс ядра
	if err := eng.Stop(); err != nil {
		log.Printf("[engine-reload] warning: stop returned error: %v", err)
	}

	// 5. Запускаем ядро с новым конфигом
	if err := eng.Start(ctx, targetPath); err != nil {
		log.Printf("[engine-reload] CRITICAL: %s failed to start with new config: %v. Triggering ROLLBACK...", eng.Name(), err)

		// 6. ROLLBACK: возвращаем бэкап и поднимаем стабильную версию
		if _, statErr := os.Stat(backupPath); statErr == nil {
			_ = os.Rename(backupPath, targetPath)
			if rbErr := eng.Start(ctx, targetPath); rbErr != nil {
				log.Printf("[engine-reload] FATAL: rollback start failed: %v", rbErr)
				return fmt.Errorf("engine start failed: %w; rollback also failed: %v", err, rbErr)
			}
			log.Printf("[engine-reload] Rollback successful: %s restored from backup config", eng.Name())
		}

		return fmt.Errorf("engine %s failed to start, rolled back to previous config: %w", eng.Name(), err)
	}

	// Успешный запуск — удаляем временный бэкап
	_ = os.Remove(backupPath)
	return nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

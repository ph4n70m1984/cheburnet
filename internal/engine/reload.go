package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cheburnet/internal/config"
)

var (
	// ErrReloadInProgress возвращается при попытке параллельного запуска перезагрузки ядра,
	// если предыдущая операция еще не завершилась и истек таймаут ожидания шлюза.
	ErrReloadInProgress = errors.New("engine reload is already in progress")

	// gatesMu и gates обеспечивают независимую сериализацию перезагрузки для каждого движка.
	// Набор движков в демоне фиксирован, поэтому утечки памяти в таблице не происходит.
	gatesMu sync.Mutex
	gates   = make(map[string]chan struct{})
)

func getGate(name string) chan struct{} {
	gatesMu.Lock()
	defer gatesMu.Unlock()
	g, ok := gates[name]
	if !ok {
		g = make(chan struct{}, 1)
		g <- struct{}{}
		gates[name] = g
	}
	return g
}

// SafeReload атомарно генерирует новый конфиг, валидирует его силами ядра, проверяет на идентичность,
// применяет без простоя при отсутствии изменений, а при их наличии выполняет быстрый рестарт
// с активным опросом готовности портов и откатом на резервную копию при сбое.
func SafeReload(ctx context.Context, eng Engine, cfg *config.CheburConfig, targetPath string) error {
	gate := getGate(eng.Name())

	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()

	select {
	case <-gate:
		defer func() { gate <- struct{}{} }()
	case <-ctx.Done():
		return fmt.Errorf("reload aborted by context: %w", ctx.Err())
	case <-timer.C:
		log.Printf("[engine-reload] Rejecting concurrent reload request for %s: lock wait timeout", eng.Name())
		return ErrReloadInProgress
	}

	stagingPath := strings.TrimSuffix(targetPath, ".json") + ".new.json"
	backupPath := strings.TrimSuffix(targetPath, ".json") + ".bak.json"

	// Страховка от зависших временных файлов при ранних return / panic
	defer os.Remove(stagingPath)

	// 1. Pre-flight: сборка конфигурации во временный staging-файл
	if err := eng.BuildConfig(cfg, stagingPath); err != nil {
		return fmt.Errorf("build config failed for %s: %w (active process untouched)", eng.Name(), err)
	}

	// 2. Валидация синтаксиса и схемы бинарником ядра
	if err := eng.ValidateConfig(ctx, stagingPath); err != nil {
		return fmt.Errorf("binary validation failed for %s: %w (active process untouched)", eng.Name(), err)
	}

	// 3. No-op проверка: если скомпилированный конфиг байт-в-байт совпадает с активным, рестарт пропускается
	if isConfigIdentical(stagingPath, targetPath) {
		log.Printf("[engine-reload] Generated config is identical to active config (%s). Skipping process restart.", targetPath)
		return nil
	}

	// 4. Создание атомарного бэкапа текущего рабочего конфига
	hasBackup := false
	if _, err := os.Stat(targetPath); err == nil {
		if err := copyFileAtomic(targetPath, backupPath); err != nil {
			return fmt.Errorf("aborting reload: failed to create safety backup: %w (active process untouched)", err)
		}
		hasBackup = true
	}

	// 5. Принудительный сброс данных и метаданных staging-файла на физический накопитель
	if err := syncFile(stagingPath); err != nil {
		return fmt.Errorf("failed to fsync staging config: %w", err)
	}

	// 6. Атомарная замена рабочего файла проверенной конфигурацией
	if err := os.Rename(stagingPath, targetPath); err != nil {
		return fmt.Errorf("failed to commit staging config: %w", err)
	}
	if err := syncDir(filepath.Dir(targetPath)); err != nil {
		log.Printf("[engine-reload] warning: dir sync failed for %s: %v", targetPath, err)
	}

	// 7. Остановка текущего процесса ядра
	if err := eng.Stop(); err != nil {
		log.Printf("[engine-reload] warning: stop returned error for %s: %v", eng.Name(), err)
	}

	// Амортизационная пауза: даем сетевому стеку ядра ОС завершить очистку сокетов
	time.Sleep(50 * time.Millisecond)

	// 8. Запуск обновленного ядра с долгоживущим context.Background(),
	// чтобы отмена родительского reloadCtx (например, таймаут HTTP-клиента) не убила работающий сервис
	startCtx := context.Background()
	if err := eng.Start(startCtx, targetPath); err != nil {
		return triggerRollback(eng, targetPath, backupPath, hasBackup, fmt.Errorf("engine start failed: %w", err))
	}

	// 9. Активный readiness polling локальных портов
	if err := waitForEngineReady(ctx, cfg, 5*time.Second); err != nil {
		// Дифференцируем отмену контекста демона (SIGTERM / graceful shutdown) от сбоя готовности портов
		if ctx.Err() != nil {
			log.Printf("[engine-reload] WARNING: reload aborted by parent context after engine start; "+
				"engine %s is running with unverified health, rollback skipped: %v", eng.Name(), ctx.Err())
			return fmt.Errorf("reload aborted: %w", ctx.Err())
		}

		log.Printf("[engine-reload] CRITICAL: Engine %s started but readiness probe failed: %v", eng.Name(), err)
		return triggerRollback(eng, targetPath, backupPath, hasBackup, fmt.Errorf("readiness check failed: %w", err))
	}

	log.Printf("[engine-reload] Engine %s successfully reloaded and healthy", eng.Name())
	return nil
}

// waitForEngineReady циклически опрашивает сокеты ядра с интервалом 100 мс
func waitForEngineReady(ctx context.Context, cfg *config.CheburConfig, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var lastErr error
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			probeCtx, cancel := context.WithTimeout(ctx, 350*time.Millisecond)
			err := VerifyEngineAlive(probeCtx, cfg)
			cancel()

			if err == nil {
				return nil
			}
			lastErr = err

			if time.Now().After(deadline) {
				return fmt.Errorf("timeout waiting for engine ports: %w", lastErr)
			}
		}
	}
}

// isConfigIdentical сравнивает конфигурации: сначала размер, затем байты
func isConfigIdentical(pathA, pathB string) bool {
	statA, errA := os.Stat(pathA)
	statB, errB := os.Stat(pathB)
	if errA == nil && errB == nil && statA.Size() != statB.Size() {
		return false
	}

	dataA, errA := os.ReadFile(pathA)
	dataB, errB := os.ReadFile(pathB)
	if errA != nil || errB != nil {
		return false
	}

	return bytes.Equal(dataA, dataB)
}

func triggerRollback(eng Engine, targetPath, backupPath string, hasBackup bool, originalErr error) error {
	log.Printf("[engine-reload] Initiating ROLLBACK due to: %v", originalErr)

	if stopErr := eng.Stop(); stopErr != nil {
		log.Printf("[engine-reload] warning: stop failed during rollback cleanup: %v", stopErr)
	}
	time.Sleep(50 * time.Millisecond)

	if !hasBackup {
		return fmt.Errorf("%w; rollback impossible: no previous backup exists", originalErr)
	}

	if _, statErr := os.Stat(backupPath); statErr != nil {
		return fmt.Errorf("%w; rollback failed: backup file missing: %v", originalErr, statErr)
	}

	if err := copyFileAtomic(backupPath, targetPath); err != nil {
		return fmt.Errorf("%w; rollback failed to restore file: %v", originalErr, err)
	}

	startCtx := context.Background()
	if rbErr := eng.Start(startCtx, targetPath); rbErr != nil {
		log.Printf("[engine-reload] FATAL: rollback start failed: %v", rbErr)
		return fmt.Errorf("%w; rollback start also failed: %v", originalErr, rbErr)
	}

	log.Printf("[engine-reload] Rollback successful: %s restored from backup config", eng.Name())
	return fmt.Errorf("rolled back to previous config: %w", originalErr)
}

// copyFileAtomic гарантирует атомарную запись, строгое сохранение прав доступа и fsync
func copyFileAtomic(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source is not a regular file: %s", src)
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	dir := filepath.Dir(dst)
	tmpFile, err := os.CreateTemp(dir, filepath.Base(dst)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmpFile.Name()

	closed := false
	defer func() {
		if !closed {
			_ = tmpFile.Close()
		}
		_ = os.Remove(tmpName)
	}()

	if _, err := io.Copy(tmpFile, in); err != nil {
		return err
	}

	// 1. Применяем права доступа к метаданным inode
	if err := tmpFile.Chmod(info.Mode().Perm()); err != nil {
		return err
	}

	// 2. Сбрасываем данные и метаданные на физический носитель
	if err := tmpFile.Sync(); err != nil {
		return err
	}

	// 3. Закрываем дескриптор перед переименованием
	if err := tmpFile.Close(); err != nil {
		return err
	}
	closed = true

	// 4. Атомарная замена файла и синхронизация родительской директории
	if err := os.Rename(tmpName, dst); err != nil {
		return err
	}

	if err := syncDir(dir); err != nil {
		log.Printf("[engine-reload] warning: dir sync failed for %s: %v", dir, err)
	}

	return nil
}

func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		log.Printf("[engine-reload] notice: open file RDWR failed (%v), falling back to RDONLY for sync: %s", err, path)
		f, err = os.Open(path)
		if err != nil {
			return err
		}
	}
	defer f.Close()
	return f.Sync()
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

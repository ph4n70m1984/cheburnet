//go:build metrics

package api

import (
	"github.com/ansrivas/fiberprometheus/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func setupMetrics(app *fiber.App) {
	// 1. Регистрируем сборщик процессора, памяти и сети хоста
	sysCollector := NewSystemCollector()
	_ = prometheus.DefaultRegisterer.Register(sysCollector)

	// 2. Регистрируем сборщик метрик ядра sing-box (CPU, RSS, трафик, соединения)
	singboxCollector := NewSingboxCollector()
	_ = prometheus.DefaultRegisterer.Register(singboxCollector)

	// 3. Инициализируем метрики HTTP для Fiber
	prometheusExporter := fiberprometheus.New("cheburnetd")
	app.Use(prometheusExporter.Middleware)

	// 4. Отдаем объединенный реестр (Go runtime + Fiber + System + Singbox) через promhttp
	app.Get("/metrics", adaptor.HTTPHandler(promhttp.Handler()))
}

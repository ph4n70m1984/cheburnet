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
	// 1. Регистрируем сборщик процессора, памяти и сети в глобальном реестре
	sysCollector := NewSystemCollector()
	_ = prometheus.DefaultRegisterer.Register(sysCollector)

	// 2. Инициализируем метрики HTTP для Fiber
	prometheusExporter := fiberprometheus.New("cheburnetd")
	app.Use(prometheusExporter.Middleware)

	// 3. Отдаем объединенный реестр (Go runtime + Fiber + SystemCollector) через promhttp
	app.Get("/metrics", adaptor.HTTPHandler(promhttp.Handler()))
}

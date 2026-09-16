//go:build !metrics

package api

import (
	"github.com/gofiber/fiber/v2"
)

func setupMetrics(app *fiber.App) {
	app.Get("/metrics", func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "Prometheus metrics are disabled in this build. Rebuild with -tags metrics.",
		})
	})
}

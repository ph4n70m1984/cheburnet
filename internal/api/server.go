package api

import (
	"cheburnet/internal/config"
	"cheburnet/internal/engine"
	"cheburnet/internal/subscription"
	"cheburnet/internal/telemetry"
	"cheburnet/internal/updater"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/websocket/v2"
)

type Server struct {
	app        *fiber.App
	state      *config.StateManager
	hub        *telemetry.Hub
	subWorker  *subscription.Worker
	updater    *updater.Manager
	getEngine  func() engine.Engine
	swapEngine func(name string) error
}

func NewServer(
	state *config.StateManager,
	hub *telemetry.Hub,
	subWorker *subscription.Worker,
	upd *updater.Manager,
	getEngine func() engine.Engine,
	swapEngine func(name string) error,
) *Server {
	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
		AppName:               "Chebur.NET Daemon",
	})

	app.Use(cors.New(cors.Config{
		AllowOrigins: "*",
		AllowHeaders: "Origin, Content-Type, Accept",
	}))

	s := &Server{
		app:        app,
		state:      state,
		hub:        hub,
		subWorker:  subWorker,
		updater:    upd,
		getEngine:  getEngine,
		swapEngine: swapEngine,
	}

	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	api := s.app.Group("/api/v1")

	// Системные эндпоинты
	api.Get("/status", s.handleStatus)
	api.Post("/engine/switch", s.handleSwitchEngine)
	api.Post("/reload", s.handleReloadConfig)
	api.Get("/updates/check", s.handleCheckUpdates)
	api.Post("/updates/upgrade", s.handlePerformUpdate)

	// Ноды, подписки и источники
	api.Get("/nodes", s.handleGetNodes)
	api.Post("/nodes/add", s.handleAddNode)
	api.Post("/subscriptions/update", s.handleUpdateSubscriptions)
	api.Post("/sources/add", s.handleAddSource)

	// WebSocket телеметрия
	s.app.Use("/ws", func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	})

	s.app.Get("/ws/telemetry", websocket.New(func(c *websocket.Conn) {
		s.hub.Register(c)
		defer s.hub.Unregister(c)

		for {
			if _, _, err := c.ReadMessage(); err != nil {
				break
			}
		}
	}))
}

func (s *Server) Listen(addr string) error {
	return s.app.Listen(addr)
}

func (s *Server) Shutdown() error {
	return s.app.Shutdown()
}

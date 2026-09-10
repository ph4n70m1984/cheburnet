package api

import (
	"context"
	"log"
	"os/exec"
	"strings"
	"time"

	"cheburnet/internal/config"
	"cheburnet/internal/diagnostics"
	"cheburnet/internal/engine"
	"cheburnet/internal/network"
	"cheburnet/internal/subscription"
	"cheburnet/internal/telemetry"
	"cheburnet/internal/updater"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/websocket/v2"
)

type ActionCallback func(action string) error

type Server struct {
	app        *fiber.App
	state      *config.StateManager
	hub        *telemetry.Hub
	subWorker  *subscription.Worker
	updater    *updater.Manager
	getEngine  func() engine.Engine
	swapEngine func(name string) error
	rulesCron  *network.RulesetCron
	diagEngine *diagnostics.DiagnosticsEngine
	onAction   ActionCallback
}

func NewServer(
	state *config.StateManager,
	hub *telemetry.Hub,
	subWorker *subscription.Worker,
	upd *updater.Manager,
	getEngine func() engine.Engine,
	swapEngine func(name string) error,
	rulesCron *network.RulesetCron,
	diagEngine *diagnostics.DiagnosticsEngine,
	onAction ActionCallback,
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
		rulesCron:  rulesCron,
		diagEngine: diagEngine,
		onAction:   onAction,
	}

	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	api := s.app.Group("/api/v1")

	api.Get("/status", s.handleStatus)

	// Всеядный обработчик переключения: читает и engine, и name
	api.Post("/engine/switch", func(c *fiber.Ctx) error {
		var req struct {
			Engine string `json:"engine"`
			Name   string `json:"name"`
		}

		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "invalid request payload",
			})
		}

		target := strings.ToLower(strings.TrimSpace(req.Engine))
		if target == "" {
			target = strings.ToLower(strings.TrimSpace(req.Name))
		}

		if target != "sing-box" && target != "xray" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "engine must be sing-box or xray",
			})
		}

		if err := s.swapEngine(target); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": err.Error(),
			})
		}

		return c.JSON(fiber.Map{
			"status":        "ok",
			"active_engine": target,
		})
	})

	api.Post("/reload", s.handleReloadConfig)
	api.Get("/updates/check", s.handleCheckUpdates)
	api.Post("/updates/upgrade", s.handlePerformUpdate)

	api.Get("/diagnostics", func(c *fiber.Ctx) error {
		if s.diagEngine != nil {
			return c.JSON(s.diagEngine.Snapshot())
		}
		return c.JSON(fiber.Map{
			"healthy":  true,
			"problems": []interface{}{},
		})
	})

	api.Post("/actions/:action", func(c *fiber.Ctx) error {
		action := c.Params("action")
		if s.onAction != nil {
			if err := s.onAction(action); err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
			}
		} else {
			if action == "reload_firewall" {
				_ = exec.Command("fw4", "reload").Run()
			}
		}
		return c.JSON(fiber.Map{"status": "ok"})
	})

	api.Get("/nodes", s.handleGetNodes)
	api.Post("/nodes", s.handleAddNode)
	api.Post("/nodes/add", s.handleAddNode)
	api.Post("/source", s.handleAddSource)
	api.Post("/sources/add", s.handleAddSource)
	api.Post("/subscriptions/update", s.handleUpdateSubscriptions)

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
			var msg struct {
				Action string `json:"action"`
				Target string `json:"target"`
			}

			if err := c.ReadJSON(&msg); err != nil {
				break
			}

			switch msg.Action {
			case "check_updates":
				go func(conn *websocket.Conn) {
					cfg := s.state.Get()
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					defer cancel()

					report, err := s.updater.CheckUpdates(ctx, cfg.AutoUpdate)
					if err == nil {
						_ = conn.WriteJSON(map[string]interface{}{
							"type": "update_report",
							"data": report,
						})
					}
				}(c)

			case "perform_upgrade":
				target := msg.Target
				if target == "" {
					target = "all"
				}

				go func(tgt string) {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
					defer cancel()

					if err := s.updater.PerformUpgrade(ctx, tgt); err != nil {
						log.Printf("[ERROR] WebSocket triggered upgrade failed: %v", err)
						return
					}

					if tgt == "cheburnet" || tgt == "all" {
						time.Sleep(1 * time.Second)
						_ = exec.Command("/etc/init.d/cheburnet", "restart").Run()
					}
				}(target)
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

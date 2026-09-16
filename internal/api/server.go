package api

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"time"

	"cheburnet/internal/config"
	"cheburnet/internal/diagnostics"
	"cheburnet/internal/engine"
	"cheburnet/internal/network"
	"cheburnet/internal/subscription"
	"cheburnet/internal/telemetry"
	"cheburnet/internal/updater"

	"github.com/ansrivas/fiberprometheus/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/websocket/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type ActionCallback func(action string) error

type Server struct {
	app         *fiber.App
	state       *config.StateManager
	hub         *telemetry.Hub
	subWorker   *subscription.Worker
	updater     *updater.Manager
	getEngine   func() engine.Engine
	rulesCron   *network.RulesetCron
	diagEngine  *diagnostics.DiagnosticsEngine
	onAction    ActionCallback
	ipifyClient *http.Client
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

	proxyURL, _ := url.Parse("http://127.0.0.1:4534")

	s := &Server{
		app:        app,
		state:      state,
		hub:        hub,
		subWorker:  subWorker,
		updater:    upd,
		getEngine:  getEngine,
		rulesCron:  rulesCron,
		diagEngine: diagEngine,
		onAction:   onAction,
		ipifyClient: &http.Client{
			Transport: &http.Transport{
				Proxy:             http.ProxyURL(proxyURL),
				DisableKeepAlives: true,
				ForceAttemptHTTP2: false,
			},
			Timeout: 2 * time.Second,
		},
	}

	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	// 1. Регистрируем сборщик процессора, памяти и сети в глобальном реестре
	sysCollector := NewSystemCollector()
	_ = prometheus.DefaultRegisterer.Register(sysCollector)

	// 2. Инициализируем метрики HTTP для Fiber
	prometheusExporter := fiberprometheus.New("cheburnetd")
	s.app.Use(prometheusExporter.Middleware)

	// 3. Отдаем объединенный реестр (Go runtime + Fiber + SystemCollector) через promhttp
	s.app.Get("/metrics", adaptor.HTTPHandler(promhttp.Handler()))

	api := s.app.Group("/api/v1")

	api.Get("/status", s.handleStatus)

	api.Post("/engine/switch", func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "engine switching is disabled: sing-box is the dedicated core",
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
				fwCtx, fwCancel := context.WithTimeout(c.Context(), 10*time.Second)
				defer fwCancel()
				_ = exec.CommandContext(fwCtx, "fw4", "reload").Run()
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
	api.Get("/proxies/:name/delay", s.handleProxyDelay)

	s.app.Use("/ws", func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	})

	s.app.Get("/ws/telemetry", websocket.New(func(c *websocket.Conn) {
		s.hub.Register(c)
		defer s.hub.Unregister(c)

		c.SetReadLimit(4096)
		_ = c.SetReadDeadline(time.Now().Add(60 * time.Second))
		c.SetPongHandler(func(string) error {
			_ = c.SetReadDeadline(time.Now().Add(60 * time.Second))
			return nil
		})

		for {
			var msg struct {
				Action string `json:"action"`
				Target string `json:"target"`
			}

			if err := c.ReadJSON(&msg); err != nil {
				break
			}
			_ = c.SetReadDeadline(time.Now().Add(60 * time.Second))

			switch msg.Action {
			case "check_updates":
				go func(conn *websocket.Conn) {
					cfg := s.state.Get()
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					defer cancel()

					report, err := s.updater.CheckUpdates(ctx, cfg.AutoUpdate)
					if err == nil {
						_ = s.hub.SendJSON(conn, map[string]interface{}{
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
						restartCtx, restartCancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer restartCancel()
						_ = exec.CommandContext(restartCtx, "/etc/init.d/cheburnet", "restart").Run()
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

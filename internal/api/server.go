package api

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"cheburnet/internal/config"
	"cheburnet/internal/diagnostics"
	"cheburnet/internal/engine"
	"cheburnet/internal/network"
	"cheburnet/internal/service"
	"cheburnet/internal/subscription"
	"cheburnet/internal/telemetry"
	"cheburnet/internal/updater"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/websocket/v2"
)

type ActionCallback func(action string) error

type Server struct {
	app         *fiber.App
	pubSub      *publicSubController
	state       *config.StateManager
	hub         *telemetry.Hub
	subWorker   *subscription.Worker
	updater     *updater.Manager
	getEngine   func() engine.Engine
	rulesCron   *network.RulesetCron
	diagEngine  *diagnostics.DiagnosticsEngine
	onAction    ActionCallback
	ipifyClient *http.Client
	clashClient *http.Client
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
		AppName:               "Chebur.NET Internal Daemon",
	})

	proxyURL, _ := url.Parse("http://127.0.0.1:4534")

	s := &Server{
		app:        app,
		pubSub:     newPublicSubController(),
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
		clashClient: &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 5,
				IdleConnTimeout:     60 * time.Second,
				DisableKeepAlives:   false,
			},
			Timeout: 4 * time.Second,
		},
	}

	s.setupRoutes()
	return s
}

// authRequired проверяет наличие и валидность api_token для мутирующих запросов
func (s *Server) authRequired() fiber.Handler {
	return func(c *fiber.Ctx) error {
		cfg := s.state.Get()
		expectedToken := strings.TrimSpace(cfg.APIToken)

		if expectedToken == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "api token is not configured on daemon",
			})
		}

		// 1. Проверка заголовка Authorization: Bearer <token>
		authHeader := c.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
			if token == expectedToken {
				return c.Next()
			}
		}

		// 2. Проверка кастомного заголовка X-API-Token
		if c.Get("X-API-Token") == expectedToken {
			return c.Next()
		}

		// 3. Проверка query-параметра ?token= (необходимо для WebSocket)
		if c.Query("token") == expectedToken {
			return c.Next()
		}

		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "unauthorized: valid api token is required",
		})
	}
}

func (s *Server) setupRoutes() {
	setupMetrics(s.app)

	// Защита от DNS Rebinding и CSRF: ограничиваем Origin локальной сетью
	s.app.Use(cors.New(cors.Config{
		AllowOriginsFunc: func(origin string) bool {
			if origin == "" {
				return true
			}
			u, err := url.Parse(origin)
			if err != nil {
				return false
			}
			hostname := u.Hostname()
			return hostname == "localhost" || hostname == "127.0.0.1" ||
				strings.HasPrefix(hostname, "192.168.") ||
				strings.HasPrefix(hostname, "10.") ||
				strings.HasPrefix(hostname, "172.16.") ||
				strings.HasSuffix(hostname, ".lan")
		},
		AllowHeaders: "Origin, Content-Type, Accept, Authorization, X-API-Token",
		AllowMethods: "GET, POST, PUT, DELETE, OPTIONS",
	}))

	api := s.app.Group("/api/v1")

	// --- ОТКРЫТЫЕ READ-ONLY ЭНДПОИНТЫ ДЛЯ СИСТЕМЫ МОНИТОРИНГА И СТАТУСА ---
	api.Get("/status", s.handleStatus)
	api.Get("/diagnostics", func(c *fiber.Ctx) error {
		if s.diagEngine != nil {
			return c.JSON(s.diagEngine.Snapshot())
		}
		return c.JSON(fiber.Map{
			"healthy":  true,
			"problems": []interface{}{},
		})
	})

	// --- ЗАЩИЩЕННЫЕ ЭНДПОИНТЫ УПРАВЛЕНИЯ, МУТАЦИЙ И ОБНОВЛЕНИЯ ---
	auth := s.authRequired()

	api.Post("/engine/switch", auth, func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "engine switching is disabled: sing-box is the dedicated core",
		})
	})

	api.Post("/reload", auth, s.handleReloadConfig)
	api.Get("/updates/check", auth, s.handleCheckUpdates)
	api.Post("/updates/upgrade", auth, s.handlePerformUpdate)

	api.Post("/actions/:action", auth, func(c *fiber.Ctx) error {
		action := c.Params("action")
		if action == "restart_service" {
			log.Println("[INFO] Restart requested via API, delegating to procd ubus...")
			if err := service.RestartAsync(); err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
			}
			return c.JSON(fiber.Map{"status": "restarting"})
		}

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

	api.Get("/nodes", auth, s.handleGetNodes)
	api.Post("/nodes", auth, s.handleAddNode)
	api.Post("/nodes/add", auth, s.handleAddNode)
	api.Post("/nodes/select", auth, s.handleSelectNode)
	api.Post("/source", auth, s.handleAddSource)
	api.Post("/sources/add", auth, s.handleAddSource)
	api.Post("/subscriptions/update", auth, s.handleUpdateSubscriptions)
	api.Get("/proxies/:name/delay", auth, s.handleProxyDelay)

	// --- ЗАЩИЩЕННЫЙ WEBSOCKET КАНАЛ ТЕЛЕМЕТРИИ ---
	s.app.Use("/ws", auth, func(c *fiber.Ctx) error {
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
					if s.updater != nil && cfg.UpdateChannel != "" {
						s.updater.SetUpdateChannel(cfg.UpdateChannel)
					}

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
						log.Printf("[INFO] Restarting daemon via procd ubus after upgrade...")
						_ = service.RestartAsync()
					}
				}(target)
			}
		}
	}))
}

func (s *Server) Listen(addr string) error {
	cfg := s.state.Get()
	if cfg.PublicSubEnabled {
		pubPort := cfg.PublicSubPort
		if pubPort == 0 {
			pubPort = 9443
		}
		s.pubSub.Start(s, pubPort)
	}

	return s.app.Listen(addr)
}

func (s *Server) Shutdown() error {
	s.pubSub.Stop()
	return s.app.Shutdown()
}

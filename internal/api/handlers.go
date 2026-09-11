package api

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"cheburnet/internal/config"
	"cheburnet/internal/engine"
	"cheburnet/pkg/uri"

	"github.com/gofiber/fiber/v2"
)

// handleStatus возвращает текущий статус ядра, количество нод, внешний IP и активную ноду
func (s *Server) handleStatus(c *fiber.Ctx) error {
	cfg := s.state.Get()

	outboundIP := "Офлайн"
	proxyURL, _ := url.Parse("http://127.0.0.1:4534")
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   2 * time.Second,
	}
	if resp, err := client.Get("https://api.ipify.org"); err == nil {
		if b, err := io.ReadAll(resp.Body); err == nil {
			outboundIP = strings.TrimSpace(string(b))
		}
		resp.Body.Close()
	}

	activeNode := ""
	if len(cfg.Nodes) > 0 {
		activeNode = cfg.Nodes[0].Tag
	}

	return c.JSON(fiber.Map{
		"engine":      cfg.Engine,
		"nodes_count": len(cfg.Nodes),
		"active_node": activeNode,
		"auto_hwid":   cfg.AutoHWID,
		"custom_hwid": cfg.CustomHWID,
		"outbound_ip": outboundIP,
	})
}

// handleSwitchEngine переключает ядро между sing-box и xray
func (s *Server) handleSwitchEngine(c *fiber.Ctx) error {
	var req struct {
		Engine string `json:"engine"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	if req.Engine != "sing-box" && req.Engine != "xray" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "engine must be sing-box or xray"})
	}

	if err := s.swapEngine(req.Engine); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	s.state.SetEngine(req.Engine)
	return c.JSON(fiber.Map{"status": "ok", "active_engine": req.Engine})
}

// handleGetNodes возвращает список всех текущих нод
func (s *Server) handleGetNodes(c *fiber.Ctx) error {
	return c.JSON(s.state.Get().Nodes)
}

// handleAddNode добавляет одиночную ссылку ноды (vless, hy2, trojan, ss, socks)
func (s *Server) handleAddNode(c *fiber.Ctx) error {
	var req struct {
		URI string `json:"uri"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	cfg := s.state.Get()
	node, err := uri.ParseNodeURI(req.URI, cfg.AutoHWID, cfg.CustomHWID)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	s.state.Update(func(c *config.CheburConfig) {
		c.Nodes = append(c.Nodes, node)
	})

	return c.JSON(fiber.Map{"status": "ok", "node": node})
}

// handleUpdateSubscriptions обновляет все сохраненные подписки
func (s *Server) handleUpdateSubscriptions(c *fiber.Ctx) error {
	uciStorage := config.NewUCIStorage()
	freshCfg, err := uciStorage.Load()
	if err == nil {
		s.state.Update(func(cfg *config.CheburConfig) {
			cfg.RuleSets = freshCfg.RuleSets
			cfg.Subscriptions = freshCfg.Subscriptions
			cfg.ManualNodes = freshCfg.ManualNodes
			cfg.RulesetUpdateInterval = freshCfg.RulesetUpdateInterval
		})

		if s.rulesCron != nil {
			s.rulesCron.UpdateRulesets(freshCfg.RuleSets)
			s.rulesCron.SetInterval(freshCfg.RulesetUpdateInterval)
		}
	}

	currentSnapshot := s.state.Get()
	var allNodes []*config.GenericNode

	// Подгружаем ручные ноды
	for _, raw := range currentSnapshot.ManualNodes {
		if node, err := uri.ParseNodeURI(raw, currentSnapshot.AutoHWID, currentSnapshot.CustomHWID); err == nil {
			allNodes = append(allNodes, node)
		}
	}

	// Подгружаем ноды из подписок
	for _, sub := range currentSnapshot.Subscriptions {
		if sub.URL == "" || !sub.Enabled {
			continue
		}
		nodes, err := s.subWorker.FetchNodes(c.Context(), sub)
		if err == nil {
			allNodes = append(allNodes, nodes...)
		}
	}

	// 1. Атомарно обновляем состояние и сразу получаем свежий изолированный снимок
	freshSnapshot := s.state.Update(func(cfg *config.CheburConfig) {
		cfg.Nodes = allNodes
	})

	// 2. Безопасный перезапуск sing-box / xray через SafeReload со свежим снапшотом
	eng := s.getEngine()
	targetPath := "/tmp/run/cheburnet/sing-box.json"
	if eng.Name() == "xray" {
		targetPath = "/tmp/run/cheburnet/xray.json"
	}

	if err := engine.SafeReload(c.Context(), eng, &freshSnapshot, targetPath); err != nil {
		log.Printf("[api] update subscriptions reload error: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to safely apply updated subscriptions: " + err.Error(),
		})
	}

	return c.JSON(fiber.Map{
		"status":         "ok",
		"total_nodes":    len(freshSnapshot.Nodes),
		"total_rulesets": len(freshSnapshot.RuleSets),
		"rulesets":       freshSnapshot.RuleSets,
	})
}

// handleAddSource универсально добавляет подписку или одиночную ноду с релоадом ядра
func (s *Server) handleAddSource(c *fiber.Ctx) error {
	var req struct {
		Type      string `json:"type"`
		URL       string `json:"url"`
		UserAgent string `json:"user_agent"`
		HWID      string `json:"hwid"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "url cannot be empty"})
	}

	cfg := s.state.Get()
	uci := config.NewUCIStorage()

	var freshSnapshot config.CheburConfig

	switch req.Type {
	case "subscription":
		subCfg := config.SubscriptionConfig{
			URL:       req.URL,
			UserAgent: req.UserAgent,
			HWID:      req.HWID,
			Enabled:   true,
		}
		if subCfg.UserAgent == "" {
			subCfg.UserAgent = "Happ/4.1.3 (iPhone; iOS 17.5.1; Scale/3.00)"
		}

		newNodes, err := s.subWorker.FetchNodes(c.Context(), subCfg)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "failed to fetch subscription: " + err.Error()})
		}
		if len(newNodes) == 0 {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "no valid nodes found in subscription"})
		}

		_ = uci.AddSubscription(subCfg)
		freshSnapshot = s.state.Update(func(c *config.CheburConfig) {
			c.Subscriptions = append(c.Subscriptions, subCfg)
			existingTags := make(map[string]bool)
			for _, n := range c.Nodes {
				existingTags[n.Tag] = true
			}
			for _, n := range newNodes {
				if !existingTags[n.Tag] {
					c.Nodes = append(c.Nodes, n)
					existingTags[n.Tag] = true
				}
			}
		})

	case "node":
		node, err := uri.ParseNodeURI(req.URL, cfg.AutoHWID, cfg.CustomHWID)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid node uri: " + err.Error()})
		}

		_ = uci.AddManualNode(req.URL)
		freshSnapshot = s.state.Update(func(c *config.CheburConfig) {
			c.Nodes = append(c.Nodes, node)
		})

	default:
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "type must be 'subscription' or 'node'"})
	}

	eng := s.getEngine()
	targetPath := "/tmp/run/cheburnet/sing-box.json"
	if eng.Name() == "xray" {
		targetPath = "/tmp/run/cheburnet/xray.json"
	}

	if err := engine.SafeReload(c.Context(), eng, &freshSnapshot, targetPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to safely reload engine with new source: " + err.Error(),
		})
	}

	return c.JSON(fiber.Map{
		"status":      "ok",
		"total_nodes": len(freshSnapshot.Nodes),
	})
}

// handleReloadConfig считывает актуальный UCI-файл и обновляет ноды
func (s *Server) handleReloadConfig(c *fiber.Ctx) error {
	_ = exec.Command("uci", "commit", "cheburnet").Run()

	uciStorage := config.NewUCIStorage()
	newCfg, err := uciStorage.Load()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to read UCI: " + err.Error(),
		})
	}

	var allNodes []*config.GenericNode

	// Подгружаем ручные ноды
	for _, raw := range newCfg.ManualNodes {
		if node, err := uri.ParseNodeURI(raw, newCfg.AutoHWID, newCfg.CustomHWID); err == nil {
			allNodes = append(allNodes, node)
		}
	}

	// Подгружаем активные подписки
	for _, sub := range newCfg.Subscriptions {
		if sub.URL == "" || !sub.Enabled {
			continue
		}
		subNodes, err := s.subWorker.FetchNodes(c.Context(), sub)
		if err == nil {
			allNodes = append(allNodes, subNodes...)
		}
	}

	newCfg.Nodes = allNodes

	// 1. Атомарно обновляем конфигурацию в памяти и возвращаем чистый снапшот
	freshSnapshot := s.state.Update(func(cfg *config.CheburConfig) {
		*cfg = *newCfg
	})

	// 2. Синхронизируем списки и интервал планировщика cron
	if s.rulesCron != nil {
		s.rulesCron.UpdateRulesets(freshSnapshot.RuleSets)
		s.rulesCron.SetInterval(freshSnapshot.RulesetUpdateInterval)
	}

	eng := s.getEngine()
	targetPath := "/tmp/run/cheburnet/sing-box.json"
	if eng.Name() == "xray" {
		targetPath = "/tmp/run/cheburnet/xray.json"
	}

	// 3. Передаем свежий снапшот в SafeReload
	if len(freshSnapshot.Nodes) > 0 {
		if err := engine.SafeReload(c.Context(), eng, &freshSnapshot, targetPath); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "Failed to safely reload engine: " + err.Error(),
			})
		}
	} else {
		_ = eng.Stop()
	}

	return c.JSON(fiber.Map{
		"status":  "ok",
		"message": "Configuration reloaded successfully",
		"nodes":   len(freshSnapshot.Nodes),
	})
}

func (s *Server) handleCheckUpdates(c *fiber.Ctx) error {
	cfg := s.state.Get()
	rep, err := s.updater.CheckUpdates(c.Context(), cfg.AutoUpdate)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(rep)
}

func (s *Server) handlePerformUpdate(c *fiber.Ctx) error {
	var body struct {
		Target string `json:"target"`
	}
	if err := c.BodyParser(&body); err != nil || body.Target == "" {
		body.Target = "all"
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		if err := s.updater.PerformUpgrade(ctx, body.Target); err != nil {
			log.Printf("[ERROR] Upgrade failed: %v", err)
			return
		}

		if body.Target == "cheburnet" || body.Target == "all" {
			log.Println("[INFO] Package upgrade completed. Restarting service via init script...")
			time.Sleep(1 * time.Second)
			_ = exec.Command("/etc/init.d/cheburnet", "restart").Run()
		}
	}()

	return c.JSON(fiber.Map{
		"status":  "in_progress",
		"message": "Update process started in background",
		"target":  body.Target,
	})
}

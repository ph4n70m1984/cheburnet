package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

const TargetConfigPath = "/tmp/run/cheburnet/sing-box.json"

type DelayResult struct {
	Delay   *int   `json:"delay"`
	Status  string `json:"status,omitempty"`
	Message string `json:"message,omitempty"`
}

// getRealActiveNode опрашивает sing-box Clash API для определения текущего активного аутбаунда
func getRealActiveNode(defaultTag string) string {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get("http://127.0.0.1:9090/proxies/PROXY")
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return defaultTag
	}
	defer resp.Body.Close()

	var result struct {
		Now string `json:"now"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err == nil && result.Now != "" {
		return result.Now
	}

	return defaultTag
}

// handleStatus возвращает текущий статус ядра, количество нод, внешний IP, активную ноду и флаги возможностей
func (s *Server) handleStatus(c *fiber.Ctx) error {
	cfg := s.state.Get()

	outboundIP := "Офлайн"

	reqCtx, cancel := context.WithTimeout(c.Context(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, "https://api.ipify.org", nil)
	if err == nil {
		if resp, err := s.ipifyClient.Do(req); err == nil {
			if b, err := io.ReadAll(io.LimitReader(resp.Body, 64)); err == nil {
				outboundIP = strings.TrimSpace(string(b))
			}
			_ = resp.Body.Close()
		}
	}

	activeNode := "auto"
	if len(cfg.Nodes) > 0 {
		activeNode = getRealActiveNode("auto")
	}

	return c.JSON(fiber.Map{
		"engine":      "sing-box",
		"nodes_count": len(cfg.Nodes),
		"active_node": activeNode,
		"auto_hwid":   cfg.AutoHWID,
		"custom_hwid": cfg.CustomHWID,
		"outbound_ip": outboundIP,
		"features": fiber.Map{
			"public_sub": HasPublicSubFeature,
		},
	})
}

// handleGetNodes возвращает список серверов вместе с виртуальной нодой auto, задержками и статусом
func (s *Server) handleGetNodes(c *fiber.Ctx) error {
	cfg := s.state.Get()
	activeNode := getRealActiveNode("auto")

	type nodeView struct {
		Tag      string `json:"tag"`
		Protocol string `json:"protocol"`
		Address  string `json:"address,omitempty"`
		Port     int    `json:"port,omitempty"`
		Active   bool   `json:"active"`
		Latency  int    `json:"latency"`
		Status   string `json:"status"`
	}

	latencies := make(map[string]int)
	bestLatency := 0

	client := &http.Client{Timeout: 600 * time.Millisecond}
	if resp, err := client.Get("http://127.0.0.1:9090/proxies"); err == nil && resp.StatusCode == http.StatusOK {
		var clashData struct {
			Proxies map[string]struct {
				History []struct {
					Delay int `json:"delay"`
				} `json:"history"`
				Now string `json:"now"`
			} `json:"proxies"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&clashData); err == nil {
			for tag, p := range clashData.Proxies {
				if len(p.History) > 0 {
					latencies[tag] = p.History[len(p.History)-1].Delay
				}
			}

			if autoGroup, ok := clashData.Proxies["auto"]; ok && autoGroup.Now != "" {
				bestLatency = latencies[autoGroup.Now]
			}
		}
		_ = resp.Body.Close()
	}

	if bestLatency == 0 {
		minDelay := 999999
		for _, d := range latencies {
			if d > 0 && d < minDelay {
				minDelay = d
			}
		}
		if minDelay < 999999 {
			bestLatency = minDelay
		}
	}

	var res []nodeView

	if len(cfg.Nodes) > 0 {
		autoStatus := "● Доступен"
		if bestLatency == 0 {
			autoStatus = "● Ожидание"
		}

		res = append(res, nodeView{
			Tag:      "auto",
			Protocol: "urltest",
			Active:   activeNode == "auto",
			Latency:  bestLatency,
			Status:   autoStatus,
		})
	}

	for _, n := range cfg.Nodes {
		d := latencies[n.Tag]
		status := "● Доступен"
		if d == 0 {
			status = "● Ожидание"
		}

		res = append(res, nodeView{
			Tag:      n.Tag,
			Protocol: n.Protocol,
			Address:  n.Address,
			Port:     n.Port,
			Active:   activeNode == n.Tag,
			Latency:  d,
			Status:   status,
		})
	}

	return c.JSON(res)
}

// handleSelectNode переключает ноду в sing-box на лету через Clash API
func (s *Server) handleSelectNode(c *fiber.Ctx) error {
	var req struct {
		Tag string `json:"tag"`
	}
	if err := c.BodyParser(&req); err != nil || req.Tag == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "tag is required"})
	}

	payload, _ := json.Marshal(map[string]string{
		"name": req.Tag,
	})

	putReq, err := http.NewRequestWithContext(c.Context(), http.MethodPut, "http://127.0.0.1:9090/proxies/PROXY", bytes.NewBuffer(payload))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	putReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(putReq)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": "failed to reach sing-box API: " + err.Error()})
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": fmt.Sprintf("sing-box returned code %d", resp.StatusCode)})
	}

	return c.JSON(fiber.Map{
		"status":      "ok",
		"active_node": req.Tag,
	})
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

	for _, raw := range currentSnapshot.ManualNodes {
		if node, err := uri.ParseNodeURI(raw, currentSnapshot.AutoHWID, currentSnapshot.CustomHWID); err == nil {
			allNodes = append(allNodes, node)
		}
	}

	for _, sub := range currentSnapshot.Subscriptions {
		if sub.URL == "" || !sub.Enabled {
			continue
		}
		nodes, err := s.subWorker.FetchNodes(c.Context(), sub)
		if err == nil {
			allNodes = append(allNodes, nodes...)
		}
	}

	freshSnapshot := s.state.Update(func(cfg *config.CheburConfig) {
		cfg.Nodes = allNodes
	})

	eng := s.getEngine()
	if err := engine.SafeReload(c.Context(), eng, &freshSnapshot, TargetConfigPath); err != nil {
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
	if err := engine.SafeReload(c.Context(), eng, &freshSnapshot, TargetConfigPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to safely reload engine with new source: " + err.Error(),
		})
	}

	return c.JSON(fiber.Map{
		"status":      "ok",
		"total_nodes": len(freshSnapshot.Nodes),
	})
}

// handleReloadConfig считывает актуальный UCI-файл и обновляет ноды с таймаутом выполнения
func (s *Server) handleReloadConfig(c *fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(c.Context(), 5*time.Second)
	defer cancel()

	if err := exec.CommandContext(ctx, "uci", "commit", "cheburnet").Run(); err != nil {
		log.Printf("[WARN] uci commit returned error: %v", err)
	}

	uciStorage := config.NewUCIStorage()
	newCfg, err := uciStorage.Load()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to read UCI: " + err.Error(),
		})
	}

	var allNodes []*config.GenericNode

	for _, raw := range newCfg.ManualNodes {
		if node, err := uri.ParseNodeURI(raw, newCfg.AutoHWID, newCfg.CustomHWID); err == nil {
			allNodes = append(allNodes, node)
		}
	}

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

	freshSnapshot := s.state.Update(func(cfg *config.CheburConfig) {
		*cfg = *newCfg
	})

	if s.rulesCron != nil {
		s.rulesCron.UpdateRulesets(freshSnapshot.RuleSets)
		s.rulesCron.SetInterval(freshSnapshot.RulesetUpdateInterval)
	}

	eng := s.getEngine()

	if len(freshSnapshot.Nodes) > 0 {
		if err := engine.SafeReload(c.Context(), eng, &freshSnapshot, TargetConfigPath); err != nil {
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
			restartCtx, restartCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer restartCancel()
			_ = exec.CommandContext(restartCtx, "/etc/init.d/cheburnet", "restart").Run()
		}
	}()

	return c.JSON(fiber.Map{
		"status":  "in_progress",
		"message": "Update process started in background",
		"target":  body.Target,
	})
}

// handleProxyDelay обрабатывает замер задержки для серверов и транслирует пинг активной ноды для auto
func (s *Server) handleProxyDelay(c *fiber.Ctx) error {
	name := c.Params("name")
	testURL := c.Query("url", "https://www.gstatic.com/generate_204")
	timeout := c.Query("timeout", "3000")

	if strings.EqualFold(name, "auto") {
		client := &http.Client{Timeout: 1 * time.Second}

		resp, err := client.Get("http://127.0.0.1:9090/proxies/auto")
		if err == nil && resp.StatusCode == http.StatusOK {
			var groupData struct {
				Now string `json:"now"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&groupData); err == nil && groupData.Now != "" {
				_ = resp.Body.Close()

				escapedTarget := url.PathEscape(groupData.Now)
				targetDelayURL := fmt.Sprintf("http://127.0.0.1:9090/proxies/%s/delay?url=%s&timeout=%s", escapedTarget, url.QueryEscape(testURL), timeout)

				if dResp, dErr := client.Get(targetDelayURL); dErr == nil {
					defer dResp.Body.Close()
					var res DelayResult
					if err := json.NewDecoder(dResp.Body).Decode(&res); err == nil {
						return c.JSON(res)
					}
				}
			} else {
				_ = resp.Body.Close()
			}
		}

		return c.JSON(DelayResult{
			Delay:  nil,
			Status: "unknown",
		})
	}

	escapedName := url.PathEscape(name)
	targetURL := fmt.Sprintf("http://127.0.0.1:9090/proxies/%s/delay?url=%s&timeout=%s", escapedName, url.QueryEscape(testURL), timeout)

	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(targetURL)
	if err != nil {
		return c.Status(fiber.StatusGatewayTimeout).JSON(DelayResult{
			Delay:   nil,
			Status:  "timeout",
			Message: "timeout",
		})
	}
	defer resp.Body.Close()

	var result DelayResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return c.Status(resp.StatusCode).SendString("error reading response")
	}

	return c.Status(resp.StatusCode).JSON(result)
}

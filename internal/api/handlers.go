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

// clashRequest выполняет HTTP-запрос к Clash API ядра с авторизацией через Bearer, если секрет задан
func (s *Server) clashRequest(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	targetURL := "http://127.0.0.1:9090" + path
	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, targetURL, bodyReader)
	if err != nil {
		return nil, err
	}

	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	cfg := s.state.Get()
	if secret := strings.TrimSpace(cfg.ClashAPISecret); secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}

	return s.clashClient.Do(req)
}

// getRealActiveNode опрашивает sing-box Clash API для определения текущего активного аутбаунда
func (s *Server) getRealActiveNode(ctx context.Context, defaultTag string) string {
	reqCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()

	resp, err := s.clashRequest(reqCtx, http.MethodGet, "/proxies/PROXY", nil)
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
		activeNode = s.getRealActiveNode(c.Context(), "auto")
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
	activeNode := s.getRealActiveNode(c.Context(), "auto")

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

	reqCtx, cancel := context.WithTimeout(c.Context(), 600*time.Millisecond)
	defer cancel()

	if resp, err := s.clashRequest(reqCtx, http.MethodGet, "/proxies", nil); err == nil && resp.StatusCode == http.StatusOK {
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

	reqCtx, cancel := context.WithTimeout(c.Context(), 2*time.Second)
	defer cancel()

	resp, err := s.clashRequest(reqCtx, http.MethodPut, "/proxies/PROXY", payload)
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

// handleAddNode добавляет одиночную ноду по ссылке с валидацией через кандидата
func (s *Server) handleAddNode(c *fiber.Ctx) error {
	var req struct {
		URI string `json:"uri"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	candidate := s.state.Clone()
	node, err := uri.ParseNodeURI(req.URI, candidate.AutoHWID, candidate.CustomHWID)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	candidate.Nodes = append(candidate.Nodes, node)
	candidate.ManualNodes = append(candidate.ManualNodes, req.URI)

	eng := s.getEngine()
	if eng != nil {
		if err := engine.SafeReload(c.Context(), eng, candidate, TargetConfigPath); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "Failed to safely reload engine with new node: " + err.Error(),
			})
		}
	}

	// Фиксируем стейт только после успешного SafeReload (строка 271)
	committed, err := s.state.Commit(candidate, false)
	if err != nil {
		log.Printf("[WARN] State commit warning: %v", err)
	}

	return c.JSON(fiber.Map{"status": "ok", "node": node, "total_nodes": len(committed.Nodes)})
}

// handleUpdateSubscriptions обновляет все сохраненные подписки по модели Two-Phase Commit
func (s *Server) handleUpdateSubscriptions(c *fiber.Ctx) error {
	// 1. Создаем изолированного кандидата конфигурации
	candidate := s.state.Clone()

	uciStorage := config.NewUCIStorage()
	freshCfg, err := uciStorage.Load()
	if err == nil && freshCfg != nil {
		candidate.RuleSets = freshCfg.RuleSets
		candidate.Subscriptions = freshCfg.Subscriptions
		candidate.ManualNodes = freshCfg.ManualNodes
		candidate.RulesetUpdateInterval = freshCfg.RulesetUpdateInterval
		candidate.ClashAPISecret = freshCfg.ClashAPISecret
	}

	var allNodes []*config.GenericNode

	for _, raw := range candidate.ManualNodes {
		if node, err := uri.ParseNodeURI(raw, candidate.AutoHWID, candidate.CustomHWID); err == nil {
			allNodes = append(allNodes, node)
		}
	}

	for _, sub := range candidate.Subscriptions {
		if sub.URL == "" || !sub.Enabled {
			continue
		}
		nodes, err := s.subWorker.FetchNodes(c.Context(), sub)
		if err == nil {
			allNodes = append(allNodes, nodes...)
		}
	}

	candidate.Nodes = allNodes

	// 2. SafeReload выполняется строго над кандидатом
	eng := s.getEngine()
	if err := engine.SafeReload(c.Context(), eng, candidate, TargetConfigPath); err != nil {
		log.Printf("[api] update subscriptions reload error: %v", err)
		// Состояние s.state НЕ затронуто, старые рабочие ноды сохранены
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to safely apply updated subscriptions: " + err.Error(),
		})
	}

	// 3. Только после успешного старта процесса фиксируем новое состояние (строка 321)
	// persistUCI: false — динамические узлы подписок сохраняются в RAM, сберегая флеш
	committed, err := s.state.Commit(candidate, false)
	if err != nil {
		log.Printf("[WARN] State commit warning: %v", err)
	}

	if s.rulesCron != nil {
		s.rulesCron.UpdateRulesets(committed.RuleSets)
		s.rulesCron.SetInterval(committed.RulesetUpdateInterval)
	}

	return c.JSON(fiber.Map{
		"status":         "ok",
		"total_nodes":    len(committed.Nodes),
		"total_rulesets": len(committed.RuleSets),
		"rulesets":       committed.RuleSets,
	})
}

// handleAddSource универсально добавляет подписку или ноду с валидацией кандидата до коммита
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

	candidate := s.state.Clone()
	uci := config.NewUCIStorage()

	var subToAdd *config.SubscriptionConfig
	var manualNodeToAdd string

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

		candidate.Subscriptions = append(candidate.Subscriptions, subCfg)
		existingTags := make(map[string]bool)
		for _, n := range candidate.Nodes {
			existingTags[n.Tag] = true
		}
		for _, n := range newNodes {
			if !existingTags[n.Tag] {
				candidate.Nodes = append(candidate.Nodes, n)
				existingTags[n.Tag] = true
			}
		}
		subToAdd = &subCfg

	case "node":
		node, err := uri.ParseNodeURI(req.URL, candidate.AutoHWID, candidate.CustomHWID)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid node uri: " + err.Error()})
		}

		candidate.Nodes = append(candidate.Nodes, node)
		candidate.ManualNodes = append(candidate.ManualNodes, req.URL)
		manualNodeToAdd = req.URL

	default:
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "type must be 'subscription' or 'node'"})
	}

	// Валидируем и перезагружаем ядро над кандидатом
	eng := s.getEngine()
	if err := engine.SafeReload(c.Context(), eng, candidate, TargetConfigPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to safely reload engine with new source: " + err.Error(),
		})
	}

	// Сохраняем в UCI и фиксируем стейт только после успешного применения ядром
	if subToAdd != nil {
		_ = uci.AddSubscription(*subToAdd)
	}
	if manualNodeToAdd != "" {
		_ = uci.AddManualNode(manualNodeToAdd)
	}

	// Фиксируем обновленное состояние (строка 422)
	committed, err := s.state.Commit(candidate, false)
	if err != nil {
		log.Printf("[WARN] State commit warning: %v", err)
	}

	return c.JSON(fiber.Map{
		"status":      "ok",
		"total_nodes": len(committed.Nodes),
	})
}

// handleReloadConfig считывает конфигурацию с диска и применяет через кандидата
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

	candidate := s.state.Clone()
	*candidate = *newCfg

	var allNodes []*config.GenericNode

	for _, raw := range candidate.ManualNodes {
		if node, err := uri.ParseNodeURI(raw, candidate.AutoHWID, candidate.CustomHWID); err == nil {
			allNodes = append(allNodes, node)
		}
	}

	for _, sub := range candidate.Subscriptions {
		if sub.URL == "" || !sub.Enabled {
			continue
		}
		subNodes, err := s.subWorker.FetchNodes(c.Context(), sub)
		if err == nil {
			allNodes = append(allNodes, subNodes...)
		}
	}

	candidate.Nodes = allNodes

	eng := s.getEngine()

	if len(candidate.Nodes) > 0 {
		if err := engine.SafeReload(c.Context(), eng, candidate, TargetConfigPath); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "Failed to safely reload engine: " + err.Error(),
			})
		}
	} else {
		_ = eng.Stop()
	}

	// Фиксируем новое состояние после прохождения SafeReload (строка 484)
	committed, err := s.state.Commit(candidate, false)
	if err != nil {
		log.Printf("[WARN] State commit warning: %v", err)
	}

	if s.rulesCron != nil {
		s.rulesCron.UpdateRulesets(committed.RuleSets)
		s.rulesCron.SetInterval(committed.RulesetUpdateInterval)
	}

	return c.JSON(fiber.Map{
		"status":  "ok",
		"message": "Configuration reloaded successfully",
		"nodes":   len(committed.Nodes),
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
		reqCtx, cancel := context.WithTimeout(c.Context(), 1*time.Second)
		defer cancel()

		resp, err := s.clashRequest(reqCtx, http.MethodGet, "/proxies/auto", nil)
		if err == nil && resp.StatusCode == http.StatusOK {
			var groupData struct {
				Now string `json:"now"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&groupData); err == nil && groupData.Now != "" {
				_ = resp.Body.Close()

				escapedTarget := url.PathEscape(groupData.Now)
				delayPath := fmt.Sprintf("/proxies/%s/delay?url=%s&timeout=%s", escapedTarget, url.QueryEscape(testURL), timeout)

				dCtx, dCancel := context.WithTimeout(c.Context(), 3*time.Second)
				defer dCancel()

				if dResp, dErr := s.clashRequest(dCtx, http.MethodGet, delayPath, nil); dErr == nil {
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
	delayPath := fmt.Sprintf("/proxies/%s/delay?url=%s&timeout=%s", escapedName, url.QueryEscape(testURL), timeout)

	reqCtx, cancel := context.WithTimeout(c.Context(), 4*time.Second)
	defer cancel()

	resp, err := s.clashRequest(reqCtx, http.MethodGet, delayPath, nil)
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

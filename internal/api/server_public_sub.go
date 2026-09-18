//go:build with_public_sub

package api

import (
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	"cheburnet/internal/config"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"
)

const HasPublicSubFeature = true

type publicSubController struct {
	mu      sync.Mutex
	app     *fiber.App
	running bool
}

func newPublicSubController() *publicSubController {
	return &publicSubController{}
}

func (p *publicSubController) Start(s *Server, port int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		return
	}

	cfg := s.state.Get()
	if !cfg.PublicSubEnabled || strings.TrimSpace(cfg.PublicSubToken) == "" {
		log.Println("[INFO] Публичный сервер подписки отключен в конфигурации")
		return
	}

	if port <= 0 {
		port = 9443
	}

	pubApp := fiber.New(fiber.Config{
		DisableStartupMessage: true,
		AppName:               "Chebur.NET Public Subscription Server",
	})

	pubLimiter := limiter.New(limiter.Config{
		Max:        20,
		Expiration: 1 * time.Minute,
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(fiber.StatusTooManyRequests).SendString("Rate limit exceeded")
		},
	})

	pubApp.Get("/sub/:token", pubLimiter, func(c *fiber.Ctx) error {
		currentCfg := s.state.Get()
		if !currentCfg.PublicSubEnabled || strings.TrimSpace(currentCfg.PublicSubToken) == "" {
			return c.Status(fiber.StatusNotFound).SendString("Not Found")
		}

		clientToken := c.Params("token")
		if subtle.ConstantTimeCompare([]byte(clientToken), []byte(currentCfg.PublicSubToken)) != 1 {
			return c.Status(fiber.StatusUnauthorized).SendString("Unauthorized")
		}

		nodes := currentCfg.Nodes

		if strings.ToLower(c.Query("format")) == "json" {
			return c.JSON(fiber.Map{
				"version": 1,
				"servers": nodes,
			})
		}

		var uriList []string
		for _, n := range nodes {
			if n != nil {
				if uri := nodeToURI(n); uri != "" {
					uriList = append(uriList, uri)
				}
			}
		}

		payload := base64.StdEncoding.EncodeToString([]byte(strings.Join(uriList, "\n")))

		c.Set("Content-Type", "text/plain; charset=utf-8")
		c.Set("Subscription-Userinfo", "upload=0; download=0; total=107374182400; expire=0")
		c.Set("Profile-Update-Interval", "24")
		c.Set("Profile-Title", "Chebur.NET Happ Outbounds")

		return c.SendString(payload)
	})

	pubApp.Use(func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusNotFound).SendString("Not Found")
	})

	p.app = pubApp
	p.running = true

	addr := fmt.Sprintf("0.0.0.0:%d", port)
	log.Printf("[INFO] Запуск публичного сервера подписки на %s", addr)

	go func() {
		if err := pubApp.Listen(addr); err != nil {
			log.Printf("[WARN] Публичный сервер подписки остановлен: %v", err)
			p.mu.Lock()
			p.running = false
			p.mu.Unlock()
		}
	}()
}

func (p *publicSubController) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running && p.app != nil {
		log.Println("[INFO] Остановка публичного сервера подписки...")
		_ = p.app.Shutdown()
		p.running = false
		p.app = nil
	}
}

func nodeToURI(n *config.GenericNode) string {
	if n == nil {
		return ""
	}

	switch strings.ToLower(n.Protocol) {
	case "vless":
		v := url.Values{}
		if n.Security != "" {
			v.Set("security", n.Security)
		}
		if n.SNI != "" {
			v.Set("sni", n.SNI)
		}
		if n.Flow != "" {
			v.Set("flow", n.Flow)
		}
		if n.PublicKey != "" {
			v.Set("pbk", n.PublicKey)
		}
		if n.ShortID != "" {
			v.Set("sid", n.ShortID)
		}
		if n.Fingerprint != "" {
			v.Set("fp", n.Fingerprint)
		}
		if n.Network != "" {
			v.Set("type", n.Network)
		}
		if n.Path != "" {
			v.Set("path", n.Path)
		}
		if n.Host != "" {
			v.Set("host", n.Host)
		}
		return fmt.Sprintf("vless://%s@%s:%d?%s#%s", n.UUID, n.Address, n.Port, v.Encode(), url.QueryEscape(n.Tag))

	case "trojan":
		v := url.Values{}
		if n.Security != "" {
			v.Set("security", n.Security)
		}
		if n.SNI != "" {
			v.Set("sni", n.SNI)
		}
		if n.Network != "" {
			v.Set("type", n.Network)
		}
		if n.Path != "" {
			v.Set("path", n.Path)
		}
		return fmt.Sprintf("trojan://%s@%s:%d?%s#%s", url.QueryEscape(n.Password), n.Address, n.Port, v.Encode(), url.QueryEscape(n.Tag))

	case "shadowsocks", "ss":
		auth := fmt.Sprintf("%s:%s", n.Method, n.Password)
		userInfo := base64.URLEncoding.EncodeToString([]byte(auth))
		return fmt.Sprintf("ss://%s@%s:%d#%s", userInfo, n.Address, n.Port, url.QueryEscape(n.Tag))

	default:
		return ""
	}
}

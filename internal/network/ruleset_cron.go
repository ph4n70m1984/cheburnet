package network

import (
	"context"
	"log"
	"sync"
	"time"
)

type ReloadCallback func() error

type RulesetCron struct {
	loader         *CompressedRulesetLoader
	rulesets       []string
	interval       time.Duration
	reloadCallback ReloadCallback
	mu             sync.RWMutex
	ticker         *time.Ticker
}

func ParseInterval(val string) time.Duration {
	switch val {
	case "24h", "1d":
		return 24 * time.Hour
	case "72h", "3d":
		return 72 * time.Hour
	case "168h", "1w", "7d":
		return 7 * 24 * time.Hour
	default:
		d, err := time.ParseDuration(val)
		if err == nil && d >= time.Hour {
			return d
		}
		return 72 * time.Hour
	}
}

func NewRulesetCron(loader *CompressedRulesetLoader, rulesets []string, intervalStr string, onReload ReloadCallback) *RulesetCron {
	return &RulesetCron{
		loader:         loader,
		rulesets:       rulesets,
		interval:       ParseInterval(intervalStr),
		reloadCallback: onReload,
	}
}

func (c *RulesetCron) SetInterval(intervalStr string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	newInterval := ParseInterval(intervalStr)
	c.interval = newInterval
	if c.ticker != nil {
		c.ticker.Reset(newInterval)
		log.Printf("[ruleset-cron] interval updated to %v", newInterval)
	}
}

func (c *RulesetCron) UpdateRulesets(rulesets []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rulesets = rulesets
}

func (c *RulesetCron) Start(ctx context.Context) {
	go func() {
		c.mu.Lock()
		c.ticker = time.NewTicker(c.interval)
		c.mu.Unlock()
		defer c.ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-c.ticker.C:
				c.mu.RLock()
				currentSets := make([]string, len(c.rulesets))
				copy(currentSets, c.rulesets)
				c.mu.RUnlock()

				log.Println("[ruleset-cron] starting scheduled update...")
				updatedAny := false

				// 1. Проверка и обновление geosite.dat для Xray
				if updatedGeo, err := c.loader.UpdateGeositeDat(); err != nil {
					log.Printf("[ruleset-cron] failed to update geosite.dat: %v", err)
				} else if updatedGeo {
					log.Println("[ruleset-cron] geosite.dat successfully updated")
					updatedAny = true
				}

				// 2. Обновление локальных подсетей .lst.gz
				for _, rs := range currentSets {
					if err := c.loader.UpdateRuleset(rs); err != nil {
						// 404 для подсетей — штатно для сервисов, содержащих только домены
						log.Printf("[ruleset-cron] ruleset %s: %v", rs, err)
					} else {
						log.Printf("[ruleset-cron] successfully updated and compressed subnets for %s", rs)
						updatedAny = true
					}
				}

				// 3. Перезагрузка ядра при наличии изменений
				if updatedAny && c.reloadCallback != nil {
					log.Println("[ruleset-cron] applying updated rulesets into running engine...")
					if err := c.reloadCallback(); err != nil {
						log.Printf("[ruleset-cron] engine reload failed: %v", err)
					}
				}
			}
		}
	}()
}

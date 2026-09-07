package rules

import (
	"bufio"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type ResolvedRules struct {
	Domains []string
	Subnets []string
}

type RuleManager struct {
	client *http.Client
}

func NewRuleManager() *RuleManager {
	return &RuleManager{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// FetchRulesForSets загружает подсети и домены для всех выбранных ruleset
func (rm *RuleManager) FetchRulesForSets(ruleSets []string) (*ResolvedRules, error) {
	res := &ResolvedRules{}

	for _, name := range ruleSets {
		// 1. Пытаемся забрать подсети (если для сервиса существует CIDR-список, как у telegram)
		subnetURL := fmt.Sprintf("https://raw.githubusercontent.com/itdoginfo/allow-domains/main/Subnets/IPv4/%s.lst", name)
		if subnets, err := rm.fetchLines(subnetURL); err == nil && len(subnets) > 0 {
			res.Subnets = append(res.Subnets, subnets...)
		}

		// 2. Пытаемся забрать домены
		domainURL := fmt.Sprintf("https://raw.githubusercontent.com/itdoginfo/allow-domains/main/dist/%s.txt", name)
		if domains, err := rm.fetchLines(domainURL); err == nil && len(domains) > 0 {
			res.Domains = append(res.Domains, domains...)
		}
	}

	return res, nil
}

func (rm *RuleManager) fetchLines(url string) ([]string, error) {
	resp, err := rm.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status: %d", resp.StatusCode)
	}

	var lines []string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	return lines, scanner.Err()
}

package rules

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cheburnet/internal/network"
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
		client: network.NewBypassClient(15*time.Second, network.EmergencyDirectMarkInt),
	}
}

// FetchRulesForSets загружает подсети и домены для всех выбранных ruleset
func (rm *RuleManager) FetchRulesForSets(ruleSets []string) (*ResolvedRules, error) {
	res := &ResolvedRules{}

	for _, name := range ruleSets {
		subnetURL := fmt.Sprintf("https://raw.githubusercontent.com/itdoginfo/allow-domains/main/Subnets/IPv4/%s.lst", name)
		if subnets, err := rm.fetchLines(subnetURL); err == nil && len(subnets) > 0 {
			res.Subnets = append(res.Subnets, subnets...)
		}

		domainURL := fmt.Sprintf("https://raw.githubusercontent.com/itdoginfo/allow-domains/main/dist/%s.txt", name)
		if domains, err := rm.fetchLines(domainURL); err == nil && len(domains) > 0 {
			res.Domains = append(res.Domains, domains...)
		}
	}

	return res, nil
}

func (rm *RuleManager) fetchLines(url string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := rm.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status: %d", resp.StatusCode)
	}

	var lines []string
	limitedReader := io.LimitReader(resp.Body, 8*1024*1024) // 8 МБ лимит
	scanner := bufio.NewScanner(limitedReader)
	scanBuf := make([]byte, 32*1024)
	scanner.Buffer(scanBuf, 64*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	return lines, scanner.Err()
}

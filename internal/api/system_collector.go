//go:build metrics

package api

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

type SystemCollector struct {
	mu sync.Mutex

	prevTotalCPU float64
	prevIdleCPU  float64

	cpuUsageDesc   *prometheus.Desc
	memTotalDesc   *prometheus.Desc
	memFreeDesc    *prometheus.Desc
	memAvailDesc   *prometheus.Desc
	memUsedDesc    *prometheus.Desc
	netRxBytesDesc *prometheus.Desc
	netTxBytesDesc *prometheus.Desc
	netRxPktsDesc  *prometheus.Desc
	netTxPktsDesc  *prometheus.Desc
}

func NewSystemCollector() *SystemCollector {
	return &SystemCollector{
		cpuUsageDesc: prometheus.NewDesc(
			"node_cpu_utilization_ratio",
			"Current total CPU utilization ratio (0.0 to 1.0)",
			nil, nil,
		),
		memTotalDesc: prometheus.NewDesc(
			"node_memory_total_bytes",
			"Total system memory in bytes",
			nil, nil,
		),
		memFreeDesc: prometheus.NewDesc(
			"node_memory_free_bytes",
			"Free system memory in bytes",
			nil, nil,
		),
		memAvailDesc: prometheus.NewDesc(
			"node_memory_available_bytes",
			"Available system memory in bytes",
			nil, nil,
		),
		memUsedDesc: prometheus.NewDesc(
			"node_memory_used_bytes",
			"Used system memory in bytes (Total - Available)",
			nil, nil,
		),
		netRxBytesDesc: prometheus.NewDesc(
			"node_network_receive_bytes_total",
			"Network device receive bytes",
			[]string{"device"}, nil,
		),
		netTxBytesDesc: prometheus.NewDesc(
			"node_network_transmit_bytes_total",
			"Network device transmit bytes",
			[]string{"device"}, nil,
		),
		netRxPktsDesc: prometheus.NewDesc(
			"node_network_receive_packets_total",
			"Network device receive packets",
			[]string{"device"}, nil,
		),
		netTxPktsDesc: prometheus.NewDesc(
			"node_network_transmit_packets_total",
			"Network device transmit packets",
			[]string{"device"}, nil,
		),
	}
}

func (c *SystemCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.cpuUsageDesc
	ch <- c.memTotalDesc
	ch <- c.memFreeDesc
	ch <- c.memAvailDesc
	ch <- c.memUsedDesc
	ch <- c.netRxBytesDesc
	ch <- c.netTxBytesDesc
	ch <- c.netRxPktsDesc
	ch <- c.netTxPktsDesc
}

func (c *SystemCollector) Collect(ch chan<- prometheus.Metric) {
	c.collectCPU(ch)
	c.collectMemory(ch)
	c.collectNetwork(ch)
}

func (c *SystemCollector) collectCPU(ch chan<- prometheus.Metric) {
	file, err := os.Open("/proc/stat")
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		return
	}

	fields := strings.Fields(scanner.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return
	}

	var total, idle float64
	for i := 1; i < len(fields); i++ {
		val, _ := strconv.ParseFloat(fields[i], 64)
		total += val
		if i == 4 {
			idle = val
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.prevTotalCPU > 0 && total > c.prevTotalCPU {
		deltaTotal := total - c.prevTotalCPU
		deltaIdle := idle - c.prevIdleCPU
		usage := 1.0 - (deltaIdle / deltaTotal)
		if usage < 0 {
			usage = 0
		} else if usage > 1.0 {
			usage = 1.0
		}
		ch <- prometheus.MustNewConstMetric(c.cpuUsageDesc, prometheus.GaugeValue, usage)
	}

	c.prevTotalCPU = total
	c.prevIdleCPU = idle
}

func (c *SystemCollector) collectMemory(ch chan<- prometheus.Metric) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer file.Close()

	var total, free, avail float64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		val, _ := strconv.ParseFloat(fields[1], 64)
		valBytes := val * 1024

		switch fields[0] {
		case "MemTotal:":
			total = valBytes
		case "MemFree:":
			free = valBytes
		case "MemAvailable:":
			avail = valBytes
		}
	}

	if total > 0 {
		ch <- prometheus.MustNewConstMetric(c.memTotalDesc, prometheus.GaugeValue, total)
		ch <- prometheus.MustNewConstMetric(c.memFreeDesc, prometheus.GaugeValue, free)

		if avail == 0 {
			avail = free
		}
		ch <- prometheus.MustNewConstMetric(c.memAvailDesc, prometheus.GaugeValue, avail)
		ch <- prometheus.MustNewConstMetric(c.memUsedDesc, prometheus.GaugeValue, total-avail)
	}
}

func (c *SystemCollector) collectNetwork(ch chan<- prometheus.Metric) {
	file, err := os.Open("/proc/net/dev")
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum <= 2 {
			continue
		}

		line := strings.TrimSpace(scanner.Text())
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}

		dev := strings.TrimSpace(parts[0])
		if dev == "lo" {
			continue
		}

		fields := strings.Fields(parts[1])
		if len(fields) < 10 {
			continue
		}

		rxBytes, _ := strconv.ParseFloat(fields[0], 64)
		rxPkts, _ := strconv.ParseFloat(fields[1], 64)
		txBytes, _ := strconv.ParseFloat(fields[8], 64)
		txPkts, _ := strconv.ParseFloat(fields[9], 64)

		ch <- prometheus.MustNewConstMetric(c.netRxBytesDesc, prometheus.CounterValue, rxBytes, dev)
		ch <- prometheus.MustNewConstMetric(c.netTxBytesDesc, prometheus.CounterValue, txBytes, dev)
		ch <- prometheus.MustNewConstMetric(c.netRxPktsDesc, prometheus.CounterValue, rxPkts, dev)
		ch <- prometheus.MustNewConstMetric(c.netTxPktsDesc, prometheus.CounterValue, txPkts, dev)
	}
}

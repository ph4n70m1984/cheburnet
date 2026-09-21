package adaptive

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	sentinelMetricsOnce sync.Once

	sentinelStateGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "cheburnet_sentinel_state",
			Help: "Current active routing group state (1 for active, 0 for inactive).",
		},
		[]string{"group"},
	)

	sentinelSwitchesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cheburnet_sentinel_switches_total",
			Help: "Total number of group transitions executed by sentinel or user.",
		},
		[]string{"from_group", "to_group", "reason"},
	)

	sentinelLastSwitchTimestamp = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "cheburnet_sentinel_last_switch_timestamp_seconds",
			Help: "Unix epoch timestamp of the last executed routing group switch.",
		},
		[]string{"from_group", "to_group"},
	)

	sentinelProbesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cheburnet_sentinel_probes_total",
			Help: "Total number of dual-channel censorship probes executed.",
		},
		[]string{"channel", "status"},
	)

	sentinelCheckDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "cheburnet_sentinel_check_duration_seconds",
			Help:    "Execution latency of censorship check channels.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1.0, 2.0, 3.5},
		},
		[]string{"channel", "status"},
	)
)

func initSentinelMetrics() {
	sentinelMetricsOnce.Do(func() {
		_ = prometheus.Register(sentinelStateGauge)
		_ = prometheus.Register(sentinelSwitchesTotal)
		_ = prometheus.Register(sentinelLastSwitchTimestamp)
		_ = prometheus.Register(sentinelProbesTotal)
		_ = prometheus.Register(sentinelCheckDuration)
	})
}

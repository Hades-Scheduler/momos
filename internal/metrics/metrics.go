// Package metrics holds the Prometheus instruments (plan.md §3⑩): runs per
// status, hook-to-comment latency, and review cost.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// RunsTotal counts runs by terminal status.
	RunsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "momos_runs_total",
		Help: "Total review runs by status.",
	}, []string{"status"})

	// HooksTotal counts received webhooks by result.
	HooksTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "momos_hooks_total",
		Help: "Total webhooks received by result.",
	}, []string{"result"})

	// HookToComment measures the latency from webhook receipt to result callback.
	HookToComment = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "momos_hook_to_comment_seconds",
		Help:    "Latency from webhook receipt to result callback.",
		Buckets: prometheus.ExponentialBuckets(1, 2, 12),
	})

	// CostUSD accumulates review cost.
	CostUSD = promauto.NewCounter(prometheus.CounterOpts{
		Name: "momos_review_cost_usd_total",
		Help: "Cumulative review cost in USD.",
	})
)

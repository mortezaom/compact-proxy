package core

import (
	"fmt"
	"sync/atomic"
)

type Metrics struct {
	RequestsTotal              atomic.Uint64
	UpstreamErrorsTotal        atomic.Uint64
	AuthRefreshTotal           atomic.Uint64
	AccountFailoversTotal      atomic.Uint64
	ActiveStreams              atomic.Uint64
	CancelledStreamsTotal      atomic.Uint64
	CacheAffinityHitsTotal     atomic.Uint64
	CacheAffinityMissesTotal   atomic.Uint64
	ModelDiscoveryRefreshTotal atomic.Uint64
}

func (m *Metrics) render() string {
	return fmt.Sprintf("# TYPE codex_proxy_requests_total counter\ncodex_proxy_requests_total %d\n# TYPE codex_proxy_upstream_errors_total counter\ncodex_proxy_upstream_errors_total %d\n# TYPE codex_proxy_auth_refresh_total counter\ncodex_proxy_auth_refresh_total %d\n# TYPE codex_proxy_account_failovers_total counter\ncodex_proxy_account_failovers_total %d\n# TYPE codex_proxy_active_streams gauge\ncodex_proxy_active_streams %d\n# TYPE codex_proxy_cancelled_streams_total counter\ncodex_proxy_cancelled_streams_total %d\n# TYPE codex_proxy_cache_affinity_hits_total counter\ncodex_proxy_cache_affinity_hits_total %d\n# TYPE codex_proxy_cache_affinity_misses_total counter\ncodex_proxy_cache_affinity_misses_total %d\n# TYPE codex_proxy_model_discovery_refresh_total counter\ncodex_proxy_model_discovery_refresh_total %d\n", m.RequestsTotal.Load(), m.UpstreamErrorsTotal.Load(), m.AuthRefreshTotal.Load(), m.AccountFailoversTotal.Load(), m.ActiveStreams.Load(), m.CancelledStreamsTotal.Load(), m.CacheAffinityHitsTotal.Load(), m.CacheAffinityMissesTotal.Load(), m.ModelDiscoveryRefreshTotal.Load())
}

type StreamGuard struct {
	metrics   *Metrics
	completed atomic.Bool
}

func newStreamGuard(metrics *Metrics) *StreamGuard {
	active := metrics.ActiveStreams.Add(1)
	logDebug("stream metrics opened: active=%d", active)
	return &StreamGuard{metrics: metrics}
}

func (g *StreamGuard) complete() { g.completed.Store(true) }
func (g *StreamGuard) close() {
	completed := g.completed.Load()
	active := g.metrics.ActiveStreams.Add(^uint64(0))
	if !completed {
		cancelled := g.metrics.CancelledStreamsTotal.Add(1)
		logDebug("stream metrics closed: completed=false active=%d cancelled_total=%d", active, cancelled)
		return
	}
	logDebug("stream metrics closed: completed=true active=%d", active)
}

package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ObserverMetrics holds per-observer metrics
type ObserverMetrics struct {
	NumRequests         uint64
	NumErrors           uint64
	NumTimeouts         uint64
	TotalResponseTime   time.Duration
	LowestResponseTime  time.Duration
	HighestResponseTime time.Duration
}

// proxyMetrics tracks operational metrics beyond endpoint-level request tracking.
// It is a package-level singleton accessed via GetProxyMetrics().
type proxyMetrics struct {
	// Per-observer metrics (keyed by observer address)
	observerMetrics map[string]*ObserverMetrics
	mutObserver     sync.RWMutex

	// Failover tier usage (keyed by "shardID:tierName")
	failoverTierCounts map[string]*uint64
	mutFailover        sync.RWMutex

	// Retry counters (keyed by endpoint path)
	retryCounts map[string]*uint64
	mutRetry    sync.RWMutex

	// Rate limit hit counters (keyed by endpoint path)
	rateLimitHits map[string]*uint64
	mutRateLimit  sync.RWMutex

	// Stuck node detections
	stuckNodeCount uint64

	// Cache hit/miss counters (keyed by cache name)
	cacheHits   map[string]*uint64
	cacheMisses map[string]*uint64
	mutCache    sync.RWMutex
}

var (
	globalProxyMetrics     *proxyMetrics
	onceProxyMetrics       sync.Once
)

// GetProxyMetrics returns the singleton proxy metrics instance
func GetProxyMetrics() *proxyMetrics {
	onceProxyMetrics.Do(func() {
		globalProxyMetrics = &proxyMetrics{
			observerMetrics:    make(map[string]*ObserverMetrics),
			failoverTierCounts: make(map[string]*uint64),
			retryCounts:        make(map[string]*uint64),
			rateLimitHits:      make(map[string]*uint64),
			cacheHits:          make(map[string]*uint64),
			cacheMisses:        make(map[string]*uint64),
		}
	})
	return globalProxyMetrics
}

// AddObserverData records a request to a specific observer node
func (pm *proxyMetrics) AddObserverData(observerAddress string, withError bool, isTimeout bool, duration time.Duration) {
	pm.mutObserver.Lock()
	defer pm.mutObserver.Unlock()

	current := pm.observerMetrics[observerAddress]
	if current == nil {
		errCount := uint64(0)
		if withError {
			errCount = 1
		}
		timeoutCount := uint64(0)
		if isTimeout {
			timeoutCount = 1
		}
		pm.observerMetrics[observerAddress] = &ObserverMetrics{
			NumRequests:         1,
			NumErrors:           errCount,
			NumTimeouts:         timeoutCount,
			TotalResponseTime:   duration,
			LowestResponseTime:  duration,
			HighestResponseTime: duration,
		}
		return
	}

	current.NumRequests++
	if withError {
		current.NumErrors++
	}
	if isTimeout {
		current.NumTimeouts++
	}
	if duration < current.LowestResponseTime {
		current.LowestResponseTime = duration
	}
	if duration > current.HighestResponseTime {
		current.HighestResponseTime = duration
	}
	current.TotalResponseTime += duration
}

// IncrementFailoverTier records which fallback tier was used for a shard
func (pm *proxyMetrics) IncrementFailoverTier(shardID uint32, tier string) {
	key := fmt.Sprintf("%d:%s", shardID, tier)
	pm.mutFailover.Lock()
	defer pm.mutFailover.Unlock()

	if pm.failoverTierCounts[key] == nil {
		val := uint64(0)
		pm.failoverTierCounts[key] = &val
	}
	*pm.failoverTierCounts[key]++
}

// IncrementRetry records a retry attempt for a given endpoint
func (pm *proxyMetrics) IncrementRetry(endpoint string) {
	pm.mutRetry.Lock()
	defer pm.mutRetry.Unlock()

	if pm.retryCounts[endpoint] == nil {
		val := uint64(0)
		pm.retryCounts[endpoint] = &val
	}
	*pm.retryCounts[endpoint]++
}

// IncrementRateLimitHit records a rate limit enforcement
func (pm *proxyMetrics) IncrementRateLimitHit(endpoint string) {
	pm.mutRateLimit.Lock()
	defer pm.mutRateLimit.Unlock()

	if pm.rateLimitHits[endpoint] == nil {
		val := uint64(0)
		pm.rateLimitHits[endpoint] = &val
	}
	*pm.rateLimitHits[endpoint]++
}

// IncrementStuckNode records a stuck node detection event
func (pm *proxyMetrics) IncrementStuckNode() {
	atomic.AddUint64(&pm.stuckNodeCount, 1)
}

// IncrementCacheHit records a cache hit
func (pm *proxyMetrics) IncrementCacheHit(cacheName string) {
	pm.mutCache.Lock()
	defer pm.mutCache.Unlock()

	if pm.cacheHits[cacheName] == nil {
		val := uint64(0)
		pm.cacheHits[cacheName] = &val
	}
	*pm.cacheHits[cacheName]++
}

// IncrementCacheMiss records a cache miss
func (pm *proxyMetrics) IncrementCacheMiss(cacheName string) {
	pm.mutCache.Lock()
	defer pm.mutCache.Unlock()

	if pm.cacheMisses[cacheName] == nil {
		val := uint64(0)
		pm.cacheMisses[cacheName] = &val
	}
	*pm.cacheMisses[cacheName]++
}

// GetPrometheusMetrics returns all proxy operational metrics in Prometheus format
func (pm *proxyMetrics) GetPrometheusMetrics() string {
	sb := strings.Builder{}

	// Observer metrics
	pm.mutObserver.RLock()
	observerAddresses := make([]string, 0, len(pm.observerMetrics))
	for addr := range pm.observerMetrics {
		observerAddresses = append(observerAddresses, addr)
	}
	sort.Strings(observerAddresses)
	for _, addr := range observerAddresses {
		m := pm.observerMetrics[addr]
		sb.WriteString(fmt.Sprintf("observer_num_requests{address=\"%s\"} %d\n", addr, m.NumRequests))
		sb.WriteString(fmt.Sprintf("observer_num_errors{address=\"%s\"} %d\n", addr, m.NumErrors))
		sb.WriteString(fmt.Sprintf("observer_num_timeouts{address=\"%s\"} %d\n", addr, m.NumTimeouts))
		sb.WriteString(fmt.Sprintf("observer_total_response_time_ns{address=\"%s\"} %d\n", addr, m.TotalResponseTime))
		sb.WriteString(fmt.Sprintf("observer_highest_response_time_ns{address=\"%s\"} %d\n", addr, m.HighestResponseTime))
		sb.WriteString(fmt.Sprintf("observer_lowest_response_time_ns{address=\"%s\"} %d\n", addr, m.LowestResponseTime))
	}
	pm.mutObserver.RUnlock()

	// Failover tier counts
	pm.mutFailover.RLock()
	tierKeys := make([]string, 0, len(pm.failoverTierCounts))
	for k := range pm.failoverTierCounts {
		tierKeys = append(tierKeys, k)
	}
	sort.Strings(tierKeys)
	for _, key := range tierKeys {
		parts := strings.SplitN(key, ":", 2)
		shard := parts[0]
		tier := parts[1]
		sb.WriteString(fmt.Sprintf("failover_tier_usage{shard=\"%s\", tier=\"%s\"} %d\n", shard, tier, *pm.failoverTierCounts[key]))
	}
	pm.mutFailover.RUnlock()

	// Retry counts
	pm.mutRetry.RLock()
	retryKeys := make([]string, 0, len(pm.retryCounts))
	for k := range pm.retryCounts {
		retryKeys = append(retryKeys, k)
	}
	sort.Strings(retryKeys)
	for _, endpoint := range retryKeys {
		sb.WriteString(fmt.Sprintf("observer_retry_count{endpoint=\"%s\"} %d\n", endpoint, *pm.retryCounts[endpoint]))
	}
	pm.mutRetry.RUnlock()

	// Rate limit hits
	pm.mutRateLimit.RLock()
	rlKeys := make([]string, 0, len(pm.rateLimitHits))
	for k := range pm.rateLimitHits {
		rlKeys = append(rlKeys, k)
	}
	sort.Strings(rlKeys)
	for _, endpoint := range rlKeys {
		sb.WriteString(fmt.Sprintf("rate_limit_hits{endpoint=\"%s\"} %d\n", endpoint, *pm.rateLimitHits[endpoint]))
	}
	pm.mutRateLimit.RUnlock()

	// Stuck node count
	stuckCount := atomic.LoadUint64(&pm.stuckNodeCount)
	if stuckCount > 0 {
		sb.WriteString(fmt.Sprintf("stuck_node_detections_total %d\n", stuckCount))
	}

	// Cache hits/misses
	pm.mutCache.RLock()
	cacheNames := make(map[string]bool)
	for k := range pm.cacheHits {
		cacheNames[k] = true
	}
	for k := range pm.cacheMisses {
		cacheNames[k] = true
	}
	sortedCacheNames := make([]string, 0, len(cacheNames))
	for k := range cacheNames {
		sortedCacheNames = append(sortedCacheNames, k)
	}
	sort.Strings(sortedCacheNames)
	for _, name := range sortedCacheNames {
		hits := uint64(0)
		misses := uint64(0)
		if pm.cacheHits[name] != nil {
			hits = *pm.cacheHits[name]
		}
		if pm.cacheMisses[name] != nil {
			misses = *pm.cacheMisses[name]
		}
		sb.WriteString(fmt.Sprintf("cache_hits{cache=\"%s\"} %d\n", name, hits))
		sb.WriteString(fmt.Sprintf("cache_misses{cache=\"%s\"} %d\n", name, misses))
	}
	pm.mutCache.RUnlock()

	return sb.String()
}

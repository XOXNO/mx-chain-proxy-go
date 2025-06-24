package middleware

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	logger "github.com/multiversx/mx-chain-logger-go"
	"github.com/multiversx/mx-chain-proxy-go/data"
)

// ReturnCodeRequestError defines a request which hasn't been executed successfully due to a bad request received
const ReturnCodeRequestError string = "bad_request"

var logRateLimit = logger.GetOrCreate("rate-limiter")

// requestInfo holds request count and timestamp information
type requestInfo struct {
	count     uint64
	firstSeen time.Time
	lastSeen  time.Time
}

type rateLimiter struct {
	requestsMap    map[string]*requestInfo
	mutRequestsMap sync.RWMutex
	limits         map[string]uint64
	countDuration  time.Duration
	cleanupTicker  *time.Ticker
	stopCleanup    chan struct{}
}

// NewRateLimiter returns a new instance of rateLimiter
func NewRateLimiter(limits map[string]uint64, countDuration time.Duration) (*rateLimiter, error) {
	if limits == nil {
		return nil, ErrNilLimitsMapForEndpoints
	}
	
	rl := &rateLimiter{
		requestsMap:   make(map[string]*requestInfo),
		limits:        limits,
		countDuration: countDuration,
		stopCleanup:   make(chan struct{}),
	}
	
	// Start automatic cleanup of expired entries to prevent memory growth
	rl.startCleanupRoutine()
	
	return rl, nil
}

// startCleanupRoutine starts a background goroutine that periodically cleans up expired entries
func (rl *rateLimiter) startCleanupRoutine() {
	// Run cleanup every half of the count duration, but not less than 1 minute
	cleanupInterval := rl.countDuration / 2
	if cleanupInterval < time.Minute {
		cleanupInterval = time.Minute
	}
	
	rl.cleanupTicker = time.NewTicker(cleanupInterval)
	
	go func() {
		for {
			select {
			case <-rl.cleanupTicker.C:
				rl.cleanupExpiredEntries()
			case <-rl.stopCleanup:
				rl.cleanupTicker.Stop()
				return
			}
		}
	}()
}

// cleanupExpiredEntries removes entries that are older than the rate limit window
func (rl *rateLimiter) cleanupExpiredEntries() {
	rl.mutRequestsMap.Lock()
	defer rl.mutRequestsMap.Unlock()
	
	now := time.Now()
	expiredKeys := make([]string, 0)
	
	for key, info := range rl.requestsMap {
		// Remove entries that haven't been accessed in the rate limit window
		if now.Sub(info.lastSeen) > rl.countDuration {
			expiredKeys = append(expiredKeys, key)
		}
	}
	
	// Remove expired entries
	for _, key := range expiredKeys {
		delete(rl.requestsMap, key)
	}
	
	if len(expiredKeys) > 0 {
		logRateLimit.Debug("cleaned up expired rate limit entries", 
			"expired_count", len(expiredKeys), 
			"remaining_count", len(rl.requestsMap))
	}
}

// Stop stops the cleanup routine and releases resources
func (rl *rateLimiter) Stop() {
	if rl.stopCleanup != nil {
		close(rl.stopCleanup)
	}
}

// MiddlewareHandlerFunc returns the gin middleware for limiting the number of requests for a given endpoint
func (rl *rateLimiter) MiddlewareHandlerFunc() gin.HandlerFunc {
	return func(c *gin.Context) {
		endpoint := c.FullPath()

		limitForEndpoint, isEndpointLimited := rl.limits[endpoint]
		if !isEndpointLimited {
			return
		}

		clientIP := c.ClientIP()
		key := fmt.Sprintf("%s_%s", endpoint, clientIP)

		numRequests, isWithinWindow := rl.addInRequestsMap(key)
		if !isWithinWindow {
			// Request is outside the rate limit window, reset counter
			numRequests = 1
		}
		
		if numRequests > limitForEndpoint {
			printMessage := fmt.Sprintf("your IP exceeded the limit of %d requests in %v for this endpoint", limitForEndpoint, rl.countDuration)
			c.AbortWithStatusJSON(http.StatusTooManyRequests, data.GenericAPIResponse{
				Data:  nil,
				Error: printMessage,
				Code:  data.ReturnCode(ReturnCodeRequestError),
			})
		}
	}
}

func (rl *rateLimiter) addInRequestsMap(key string) (uint64, bool) {
	rl.mutRequestsMap.Lock()
	defer rl.mutRequestsMap.Unlock()

	now := time.Now()
	info, exists := rl.requestsMap[key]
	
	if !exists {
		// First request for this key
		rl.requestsMap[key] = &requestInfo{
			count:     1,
			firstSeen: now,
			lastSeen:  now,
		}
		return 1, true
	}

	// Check if the request is within the rate limit window
	if now.Sub(info.firstSeen) > rl.countDuration {
		// Outside window, reset the counter
		rl.requestsMap[key] = &requestInfo{
			count:     1,
			firstSeen: now,
			lastSeen:  now,
		}
		return 1, false
	}

	// Within window, increment counter
	info.count++
	info.lastSeen = now

	return info.count, true
}

// ResetMap has to be called from outside at a given interval so the requests map will be cleaned and older restrictions
// would be erased. This method now performs optimized cleanup instead of full reset to prevent unbounded memory growth.
func (rl *rateLimiter) ResetMap(version string) {
	// Perform selective cleanup instead of full reset
	rl.cleanupExpiredEntries()
	
	logRateLimit.Info("rate limiter map has been cleaned up", "version", version, "time", time.Now())
}

// GetStats returns statistics about the rate limiter for monitoring
func (rl *rateLimiter) GetStats() map[string]interface{} {
	rl.mutRequestsMap.RLock()
	defer rl.mutRequestsMap.RUnlock()
	
	totalEntries := len(rl.requestsMap)
	totalRequests := uint64(0)
	
	for _, info := range rl.requestsMap {
		totalRequests += info.count
	}
	
	return map[string]interface{}{
		"total_entries":    totalEntries,
		"total_requests":   totalRequests,
		"count_duration":   rl.countDuration.String(),
		"cleanup_interval": (rl.countDuration / 2).String(),
	}
}

// IsInterfaceNil returns true if there is no value under the interface
func (rl *rateLimiter) IsInterfaceNil() bool {
	return rl == nil
}

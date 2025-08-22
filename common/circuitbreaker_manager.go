package common

import (
	"sync"
	"time"

	logger "github.com/multiversx/mx-chain-logger-go"
)

var log = logger.GetOrCreate("circuitbreaker")

// CircuitBreakerManager manages circuit breakers for multiple observers
type CircuitBreakerManager struct {
	breakers map[string]*CircuitBreaker
	config   CircuitBreakerConfig
	mutex    sync.RWMutex
	enabled  bool
}

// NewCircuitBreakerManager creates a new circuit breaker manager
func NewCircuitBreakerManager(config CircuitBreakerConfig, enabled bool) *CircuitBreakerManager {
	return &CircuitBreakerManager{
		breakers: make(map[string]*CircuitBreaker),
		config:   config,
		enabled:  enabled,
	}
}

// GetBreaker returns the circuit breaker for the given observer address
func (cbm *CircuitBreakerManager) GetBreaker(observerAddress string) *CircuitBreaker {
	if !cbm.enabled {
		return nil
	}

	cbm.mutex.RLock()
	breaker, exists := cbm.breakers[observerAddress]
	cbm.mutex.RUnlock()

	if !exists {
		cbm.mutex.Lock()
		// Double-check pattern
		breaker, exists = cbm.breakers[observerAddress]
		if !exists {
			breaker = NewCircuitBreaker(cbm.config)
			cbm.breakers[observerAddress] = breaker
			log.Debug("created circuit breaker for observer", "address", observerAddress)
		}
		cbm.mutex.Unlock()
	}

	return breaker
}

// CanExecute checks if requests can be executed for the given observer
func (cbm *CircuitBreakerManager) CanExecute(observerAddress string) bool {
	if !cbm.enabled {
		return true
	}

	breaker := cbm.GetBreaker(observerAddress)
	return breaker.CanExecute()
}

// Execute executes a function with circuit breaker protection
func (cbm *CircuitBreakerManager) Execute(observerAddress string, fn func() error) error {
	if !cbm.enabled {
		return fn()
	}

	breaker := cbm.GetBreaker(observerAddress)
	err := breaker.Call(fn)
	
	if err != nil {
		state := breaker.GetState()
		failureCount := breaker.GetFailureCount()
		log.Debug("circuit breaker call failed", 
			"address", observerAddress, 
			"state", state.String(), 
			"failure_count", failureCount,
			"error", err.Error())
		
		// Log state transitions
		if state == CircuitBreakerStateOpen {
			log.Warn("circuit breaker opened for observer", 
				"address", observerAddress,
				"failure_count", failureCount)
		}
	}

	return err
}

// GetBreakerStats returns statistics for all circuit breakers
func (cbm *CircuitBreakerManager) GetBreakerStats() map[string]CircuitBreakerStats {
	if !cbm.enabled {
		return make(map[string]CircuitBreakerStats)
	}

	cbm.mutex.RLock()
	defer cbm.mutex.RUnlock()

	stats := make(map[string]CircuitBreakerStats)
	for address, breaker := range cbm.breakers {
		stats[address] = CircuitBreakerStats{
			State:        breaker.GetState(),
			FailureCount: breaker.GetFailureCount(),
		}
	}

	return stats
}

// ResetBreaker resets the circuit breaker for the given observer
func (cbm *CircuitBreakerManager) ResetBreaker(observerAddress string) {
	if !cbm.enabled {
		return
	}

	cbm.mutex.RLock()
	breaker, exists := cbm.breakers[observerAddress]
	cbm.mutex.RUnlock()

	if exists {
		breaker.Reset()
		log.Info("circuit breaker reset", "address", observerAddress)
	}
}

// ResetAllBreakers resets all circuit breakers
func (cbm *CircuitBreakerManager) ResetAllBreakers() {
	if !cbm.enabled {
		return
	}

	cbm.mutex.RLock()
	defer cbm.mutex.RUnlock()

	for address, breaker := range cbm.breakers {
		breaker.Reset()
		log.Info("circuit breaker reset", "address", address)
	}
}

// CleanupStaleBreakers removes circuit breakers that haven't been used recently
func (cbm *CircuitBreakerManager) CleanupStaleBreakers() {
	if !cbm.enabled {
		return
	}

	cbm.mutex.Lock()
	defer cbm.mutex.Unlock()

	now := time.Now()
	staleThreshold := time.Duration(cbm.config.RecoveryTimeoutSec*10) * time.Second

	for address, breaker := range cbm.breakers {
		// Only clean up breakers in closed state that haven't failed recently
		if breaker.GetState() == CircuitBreakerStateClosed && 
		   breaker.GetFailureCount() == 0 &&
		   now.Sub(breaker.lastFailureTime) > staleThreshold {
			delete(cbm.breakers, address)
			log.Debug("cleaned up stale circuit breaker", "address", address)
		}
	}
}

// CircuitBreakerStats holds statistics for a circuit breaker
type CircuitBreakerStats struct {
	State        CircuitBreakerState
	FailureCount int
}

// IsEnabled returns whether circuit breaker is enabled
func (cbm *CircuitBreakerManager) IsEnabled() bool {
	return cbm.enabled
}

// SetEnabled enables or disables the circuit breaker manager
func (cbm *CircuitBreakerManager) SetEnabled(enabled bool) {
	cbm.enabled = enabled
	if enabled {
		log.Info("circuit breaker manager enabled")
	} else {
		log.Info("circuit breaker manager disabled")
	}
}
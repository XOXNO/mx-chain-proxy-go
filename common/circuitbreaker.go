package common

import (
	"fmt"
	"sync"
	"time"
)

// CircuitBreakerState represents the state of a circuit breaker
type CircuitBreakerState int

const (
	// CircuitBreakerStateClosed - normal operation, requests pass through
	CircuitBreakerStateClosed CircuitBreakerState = iota
	// CircuitBreakerStateOpen - circuit is open, requests fail fast
	CircuitBreakerStateOpen
	// CircuitBreakerStateHalfOpen - limited requests pass through for testing
	CircuitBreakerStateHalfOpen
)

// CircuitBreakerConfig holds the configuration for circuit breaker
type CircuitBreakerConfig struct {
	FailureThreshold    int
	RecoveryTimeoutSec  int
	HalfOpenMaxCalls    int
	SuccessThreshold    int
}

// CircuitBreaker implements circuit breaker pattern for fault tolerance
type CircuitBreaker struct {
	config           CircuitBreakerConfig
	state            CircuitBreakerState
	failureCount     int
	successCount     int
	halfOpenCalls    int
	lastFailureTime  time.Time
	mutex            sync.RWMutex
}

// NewCircuitBreaker creates a new circuit breaker instance
func NewCircuitBreaker(config CircuitBreakerConfig) *CircuitBreaker {
	return &CircuitBreaker{
		config: config,
		state:  CircuitBreakerStateClosed,
	}
}

// Call executes the given function with circuit breaker protection
func (cb *CircuitBreaker) Call(fn func() error) error {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	// Check if we should transition from open to half-open
	if cb.state == CircuitBreakerStateOpen {
		if time.Since(cb.lastFailureTime) > time.Duration(cb.config.RecoveryTimeoutSec)*time.Second {
			cb.state = CircuitBreakerStateHalfOpen
			cb.halfOpenCalls = 0
			cb.successCount = 0
		} else {
			return fmt.Errorf("circuit breaker is open")
		}
	}

	// Check if we should reject the call in half-open state
	if cb.state == CircuitBreakerStateHalfOpen {
		if cb.halfOpenCalls >= cb.config.HalfOpenMaxCalls {
			return fmt.Errorf("circuit breaker half-open max calls exceeded")
		}
		cb.halfOpenCalls++
	}

	// Execute the function
	err := fn()

	// Handle the result
	if err != nil {
		cb.onFailure()
		return err
	}

	cb.onSuccess()
	return nil
}

// CanExecute checks if the circuit breaker allows execution
func (cb *CircuitBreaker) CanExecute() bool {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()

	if cb.state == CircuitBreakerStateOpen {
		if time.Since(cb.lastFailureTime) > time.Duration(cb.config.RecoveryTimeoutSec)*time.Second {
			return true // Transition to half-open will happen on next call
		}
		return false
	}

	if cb.state == CircuitBreakerStateHalfOpen {
		return cb.halfOpenCalls < cb.config.HalfOpenMaxCalls
	}

	return true // Closed state
}

// GetState returns the current state of the circuit breaker
func (cb *CircuitBreaker) GetState() CircuitBreakerState {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()
	return cb.state
}

// GetFailureCount returns the current failure count
func (cb *CircuitBreaker) GetFailureCount() int {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()
	return cb.failureCount
}

// Reset resets the circuit breaker to closed state
func (cb *CircuitBreaker) Reset() {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	cb.state = CircuitBreakerStateClosed
	cb.failureCount = 0
	cb.successCount = 0
	cb.halfOpenCalls = 0
}

func (cb *CircuitBreaker) onSuccess() {
	if cb.state == CircuitBreakerStateHalfOpen {
		cb.successCount++
		if cb.successCount >= cb.config.SuccessThreshold {
			cb.state = CircuitBreakerStateClosed
			cb.failureCount = 0
			cb.successCount = 0
			cb.halfOpenCalls = 0
		}
	} else {
		// Reset failure count on successful call in closed state
		cb.failureCount = 0
	}
}

func (cb *CircuitBreaker) onFailure() {
	cb.lastFailureTime = time.Now()

	if cb.state == CircuitBreakerStateClosed {
		cb.failureCount++
		if cb.failureCount >= cb.config.FailureThreshold {
			cb.state = CircuitBreakerStateOpen
		}
	} else if cb.state == CircuitBreakerStateHalfOpen {
		cb.state = CircuitBreakerStateOpen
		cb.halfOpenCalls = 0
		cb.successCount = 0
	}
}

// String returns a string representation of the circuit breaker state
func (cbs CircuitBreakerState) String() string {
	switch cbs {
	case CircuitBreakerStateClosed:
		return "CLOSED"
	case CircuitBreakerStateOpen:
		return "OPEN"
	case CircuitBreakerStateHalfOpen:
		return "HALF_OPEN"
	default:
		return "UNKNOWN"
	}
}
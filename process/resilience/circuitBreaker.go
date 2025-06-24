package resilience

import (
	"context"
	"sync"
	"time"
)

// circuitBreakerImpl implements the CircuitBreaker interface
type circuitBreakerImpl struct {
	name   string
	config CircuitBreakerConfig

	mutex           sync.RWMutex
	state           CircuitBreakerState
	failureCount    int
	successCount    int
	totalRequests   int
	lastFailureTime time.Time
	lastSuccessTime time.Time
	stateChangedAt  time.Time
	halfOpenCalls   int
}

// newCircuitBreaker creates a new circuit breaker instance
func newCircuitBreaker(name string, config CircuitBreakerConfig) (*circuitBreakerImpl, error) {
	// Validate configuration
	if config.FailureThreshold <= 0 {
		config.FailureThreshold = 5
	}
	if config.RecoveryTimeoutSec <= 0 {
		config.RecoveryTimeoutSec = 60
	}
	if config.HalfOpenMaxCalls <= 0 {
		config.HalfOpenMaxCalls = 1
	}
	if config.SuccessThreshold <= 0 {
		config.SuccessThreshold = 1
	}

	return &circuitBreakerImpl{
		name:           name,
		config:         config,
		state:          StateClosed,
		stateChangedAt: time.Now(),
	}, nil
}

// Execute runs the given function with circuit breaker protection
func (cb *circuitBreakerImpl) Execute(ctx context.Context, fn func(ctx context.Context) error) error {
	if !cb.config.Enabled {
		return fn(ctx)
	}

	// Check if we can execute the request
	if !cb.canExecute() {
		return ErrCircuitBreakerOpen
	}

	// Update total requests counter
	cb.mutex.Lock()
	cb.totalRequests++
	if cb.state == StateHalfOpen {
		cb.halfOpenCalls++
	}
	cb.mutex.Unlock()

	// Execute the function
	err := fn(ctx)
	
	// Record the result
	if err != nil {
		cb.recordFailure()
	} else {
		cb.recordSuccess()
	}

	return err
}

// canExecute determines if a request can be executed based on current state
func (cb *circuitBreakerImpl) canExecute() bool {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()

	switch cb.state {
	case StateClosed:
		return true
	case StateOpen:
		// Check if we should transition to half-open
		if time.Since(cb.stateChangedAt) > time.Duration(cb.config.RecoveryTimeoutSec)*time.Second {
			cb.mutex.RUnlock()
			cb.mutex.Lock()
			// Double-check after acquiring write lock
			if cb.state == StateOpen && time.Since(cb.stateChangedAt) > time.Duration(cb.config.RecoveryTimeoutSec)*time.Second {
				cb.transitionToHalfOpen()
			}
			cb.mutex.Unlock()
			cb.mutex.RLock()
			return cb.state == StateHalfOpen
		}
		return false
	case StateHalfOpen:
		return cb.halfOpenCalls < cb.config.HalfOpenMaxCalls
	default:
		return false
	}
}

// recordSuccess records a successful execution
func (cb *circuitBreakerImpl) recordSuccess() {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	cb.successCount++
	cb.lastSuccessTime = time.Now()

	switch cb.state {
	case StateClosed:
		// Stay closed, reset failure count
		cb.failureCount = 0
	case StateHalfOpen:
		// Check if we have enough successes to close the circuit
		if cb.successCount >= cb.config.SuccessThreshold {
			cb.transitionToClosed()
		}
	}
}

// recordFailure records a failed execution
func (cb *circuitBreakerImpl) recordFailure() {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	cb.failureCount++
	cb.lastFailureTime = time.Now()

	switch cb.state {
	case StateClosed:
		// Check if we should open the circuit
		if cb.failureCount >= cb.config.FailureThreshold {
			cb.transitionToOpen()
		}
	case StateHalfOpen:
		// Any failure in half-open state opens the circuit
		cb.transitionToOpen()
	}
}

// transitionToClosed transitions the circuit breaker to closed state
func (cb *circuitBreakerImpl) transitionToClosed() {
	cb.state = StateClosed
	cb.failureCount = 0
	cb.successCount = 0
	cb.halfOpenCalls = 0
	cb.stateChangedAt = time.Now()
}

// transitionToOpen transitions the circuit breaker to open state
func (cb *circuitBreakerImpl) transitionToOpen() {
	cb.state = StateOpen
	cb.successCount = 0
	cb.halfOpenCalls = 0
	cb.stateChangedAt = time.Now()
}

// transitionToHalfOpen transitions the circuit breaker to half-open state
func (cb *circuitBreakerImpl) transitionToHalfOpen() {
	cb.state = StateHalfOpen
	cb.successCount = 0
	cb.halfOpenCalls = 0
	cb.stateChangedAt = time.Now()
}

// GetState returns the current state of the circuit breaker
func (cb *circuitBreakerImpl) GetState() CircuitBreakerState {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()
	return cb.state
}

// GetStats returns the statistics of the circuit breaker
func (cb *circuitBreakerImpl) GetStats() CircuitBreakerStats {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()

	return CircuitBreakerStats{
		State:             cb.state,
		FailureCount:      cb.failureCount,
		SuccessCount:      cb.successCount,
		TotalRequests:     cb.totalRequests,
		LastFailureTime:   cb.lastFailureTime,
		LastSuccessTime:   cb.lastSuccessTime,
		StateChangedTime:  cb.stateChangedAt,
	}
}

// Reset manually resets the circuit breaker to closed state
func (cb *circuitBreakerImpl) Reset() {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()
	cb.transitionToClosed()
}

// Name returns the name/identifier of the circuit breaker
func (cb *circuitBreakerImpl) Name() string {
	return cb.name
}
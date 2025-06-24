package resilience

import (
	"context"
	"time"
)

// CircuitBreakerState represents the state of the circuit breaker
type CircuitBreakerState int

const (
	// StateClosed allows requests to flow through
	StateClosed CircuitBreakerState = iota
	// StateOpen blocks all requests
	StateOpen
	// StateHalfOpen allows limited requests to test service recovery
	StateHalfOpen
)

// String returns the string representation of the circuit breaker state
func (s CircuitBreakerState) String() string {
	switch s {
	case StateClosed:
		return "CLOSED"
	case StateOpen:
		return "OPEN"
	case StateHalfOpen:
		return "HALF_OPEN"
	default:
		return "UNKNOWN"
	}
}

// CircuitBreakerConfig holds the configuration for circuit breaker
type CircuitBreakerConfig struct {
	// Enabled determines if circuit breaker is active
	Enabled bool
	// FailureThreshold is the number of consecutive failures before opening the circuit
	FailureThreshold int
	// RecoveryTimeoutSec is the timeout in seconds before transitioning from open to half-open
	RecoveryTimeoutSec int
	// HalfOpenMaxCalls is the maximum number of requests allowed in half-open state
	HalfOpenMaxCalls int
	// SuccessThreshold is the number of consecutive successes needed to close the circuit in half-open state
	SuccessThreshold int
}

// CircuitBreakerStats holds statistics for the circuit breaker
type CircuitBreakerStats struct {
	State             CircuitBreakerState
	FailureCount      int
	SuccessCount      int
	TotalRequests     int
	LastFailureTime   time.Time
	LastSuccessTime   time.Time
	StateChangedTime  time.Time
}

// CircuitBreaker defines the interface for circuit breaker functionality
type CircuitBreaker interface {
	// Execute runs the given function with circuit breaker protection
	Execute(ctx context.Context, fn func(ctx context.Context) error) error
	// GetState returns the current state of the circuit breaker
	GetState() CircuitBreakerState
	// GetStats returns the statistics of the circuit breaker
	GetStats() CircuitBreakerStats
	// Reset manually resets the circuit breaker to closed state
	Reset()
	// Name returns the name/identifier of the circuit breaker
	Name() string
}

// CircuitBreakerFactory creates circuit breaker instances
type CircuitBreakerFactory interface {
	// CreateCircuitBreaker creates a new circuit breaker with the given name and config
	CreateCircuitBreaker(name string, config CircuitBreakerConfig) CircuitBreaker
}
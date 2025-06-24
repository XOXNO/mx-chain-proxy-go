package resilience

import (
	"context"
)

// disabledCircuitBreaker is a circuit breaker that is always disabled
type disabledCircuitBreaker struct {
	name string
}

// Execute simply runs the function without any circuit breaker logic
func (d *disabledCircuitBreaker) Execute(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

// GetState always returns closed state
func (d *disabledCircuitBreaker) GetState() CircuitBreakerState {
	return StateClosed
}

// GetStats returns empty stats
func (d *disabledCircuitBreaker) GetStats() CircuitBreakerStats {
	return CircuitBreakerStats{
		State: StateClosed,
	}
}

// Reset does nothing
func (d *disabledCircuitBreaker) Reset() {
	// No-op
}

// Name returns the name of the circuit breaker
func (d *disabledCircuitBreaker) Name() string {
	return d.name
}
package resilience

import "errors"

var (
	// ErrCircuitBreakerOpen is returned when the circuit breaker is in open state
	ErrCircuitBreakerOpen = errors.New("circuit breaker is open")
	// ErrCircuitBreakerHalfOpenExceeded is returned when half-open state max calls exceeded
	ErrCircuitBreakerHalfOpenExceeded = errors.New("circuit breaker half-open max calls exceeded")
	// ErrInvalidConfig is returned when circuit breaker configuration is invalid
	ErrInvalidConfig = errors.New("invalid circuit breaker configuration")
)

// IsCircuitBreakerError checks if the error is a circuit breaker error
func IsCircuitBreakerError(err error) bool {
	return errors.Is(err, ErrCircuitBreakerOpen) || errors.Is(err, ErrCircuitBreakerHalfOpenExceeded)
}
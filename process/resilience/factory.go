package resilience

import (
	"sync"
)

// circuitBreakerFactory implements CircuitBreakerFactory
type circuitBreakerFactory struct {
	mutex           sync.RWMutex
	circuitBreakers map[string]CircuitBreaker
}

// NewCircuitBreakerFactory creates a new circuit breaker factory
func NewCircuitBreakerFactory() CircuitBreakerFactory {
	return &circuitBreakerFactory{
		circuitBreakers: make(map[string]CircuitBreaker),
	}
}

// CreateCircuitBreaker creates a new circuit breaker with the given name and config
func (f *circuitBreakerFactory) CreateCircuitBreaker(name string, config CircuitBreakerConfig) CircuitBreaker {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	// Check if circuit breaker already exists
	if cb, exists := f.circuitBreakers[name]; exists {
		return cb
	}

	// Create new circuit breaker
	cb, err := newCircuitBreaker(name, config)
	if err != nil {
		// Return a disabled circuit breaker on error
		return &disabledCircuitBreaker{name: name}
	}

	f.circuitBreakers[name] = cb
	return cb
}

// GetCircuitBreaker returns an existing circuit breaker by name
func (f *circuitBreakerFactory) GetCircuitBreaker(name string) (CircuitBreaker, bool) {
	f.mutex.RLock()
	defer f.mutex.RUnlock()

	cb, exists := f.circuitBreakers[name]
	return cb, exists
}

// GetAllCircuitBreakers returns all circuit breakers
func (f *circuitBreakerFactory) GetAllCircuitBreakers() map[string]CircuitBreaker {
	f.mutex.RLock()
	defer f.mutex.RUnlock()

	result := make(map[string]CircuitBreaker)
	for name, cb := range f.circuitBreakers {
		result[name] = cb
	}
	return result
}

// ResetAll resets all circuit breakers
func (f *circuitBreakerFactory) ResetAll() {
	f.mutex.RLock()
	defer f.mutex.RUnlock()

	for _, cb := range f.circuitBreakers {
		cb.Reset()
	}
}
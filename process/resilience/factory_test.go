package resilience

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCircuitBreakerFactory(t *testing.T) {
	t.Parallel()

	t.Run("should create new circuit breaker factory", func(t *testing.T) {
		factory := NewCircuitBreakerFactory()
		require.NotNil(t, factory)
	})

	t.Run("should create circuit breaker with valid config", func(t *testing.T) {
		factory := NewCircuitBreakerFactory()
		config := CircuitBreakerConfig{
			Enabled:            true,
			FailureThreshold:   3,
			RecoveryTimeoutSec: 60,
			HalfOpenMaxCalls:   1,
			SuccessThreshold:   1,
		}

		cb := factory.CreateCircuitBreaker("test-service", config)
		require.NotNil(t, cb)
		assert.Equal(t, "test-service", cb.Name())
	})

	t.Run("should return same circuit breaker for same name", func(t *testing.T) {
		factory := NewCircuitBreakerFactory()
		config := CircuitBreakerConfig{
			Enabled:            true,
			FailureThreshold:   3,
			RecoveryTimeoutSec: 60,
			HalfOpenMaxCalls:   1,
			SuccessThreshold:   1,
		}

		cb1 := factory.CreateCircuitBreaker("test-service", config)
		cb2 := factory.CreateCircuitBreaker("test-service", config)

		assert.Equal(t, cb1, cb2)
	})

	t.Run("should create different circuit breakers for different names", func(t *testing.T) {
		factory := NewCircuitBreakerFactory()
		config := CircuitBreakerConfig{
			Enabled:            true,
			FailureThreshold:   3,
			RecoveryTimeoutSec: 60,
			HalfOpenMaxCalls:   1,
			SuccessThreshold:   1,
		}

		cb1 := factory.CreateCircuitBreaker("service-1", config)
		cb2 := factory.CreateCircuitBreaker("service-2", config)

		assert.NotEqual(t, cb1, cb2)
		assert.Equal(t, "service-1", cb1.Name())
		assert.Equal(t, "service-2", cb2.Name())
	})

	t.Run("should return disabled circuit breaker on error", func(t *testing.T) {
		factory := NewCircuitBreakerFactory()
		config := CircuitBreakerConfig{
			Enabled:            true,
			FailureThreshold:   -1, // Invalid config
			RecoveryTimeoutSec: -1, // Invalid config
		}

		// This should create a disabled circuit breaker instead of failing
		cb := factory.CreateCircuitBreaker("invalid-service", config)
		require.NotNil(t, cb)
		assert.Equal(t, "invalid-service", cb.Name())
	})
}

func TestFactoryExtendedMethods(t *testing.T) {
	t.Parallel()

	factory := NewCircuitBreakerFactory().(*circuitBreakerFactory)
	config := CircuitBreakerConfig{
		Enabled:            true,
		FailureThreshold:   3,
		RecoveryTimeoutSec: 60,
		HalfOpenMaxCalls:   1,
		SuccessThreshold:   1,
	}

	t.Run("GetCircuitBreaker should return existing circuit breaker", func(t *testing.T) {
		// Create a circuit breaker
		cb1 := factory.CreateCircuitBreaker("test-service", config)
		require.NotNil(t, cb1)

		// Get the same circuit breaker
		cb2, exists := factory.GetCircuitBreaker("test-service")
		assert.True(t, exists)
		assert.Equal(t, cb1, cb2)
	})

	t.Run("GetCircuitBreaker should return false for non-existing circuit breaker", func(t *testing.T) {
		cb, exists := factory.GetCircuitBreaker("non-existing-service")
		assert.False(t, exists)
		assert.Nil(t, cb)
	})

	t.Run("GetAllCircuitBreakers should return all created circuit breakers", func(t *testing.T) {
		// Create multiple circuit breakers
		cb1 := factory.CreateCircuitBreaker("service-1", config)
		cb2 := factory.CreateCircuitBreaker("service-2", config)

		allCBs := factory.GetAllCircuitBreakers()
		assert.Len(t, allCBs, 3) // Including the one from previous test
		assert.Contains(t, allCBs, "service-1")
		assert.Contains(t, allCBs, "service-2")
		assert.Equal(t, cb1, allCBs["service-1"])
		assert.Equal(t, cb2, allCBs["service-2"])
	})

	t.Run("ResetAll should reset all circuit breakers", func(t *testing.T) {
		// Create and modify circuit breakers
		cb1 := factory.CreateCircuitBreaker("reset-service-1", config)
		cb2 := factory.CreateCircuitBreaker("reset-service-2", config)

		// Force them to open by causing failures
		for i := 0; i < config.FailureThreshold; i++ {
			_ = cb1.Execute(context.Background(), func(ctx context.Context) error {
				return assert.AnError
			})
			_ = cb2.Execute(context.Background(), func(ctx context.Context) error {
				return assert.AnError
			})
		}

		// Verify they are open
		assert.Equal(t, StateOpen, cb1.GetState())
		assert.Equal(t, StateOpen, cb2.GetState())

		// Reset all
		factory.ResetAll()

		// Verify they are closed
		assert.Equal(t, StateClosed, cb1.GetState())
		assert.Equal(t, StateClosed, cb2.GetState())
	})
}
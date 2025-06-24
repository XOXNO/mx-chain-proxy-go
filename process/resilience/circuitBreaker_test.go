package resilience

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCircuitBreaker(t *testing.T) {
	t.Parallel()

	t.Run("default config should be valid", func(t *testing.T) {
		config := CircuitBreakerConfig{
			Enabled: true,
		}
		cb, err := newCircuitBreaker("test", config)
		require.NoError(t, err)
		require.NotNil(t, cb)
		assert.Equal(t, "test", cb.Name())
		assert.Equal(t, StateClosed, cb.GetState())
	})

	t.Run("custom config should be applied", func(t *testing.T) {
		config := CircuitBreakerConfig{
			Enabled:            true,
			FailureThreshold:   3,
			RecoveryTimeoutSec: 30,
			HalfOpenMaxCalls:   2,
			SuccessThreshold:   1,
		}
		cb, err := newCircuitBreaker("test", config)
		require.NoError(t, err)
		assert.Equal(t, config.FailureThreshold, cb.config.FailureThreshold)
		assert.Equal(t, config.RecoveryTimeoutSec, cb.config.RecoveryTimeoutSec)
		assert.Equal(t, config.HalfOpenMaxCalls, cb.config.HalfOpenMaxCalls)
		assert.Equal(t, config.SuccessThreshold, cb.config.SuccessThreshold)
	})
}

func TestCircuitBreakerStates(t *testing.T) {
	t.Parallel()

	config := CircuitBreakerConfig{
		Enabled:            true,
		FailureThreshold:   2,
		RecoveryTimeoutSec: 1,
		HalfOpenMaxCalls:   1,
		SuccessThreshold:   1,
	}

	t.Run("should start in closed state", func(t *testing.T) {
		cb, err := newCircuitBreaker("test", config)
		require.NoError(t, err)
		assert.Equal(t, StateClosed, cb.GetState())
	})

	t.Run("should open after failure threshold", func(t *testing.T) {
		cb, err := newCircuitBreaker("test", config)
		require.NoError(t, err)

		// First failure
		err = cb.Execute(context.Background(), func(ctx context.Context) error {
			return errors.New("test error")
		})
		assert.Error(t, err)
		assert.Equal(t, StateClosed, cb.GetState())

		// Second failure - should open the circuit
		err = cb.Execute(context.Background(), func(ctx context.Context) error {
			return errors.New("test error")
		})
		assert.Error(t, err)
		assert.Equal(t, StateOpen, cb.GetState())
	})

	t.Run("should reject requests when open", func(t *testing.T) {
		cb, err := newCircuitBreaker("test", config)
		require.NoError(t, err)

		// Force circuit to open
		for i := 0; i < config.FailureThreshold; i++ {
			_ = cb.Execute(context.Background(), func(ctx context.Context) error {
				return errors.New("test error")
			})
		}
		assert.Equal(t, StateOpen, cb.GetState())

		// Next request should be rejected
		err = cb.Execute(context.Background(), func(ctx context.Context) error {
			return nil
		})
		assert.Equal(t, ErrCircuitBreakerOpen, err)
	})

	t.Run("should transition to half-open after recovery timeout", func(t *testing.T) {
		cb, err := newCircuitBreaker("test", config)
		require.NoError(t, err)

		// Force circuit to open
		for i := 0; i < config.FailureThreshold; i++ {
			_ = cb.Execute(context.Background(), func(ctx context.Context) error {
				return errors.New("test error")
			})
		}
		assert.Equal(t, StateOpen, cb.GetState())

		// Wait for recovery timeout
		time.Sleep(time.Duration(config.RecoveryTimeoutSec+1) * time.Second)

		// Next request should transition to half-open
		err = cb.Execute(context.Background(), func(ctx context.Context) error {
			return nil
		})
		assert.NoError(t, err)
		assert.Equal(t, StateClosed, cb.GetState()) // Should close after successful request
	})

	t.Run("should close after successful request in half-open", func(t *testing.T) {
		cb, err := newCircuitBreaker("test", config)
		require.NoError(t, err)

		// Force circuit to open
		for i := 0; i < config.FailureThreshold; i++ {
			_ = cb.Execute(context.Background(), func(ctx context.Context) error {
				return errors.New("test error")
			})
		}

		// Manually transition to half-open
		cb.mutex.Lock()
		cb.transitionToHalfOpen()
		cb.mutex.Unlock()

		// Successful request should close the circuit
		err = cb.Execute(context.Background(), func(ctx context.Context) error {
			return nil
		})
		assert.NoError(t, err)
		assert.Equal(t, StateClosed, cb.GetState())
	})

	t.Run("should reopen on failure in half-open state", func(t *testing.T) {
		cb, err := newCircuitBreaker("test", config)
		require.NoError(t, err)

		// Manually transition to half-open
		cb.mutex.Lock()
		cb.transitionToHalfOpen()
		cb.mutex.Unlock()

		// Failed request should reopen the circuit
		err = cb.Execute(context.Background(), func(ctx context.Context) error {
			return errors.New("test error")
		})
		assert.Error(t, err)
		assert.Equal(t, StateOpen, cb.GetState())
	})
}

func TestCircuitBreakerStats(t *testing.T) {
	t.Parallel()

	config := CircuitBreakerConfig{
		Enabled:            true,
		FailureThreshold:   3,
		RecoveryTimeoutSec: 60,
		HalfOpenMaxCalls:   1,
		SuccessThreshold:   1,
	}

	cb, err := newCircuitBreaker("test", config)
	require.NoError(t, err)

	// Execute successful request
	err = cb.Execute(context.Background(), func(ctx context.Context) error {
		return nil
	})
	assert.NoError(t, err)

	// Execute failed request
	err = cb.Execute(context.Background(), func(ctx context.Context) error {
		return errors.New("test error")
	})
	assert.Error(t, err)

	stats := cb.GetStats()
	assert.Equal(t, StateClosed, stats.State)
	assert.Equal(t, 1, stats.FailureCount)
	assert.Equal(t, 1, stats.SuccessCount)
	assert.Equal(t, 2, stats.TotalRequests)
	assert.False(t, stats.LastFailureTime.IsZero())
	assert.False(t, stats.LastSuccessTime.IsZero())
}

func TestCircuitBreakerReset(t *testing.T) {
	t.Parallel()

	config := CircuitBreakerConfig{
		Enabled:            true,
		FailureThreshold:   2,
		RecoveryTimeoutSec: 60,
		HalfOpenMaxCalls:   1,
		SuccessThreshold:   1,
	}

	cb, err := newCircuitBreaker("test", config)
	require.NoError(t, err)

	// Force circuit to open
	for i := 0; i < config.FailureThreshold; i++ {
		_ = cb.Execute(context.Background(), func(ctx context.Context) error {
			return errors.New("test error")
		})
	}
	assert.Equal(t, StateOpen, cb.GetState())

	// Reset should close the circuit
	cb.Reset()
	assert.Equal(t, StateClosed, cb.GetState())

	stats := cb.GetStats()
	assert.Equal(t, 0, stats.FailureCount)
	assert.Equal(t, 0, stats.SuccessCount)
}

func TestCircuitBreakerDisabled(t *testing.T) {
	t.Parallel()

	config := CircuitBreakerConfig{
		Enabled: false,
	}

	cb, err := newCircuitBreaker("test", config)
	require.NoError(t, err)

	// Execute should always succeed when disabled
	for i := 0; i < 10; i++ {
		err = cb.Execute(context.Background(), func(ctx context.Context) error {
			return errors.New("test error")
		})
		// Error from the function should be returned, but circuit breaker shouldn't interfere
		assert.Error(t, err)
		assert.NotEqual(t, ErrCircuitBreakerOpen, err)
	}

	// State should remain closed
	assert.Equal(t, StateClosed, cb.GetState())
}

func TestCircuitBreakerConcurrency(t *testing.T) {
	t.Parallel()

	config := CircuitBreakerConfig{
		Enabled:            true,
		FailureThreshold:   5,
		RecoveryTimeoutSec: 1,
		HalfOpenMaxCalls:   2,
		SuccessThreshold:   2,
	}

	cb, err := newCircuitBreaker("test", config)
	require.NoError(t, err)

	// Run concurrent operations
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(id int) {
			defer func() { done <- struct{}{} }()
			
			for j := 0; j < 5; j++ {
				_ = cb.Execute(context.Background(), func(ctx context.Context) error {
					if j%2 == 0 {
						return nil
					}
					return errors.New("test error")
				})
				time.Sleep(10 * time.Millisecond)
			}
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify circuit breaker is still functional
	stats := cb.GetStats()
	assert.True(t, stats.TotalRequests > 0)
	assert.True(t, stats.FailureCount >= 0)
	assert.True(t, stats.SuccessCount >= 0)
}
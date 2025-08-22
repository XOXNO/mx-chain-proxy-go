package common

import (
	"errors"
	"testing"
	"time"
)

func TestCircuitBreaker_BasicOperations(t *testing.T) {
	config := CircuitBreakerConfig{
		FailureThreshold:    3,
		RecoveryTimeoutSec:  1,
		HalfOpenMaxCalls:    2,
		SuccessThreshold:    2,
	}

	cb := NewCircuitBreaker(config)

	// Test initial state
	if cb.GetState() != CircuitBreakerStateClosed {
		t.Errorf("Expected initial state to be CLOSED, got %v", cb.GetState())
	}

	// Test successful calls
	err := cb.Call(func() error { return nil })
	if err != nil {
		t.Errorf("Expected successful call, got error: %v", err)
	}

	// Test failure threshold
	for i := 0; i < 3; i++ {
		cb.Call(func() error { return errors.New("test error") })
	}

	if cb.GetState() != CircuitBreakerStateOpen {
		t.Errorf("Expected state to be OPEN after failures, got %v", cb.GetState())
	}

	// Test open state rejects calls
	err = cb.Call(func() error { return nil })
	if err == nil {
		t.Error("Expected circuit breaker to reject call in OPEN state")
	}

	// Test recovery after timeout
	time.Sleep(1200 * time.Millisecond) // Wait for recovery timeout

	err = cb.Call(func() error { return nil })
	if err != nil {
		t.Errorf("Expected call to succeed after recovery timeout, got error: %v", err)
	}

	if cb.GetState() != CircuitBreakerStateHalfOpen {
		t.Errorf("Expected state to be HALF_OPEN after recovery, got %v", cb.GetState())
	}
}

func TestCircuitBreakerManager(t *testing.T) {
	config := CircuitBreakerConfig{
		FailureThreshold:    2,
		RecoveryTimeoutSec:  1,
		HalfOpenMaxCalls:    1,
		SuccessThreshold:    1,
	}

	manager := NewCircuitBreakerManager(config, true)

	// Test observer1 - should work initially
	err := manager.Execute("observer1", func() error { return nil })
	if err != nil {
		t.Errorf("Expected successful execution, got error: %v", err)
	}

	// Test observer1 - trigger failures
	for i := 0; i < 2; i++ {
		manager.Execute("observer1", func() error { return errors.New("test error") })
	}

	// Test observer1 is now blocked
	if manager.CanExecute("observer1") {
		t.Error("Expected observer1 to be blocked after failures")
	}

	// Test observer2 still works
	if !manager.CanExecute("observer2") {
		t.Error("Expected observer2 to still work")
	}

	// Test stats
	stats := manager.GetBreakerStats()
	if len(stats) == 0 {
		t.Error("Expected stats to contain at least one observer")
	}

	if stats["observer1"].State != CircuitBreakerStateOpen {
		t.Errorf("Expected observer1 state to be OPEN, got %v", stats["observer1"].State)
	}
}

func TestCircuitBreakerManager_Disabled(t *testing.T) {
	config := CircuitBreakerConfig{}
	manager := NewCircuitBreakerManager(config, false)

	// Test that disabled manager always allows execution
	if !manager.CanExecute("any-observer") {
		t.Error("Expected disabled circuit breaker to always allow execution")
	}

	err := manager.Execute("any-observer", func() error { return errors.New("test error") })
	if err == nil {
		t.Error("Expected error to be propagated when circuit breaker is disabled")
	}
}
package middleware

import (
	"bytes"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/multiversx/mx-chain-core-go/core/check"
)

type metricsMiddleware struct {
	statusMetricsExtractor StatusMetricsExtractor
}

// NewMetricsMiddleware returns a new instance of metricsMiddleware
func NewMetricsMiddleware(statusMetricsExtractor StatusMetricsExtractor) (*metricsMiddleware, error) {
	if check.IfNil(statusMetricsExtractor) {
		return nil, ErrNilStatusMetricsExtractor
	}

	mm := &metricsMiddleware{
		statusMetricsExtractor: statusMetricsExtractor,
	}

	return mm, nil
}

// MiddlewareHandlerFunc logs updated data in regards to endpoints' durations statistics
func (mm *metricsMiddleware) MiddlewareHandlerFunc() gin.HandlerFunc {
	return func(c *gin.Context) {
		t := time.Now()

		bw := &bodyWriter{body: bytes.NewBufferString(""), ResponseWriter: c.Writer}
		c.Writer = bw

		c.Next()

		duration := time.Since(t)
		status := c.Writer.Status()

		withError := status != http.StatusOK

		mm.statusMetricsExtractor.AddRequestData(c.FullPath(), withError, duration)

		// Record per-function metrics for VM queries
		funcName, hasFuncName := c.Get("vm_func_name")
		scAddress, hasScAddress := c.Get("vm_sc_address")
		if hasFuncName && hasScAddress {
			vmQueryKey := fmt.Sprintf("vm-query:%s@%s", funcName, scAddress)
			mm.statusMetricsExtractor.AddRequestData(vmQueryKey, withError, duration)
		}
	}
}

// IsInterfaceNil returns true if there is no value under the interface
func (mm *metricsMiddleware) IsInterfaceNil() bool {
	return mm == nil
}

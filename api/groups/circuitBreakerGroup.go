package groups

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/multiversx/mx-chain-proxy-go/common"
	"github.com/multiversx/mx-chain-proxy-go/data"
)

type circuitBreakerGroup struct {
	*baseGroup
	circuitBreakerManager *common.CircuitBreakerManager
}

// NewCircuitBreakerGroup returns a new instance of circuitBreakerGroup
func NewCircuitBreakerGroup(baseGroup *baseGroup, circuitBreakerManager *common.CircuitBreakerManager) (*circuitBreakerGroup, error) {
	if baseGroup == nil {
		return nil, errors.New("nil base group provided")
	}

	return &circuitBreakerGroup{
		baseGroup:             baseGroup,
		circuitBreakerManager: circuitBreakerManager,
	}, nil
}

// RegisterRoutes will register all circuit breaker routes
func (cbg *circuitBreakerGroup) RegisterRoutes(
	ws *gin.RouterGroup,
	apiConfig data.ApiRoutesConfig,
) {
	for _, routeConfig := range apiConfig.APIPackages["circuit-breaker"].Routes {
		if !routeConfig.Open {
			log.Debug("circuit breaker route is disabled", "route", routeConfig.Name)
			continue
		}

		switch routeConfig.Name {
		case "get-circuit-breaker-status":
			ws.GET("/circuit-breaker/status", cbg.getCircuitBreakerStatus)
		case "reset-circuit-breaker":
			ws.POST("/circuit-breaker/reset/:observer", cbg.resetCircuitBreaker)
		case "reset-all-circuit-breakers":
			ws.POST("/circuit-breaker/reset-all", cbg.resetAllCircuitBreakers)
		}
	}
}

func (cbg *circuitBreakerGroup) getCircuitBreakerStatus(c *gin.Context) {
	if cbg.circuitBreakerManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Circuit breaker manager not available",
		})
		return
	}

	stats := cbg.circuitBreakerManager.GetBreakerStats()
	
	response := gin.H{
		"enabled": cbg.circuitBreakerManager.IsEnabled(),
		"breakers": make(map[string]gin.H),
	}

	for address, stat := range stats {
		response["breakers"].(map[string]gin.H)[address] = gin.H{
			"state":         stat.State.String(),
			"failure_count": stat.FailureCount,
		}
	}

	c.JSON(http.StatusOK, gin.H{"data": response})
}

func (cbg *circuitBreakerGroup) resetCircuitBreaker(c *gin.Context) {
	if cbg.circuitBreakerManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Circuit breaker manager not available",
		})
		return
	}

	observer := c.Param("observer")
	if observer == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Observer address is required",
		})
		return
	}

	cbg.circuitBreakerManager.ResetBreaker(observer)

	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"message":  "Circuit breaker reset successfully",
			"observer": observer,
		},
	})
}

func (cbg *circuitBreakerGroup) resetAllCircuitBreakers(c *gin.Context) {
	if cbg.circuitBreakerManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Circuit breaker manager not available",
		})
		return
	}

	cbg.circuitBreakerManager.ResetAllBreakers()

	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"message": "All circuit breakers reset successfully",
		},
	})
}
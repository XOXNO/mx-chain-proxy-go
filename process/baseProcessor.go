package process

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/multiversx/mx-chain-core-go/core"
	"github.com/multiversx/mx-chain-core-go/core/check"
	logger "github.com/multiversx/mx-chain-logger-go"
	"github.com/multiversx/mx-chain-proxy-go/common"
	proxyData "github.com/multiversx/mx-chain-proxy-go/data"
	"github.com/multiversx/mx-chain-proxy-go/observer"
)

var log = logger.GetOrCreate("process")
var mutHttpClient sync.RWMutex

const (
	nodeSyncedNonceDifferenceThreshold = 10
	stepDelayForCheckingNodesSyncState = 1 * time.Minute
	timeoutDurationForNodeStatus       = 2 * time.Second
)

// BaseProcessor represents an implementation of CoreProcessor that helps to process requests
type BaseProcessor struct {
	mutState                       sync.RWMutex
	shardCoordinator               common.Coordinator
	observersProvider              observer.NodesProviderHandler
	fullHistoryNodesProvider       observer.NodesProviderHandler
	pubKeyConverter                core.PubkeyConverter
	shardIDs                       []uint32
	nodeStatusFetcher              func(url string) (*proxyData.NodeStatusAPIResponse, int, error)
	chanTriggerNodesState          chan struct{}
	delayForCheckingNodesSyncState time.Duration
	cancelFunc                     func()
	noStatusCheck                  bool
	circuitBreakerManager          *common.CircuitBreakerManager

	httpClient *http.Client
	
	// Observer address to shard mapping for smart failover
	mutObserverToShard    sync.RWMutex
	observerToShardMap    map[string]uint32
}

// NewBaseProcessor creates a new instance of BaseProcessor struct
func NewBaseProcessor(
	requestTimeoutSec int,
	shardCoord common.Coordinator,
	observersProvider observer.NodesProviderHandler,
	fullHistoryNodesProvider observer.NodesProviderHandler,
	pubKeyConverter core.PubkeyConverter,
	noStatusCheck bool,
	circuitBreakerManager *common.CircuitBreakerManager,
) (*BaseProcessor, error) {
	if check.IfNil(shardCoord) {
		return nil, ErrNilShardCoordinator
	}
	if requestTimeoutSec <= 0 {
		return nil, ErrInvalidRequestTimeout
	}
	if check.IfNil(observersProvider) {
		return nil, fmt.Errorf("%w for observers", ErrNilNodesProvider)
	}
	if check.IfNil(fullHistoryNodesProvider) {
		return nil, fmt.Errorf("%w for full history nodes", ErrNilNodesProvider)
	}
	if check.IfNil(pubKeyConverter) {
		return nil, ErrNilPubKeyConverter
	}

	// Create HTTP client with better connection management
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     30 * time.Second,
		DisableKeepAlives:   false,
	}

	httpClient := &http.Client{
		Timeout:   time.Duration(requestTimeoutSec) * time.Second,
		Transport: transport,
	}

	bp := &BaseProcessor{
		shardCoordinator:               shardCoord,
		observersProvider:              observersProvider,
		fullHistoryNodesProvider:       fullHistoryNodesProvider,
		httpClient:                     httpClient,
		pubKeyConverter:                pubKeyConverter,
		shardIDs:                       computeShardIDs(shardCoord),
		delayForCheckingNodesSyncState: stepDelayForCheckingNodesSyncState,
		chanTriggerNodesState:          make(chan struct{}),
		noStatusCheck:                  noStatusCheck,
		circuitBreakerManager:          circuitBreakerManager,
		observerToShardMap:             make(map[string]uint32),
	}
	bp.nodeStatusFetcher = bp.getNodeStatusResponseFromAPI

	if noStatusCheck {
		log.Info("Proxy started with no status check! The provided observers will always be considered synced!")
	}

	// Initialize observer to shard mapping
	bp.refreshObserverToShardMapping()

	return bp, nil
}

// StartNodesSyncStateChecks will simply start the goroutine that handles the nodes sync state
func (bp *BaseProcessor) StartNodesSyncStateChecks() {
	if bp.cancelFunc != nil {
		log.Error("BaseProcessor - cache update already started")
		return
	}

	var ctx context.Context
	ctx, bp.cancelFunc = context.WithCancel(context.Background())

	go bp.handleOutOfSyncNodes(ctx)
}

// GetShardIDs will return the shard IDs slice
func (bp *BaseProcessor) GetShardIDs() []uint32 {
	return bp.shardIDs
}

// ReloadObservers will call the nodes reloading from the observers provider
func (bp *BaseProcessor) ReloadObservers() proxyData.NodesReloadResponse {
	return bp.observersProvider.ReloadNodes(proxyData.Observer)
}

// ReloadFullHistoryObservers will call the nodes reloading from the full history observers provider
func (bp *BaseProcessor) ReloadFullHistoryObservers() proxyData.NodesReloadResponse {
	return bp.fullHistoryNodesProvider.ReloadNodes(proxyData.FullHistoryNode)
}

// GetObservers returns the registered observers on a shard
func (bp *BaseProcessor) GetObservers(shardID uint32, dataAvailability proxyData.ObserverDataAvailabilityType) ([]*proxyData.NodeData, error) {
	return bp.observersProvider.GetNodesByShardId(shardID, dataAvailability)
}

// GetAllObservers will return all the observers, regardless of shard ID
func (bp *BaseProcessor) GetAllObservers(dataAvailability proxyData.ObserverDataAvailabilityType) ([]*proxyData.NodeData, error) {
	return bp.observersProvider.GetAllNodes(dataAvailability)
}

// GetObserversOnePerShard will return a slice containing an observer for each shard
func (bp *BaseProcessor) GetObserversOnePerShard(dataAvailability proxyData.ObserverDataAvailabilityType) ([]*proxyData.NodeData, error) {
	return bp.getNodesOnePerShard(bp.observersProvider.GetNodesByShardId, dataAvailability)
}

// GetFullHistoryNodes returns the registered full history nodes on a shard
func (bp *BaseProcessor) GetFullHistoryNodes(shardID uint32, dataAvailability proxyData.ObserverDataAvailabilityType) ([]*proxyData.NodeData, error) {
	return bp.fullHistoryNodesProvider.GetNodesByShardId(shardID, dataAvailability)
}

// GetAllFullHistoryNodes will return all the full history nodes, regardless of shard ID
func (bp *BaseProcessor) GetAllFullHistoryNodes(dataAvailability proxyData.ObserverDataAvailabilityType) ([]*proxyData.NodeData, error) {
	return bp.fullHistoryNodesProvider.GetAllNodes(dataAvailability)
}

// GetFullHistoryNodesOnePerShard will return a slice containing a full history node for each shard
func (bp *BaseProcessor) GetFullHistoryNodesOnePerShard(dataAvailability proxyData.ObserverDataAvailabilityType) ([]*proxyData.NodeData, error) {
	return bp.getNodesOnePerShard(bp.fullHistoryNodesProvider.GetNodesByShardId, dataAvailability)
}

func (bp *BaseProcessor) getNodesOnePerShard(
	observersInShardGetter func(shardID uint32, dataAvailability proxyData.ObserverDataAvailabilityType) ([]*proxyData.NodeData, error),
	dataAvailability proxyData.ObserverDataAvailabilityType,
) ([]*proxyData.NodeData, error) {
	numShards := bp.shardCoordinator.NumberOfShards()
	sliceToReturn := make([]*proxyData.NodeData, 0)

	for shardID := uint32(0); shardID < numShards; shardID++ {
		observersInShard, err := observersInShardGetter(shardID, dataAvailability)
		if err != nil || len(observersInShard) < 1 {
			continue
		}

		sliceToReturn = append(sliceToReturn, observersInShard[0])
	}

	observersInShardMeta, err := observersInShardGetter(core.MetachainShardId, dataAvailability)
	if err == nil && len(observersInShardMeta) > 0 {
		sliceToReturn = append(sliceToReturn, observersInShardMeta[0])
	}

	if len(sliceToReturn) == 0 {
		return nil, ErrNoObserverAvailable
	}

	return sliceToReturn, nil
}

// ComputeShardId computes the shard id in which the account resides
func (bp *BaseProcessor) ComputeShardId(addressBuff []byte) (uint32, error) {
	bp.mutState.RLock()
	defer bp.mutState.RUnlock()

	return bp.shardCoordinator.ComputeId(addressBuff), nil
}

// CallGetRestEndPoint calls an external end point (sends a request on a node) with circuit breaker support and automatic failover
func (bp *BaseProcessor) CallGetRestEndPoint(
	address string,
	path string,
	value interface{},
) (int, error) {
	// Try the primary observer first
	statusCode, err := bp.executeGetRequest(address, path, value)
	
	// If primary request succeeds, return immediately
	if err == nil && statusCode == http.StatusOK {
		return statusCode, nil
	}

	// If circuit breaker is open or request failed, try failover
	if bp.shouldAttemptFailover(address, err, statusCode) {
		log.Debug("attempting failover for GET request", "failed_observer", address, "path", path, "error", err)
		
		failoverStatusCode, failoverErr := bp.attemptGetFailover(address, path, value)
		if failoverErr == nil {
			return failoverStatusCode, nil
		}
		
		log.Debug("failover also failed", "original_error", err, "failover_error", failoverErr)
		// Return original error, not failover error, to preserve timeout status codes
		return statusCode, err
	}

	// Return original error if no failover succeeded
	return statusCode, err
}

// shouldAttemptFailover determines if failover should be attempted based on error conditions
func (bp *BaseProcessor) shouldAttemptFailover(address string, err error, statusCode int) bool {
	// Don't attempt failover if circuit breaker is disabled
	if bp.circuitBreakerManager == nil {
		return false
	}

	// Attempt failover for circuit breaker open errors
	if err != nil && strings.Contains(err.Error(), "circuit breaker open") {
		return true
	}

	// Attempt failover for timeout errors
	if err != nil && (isTimeoutError(err) || strings.Contains(err.Error(), "timeout")) {
		return true
	}

	// Attempt failover for connection errors
	if err != nil && (strings.Contains(err.Error(), "connection") || strings.Contains(err.Error(), "network")) {
		return true
	}

	// Attempt failover for service unavailable status
	if statusCode == http.StatusServiceUnavailable || statusCode == http.StatusNotFound {
		return true
	}

	return false
}

// attemptGetFailover tries to execute the GET request on alternative observers in the same shard
func (bp *BaseProcessor) attemptGetFailover(failedAddress string, path string, value interface{}) (int, error) {
	// Get the shard ID for the failed observer
	shardID, exists := bp.getShardForObserver(failedAddress)
	if !exists {
		// If we can't determine the shard, try metachain observers
		shardID = core.MetachainShardId
	}

	// Get alternative observers for this shard  
	observers, err := bp.GetObservers(shardID, proxyData.AvailabilityRecent)
	if err != nil {
		return 0, fmt.Errorf("failed to get observers for failover: %w", err)
	}

	// Try each observer except the failed one
	for _, observer := range observers {
		if observer.Address == failedAddress {
			continue // Skip the failed observer
		}

		log.Debug("trying failover observer", "observer", observer.Address, "shard", shardID, "failed_observer", failedAddress)
		
		statusCode, err := bp.executeGetRequest(observer.Address, path, value)
		if err == nil && statusCode == http.StatusOK {
			log.Debug("failover successful", "observer", observer.Address, "shard", shardID)
			return statusCode, nil
		}

		log.Debug("failover observer also failed", "observer", observer.Address, "error", err, "status", statusCode)
	}

	return 0, fmt.Errorf("all observers in shard %d failed for failover", shardID)
}

// executePostRequest performs the actual POST request with circuit breaker protection
func (bp *BaseProcessor) executePostRequest(address string, path string, data interface{}, response interface{}) (int, error) {
	// Check circuit breaker before making request
	if bp.circuitBreakerManager != nil && !bp.circuitBreakerManager.CanExecute(address) {
		bp.triggerNodesSyncCheck(address)
		return http.StatusServiceUnavailable, fmt.Errorf("circuit breaker open for observer %s", address)
	}

	buff, err := json.Marshal(data)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	// Execute request with circuit breaker protection
	var responseStatusCode int
	execErr := bp.executeWithCircuitBreaker(address, func() error {
		req, err := http.NewRequest("POST", address+path, bytes.NewReader(buff))
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}

		userAgent := "Multiversx Proxy / 1.0.0 <Posting to nodes>"
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", userAgent)

		resp, err := bp.httpClient.Do(req)
		if err != nil {
			bp.triggerNodesSyncCheck(address)
			if isTimeoutError(err) {
				return fmt.Errorf("request timeout: %w", err)
			}
			return fmt.Errorf("request failed: %w", err)
		}

		defer func() {
			errNotCritical := resp.Body.Close()
			if errNotCritical != nil {
				log.Warn("base process POST: close body", "error", errNotCritical.Error())
			}
		}()

		responseBodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read response body: %w", err)
		}

		responseStatusCode = resp.StatusCode
		if responseStatusCode == http.StatusOK {
			err = json.Unmarshal(responseBodyBytes, response)
			if err != nil {
				return fmt.Errorf("failed to unmarshal response: %w", err)
			}
			return nil
		}

		// status response not ok, return the error
		genericApiResponse := proxyData.GenericAPIResponse{}
		err = json.Unmarshal(responseBodyBytes, &genericApiResponse)
		if err != nil {
			return fmt.Errorf("non-OK response status %d, error unmarshaling response: %w", responseStatusCode, err)
		}

		return fmt.Errorf("non-OK response status %d: %s", responseStatusCode, genericApiResponse.Error)
	})

	if execErr != nil {
		if responseStatusCode == 0 {
			// Check if it's a timeout error
			if isTimeoutError(execErr) {
				return http.StatusRequestTimeout, execErr
			}
			// If we never got a response, assume it's a connection issue
			return http.StatusNotFound, execErr
		}
		return responseStatusCode, execErr
	}

	return http.StatusOK, nil
}

// attemptPostFailover tries to execute the POST request on alternative observers in the same shard
func (bp *BaseProcessor) attemptPostFailover(failedAddress string, path string, data interface{}, response interface{}) (int, error) {
	// Get the shard ID for the failed observer
	shardID, exists := bp.getShardForObserver(failedAddress)
	if !exists {
		// If we can't determine the shard, try metachain observers
		shardID = core.MetachainShardId
	}

	// Get alternative observers for this shard  
	observers, err := bp.GetObservers(shardID, proxyData.AvailabilityRecent)
	if err != nil {
		return 0, fmt.Errorf("failed to get observers for failover: %w", err)
	}

	// Try each observer except the failed one
	for _, observer := range observers {
		if observer.Address == failedAddress {
			continue // Skip the failed observer
		}

		log.Debug("trying failover observer", "observer", observer.Address, "shard", shardID, "failed_observer", failedAddress)
		
		statusCode, err := bp.executePostRequest(observer.Address, path, data, response)
		if err == nil && statusCode == http.StatusOK {
			log.Debug("failover successful", "observer", observer.Address, "shard", shardID)
			return statusCode, nil
		}

		log.Debug("failover observer also failed", "observer", observer.Address, "error", err, "status", statusCode)
	}

	return 0, fmt.Errorf("all observers in shard %d failed for failover", shardID)
}

// executeGetRequest performs the actual GET request with circuit breaker protection
func (bp *BaseProcessor) executeGetRequest(address string, path string, value interface{}) (int, error) {
	// Check circuit breaker before making request
	if bp.circuitBreakerManager != nil && !bp.circuitBreakerManager.CanExecute(address) {
		bp.triggerNodesSyncCheck(address)
		return http.StatusServiceUnavailable, fmt.Errorf("circuit breaker open for observer %s", address)
	}

	// Execute request with circuit breaker protection
	var responseStatusCode int
	execErr := bp.executeWithCircuitBreaker(address, func() error {
		req, err := http.NewRequest("GET", address+path, nil)
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}

		userAgent := "Multiversx Proxy / 1.0.0 <Requesting data from nodes>"
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", userAgent)

		resp, err := bp.httpClient.Do(req)
		if err != nil {
			bp.triggerNodesSyncCheck(address)
			if isTimeoutError(err) {
				return fmt.Errorf("request timeout: %w", err)
			}
			return fmt.Errorf("request failed: %w", err)
		}

		defer func() {
			errNotCritical := resp.Body.Close()
			if errNotCritical != nil {
				log.Warn("base process GET: close body", "error", errNotCritical.Error())
			}
		}()

		responseBodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read response body: %w", err)
		}

		responseStatusCode = resp.StatusCode
		if responseStatusCode != http.StatusOK {
			return fmt.Errorf("non-OK response status %d: %s", responseStatusCode, string(responseBodyBytes))
		}

		err = json.Unmarshal(responseBodyBytes, value)
		if err != nil {
			return fmt.Errorf("failed to unmarshal response: %w", err)
		}

		return nil
	})

	if execErr != nil {
		if responseStatusCode == 0 {
			// Check if it's a timeout error
			if isTimeoutError(execErr) {
				return http.StatusRequestTimeout, execErr
			}
			// If we never got a response, assume it's a connection issue
			return http.StatusNotFound, execErr
		}
		return responseStatusCode, execErr
	}

	return http.StatusOK, nil
}

// CallPostRestEndPoint calls an external end point (sends a request on a node) with circuit breaker support
func (bp *BaseProcessor) CallPostRestEndPoint(
	address string,
	path string,
	data interface{},
	response interface{},
) (int, error) {
	// Try the primary observer first
	statusCode, err := bp.executePostRequest(address, path, data, response)
	
	// If primary request succeeds, return immediately
	if err == nil && statusCode == http.StatusOK {
		return statusCode, nil
	}

	// If circuit breaker is open or request failed, try failover
	if bp.shouldAttemptFailover(address, err, statusCode) {
		log.Debug("attempting failover for POST request", "failed_observer", address, "path", path, "error", err)
		
		failoverStatusCode, failoverErr := bp.attemptPostFailover(address, path, data, response)
		if failoverErr == nil {
			return failoverStatusCode, nil
		}
		
		log.Debug("failover also failed", "original_error", err, "failover_error", failoverErr)
		// Return original error, not failover error, to preserve timeout status codes
		return statusCode, err
	}

	// Return original error if no failover succeeded
	return statusCode, err
}

// executeWithCircuitBreaker executes a function with circuit breaker protection
func (bp *BaseProcessor) executeWithCircuitBreaker(address string, fn func() error) error {
	if bp.circuitBreakerManager == nil {
		return fn()
	}

	return bp.circuitBreakerManager.Execute(address, fn)
}


func (bp *BaseProcessor) triggerNodesSyncCheck(address string) {
	log.Info("triggering nodes state checks because of an offline node", "address of offline node", address)
	select {
	case bp.chanTriggerNodesState <- struct{}{}:
	default:
	}
}

func isTimeoutError(err error) bool {
	if err, ok := err.(net.Error); ok && err.Timeout() {
		return true
	}

	return false
}

// isConnectionError checks if the error is a connection-related issue
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}

	// Check for network timeout
	if isTimeoutError(err) {
		return true
	}

	// Check for connection refused, host unreachable, etc.
	errorStr := err.Error()
	connectionErrors := []string{
		"connection refused",
		"connection reset",
		"no route to host",
		"network unreachable",
		"host unreachable",
		"no such host",
		"timeout",
	}

	for _, connErr := range connectionErrors {
		if strings.Contains(strings.ToLower(errorStr), connErr) {
			return true
		}
	}

	return false
}

// GetShardCoordinator returns the shard coordinator
func (bp *BaseProcessor) GetShardCoordinator() common.Coordinator {
	return bp.shardCoordinator
}

// GetPubKeyConverter returns the public key converter
func (bp *BaseProcessor) GetPubKeyConverter() core.PubkeyConverter {
	return bp.pubKeyConverter
}

// GetObserverProvider returns the observers provider
func (bp *BaseProcessor) GetObserverProvider() observer.NodesProviderHandler {
	return bp.observersProvider
}

// GetFullHistoryNodesProvider returns the full history nodes provider object
func (bp *BaseProcessor) GetFullHistoryNodesProvider() observer.NodesProviderHandler {
	return bp.fullHistoryNodesProvider
}

func computeShardIDs(shardCoordinator common.Coordinator) []uint32 {
	shardIDs := make([]uint32, 0)
	for i := uint32(0); i < shardCoordinator.NumberOfShards(); i++ {
		shardIDs = append(shardIDs, i)
	}

	shardIDs = append(shardIDs, core.MetachainShardId)

	return shardIDs
}

// refreshObserverToShardMapping updates the mapping of observer addresses to shard IDs
func (bp *BaseProcessor) refreshObserverToShardMapping() {
	bp.mutObserverToShard.Lock()
	defer bp.mutObserverToShard.Unlock()

	// Clear existing mapping
	bp.observerToShardMap = make(map[string]uint32)

	// Populate mapping for all shards
	for _, shardID := range bp.shardIDs {
		observers, err := bp.observersProvider.GetNodesByShardId(shardID, proxyData.AvailabilityAll)
		if err != nil {
			log.Debug("failed to get observers for shard mapping", "shard", shardID, "error", err)
			continue
		}

		for _, observer := range observers {
			bp.observerToShardMap[observer.Address] = shardID
		}
	}

	log.Debug("refreshed observer to shard mapping", "total_observers", len(bp.observerToShardMap))
}

// getShardForObserver returns the shard ID for a given observer address
func (bp *BaseProcessor) getShardForObserver(observerAddress string) (uint32, bool) {
	bp.mutObserverToShard.RLock()
	defer bp.mutObserverToShard.RUnlock()

	shardID, exists := bp.observerToShardMap[observerAddress]
	return shardID, exists
}

func (bp *BaseProcessor) handleOutOfSyncNodes(ctx context.Context) {
	timer := time.NewTimer(bp.delayForCheckingNodesSyncState)
	defer timer.Stop()

	// Circuit breaker cleanup timer - runs every 10 minutes
	cleanupTimer := time.NewTimer(10 * time.Minute)
	defer cleanupTimer.Stop()

	bp.handleNodes()
	for {
		timer.Reset(bp.delayForCheckingNodesSyncState)

		select {
		case <-timer.C:
			bp.handleNodes()
		case <-bp.chanTriggerNodesState:
			bp.handleNodes()
		case <-cleanupTimer.C:
			if bp.circuitBreakerManager != nil {
				bp.circuitBreakerManager.CleanupStaleBreakers()
			}
			cleanupTimer.Reset(10 * time.Minute)
		case <-ctx.Done():
			log.Info("finishing BaseProcessor nodes state update...")
			return
		}
	}
}

func (bp *BaseProcessor) handleNodes() {
	// if proxy is started with no-status-check flag, only print the observers.
	// they are already initialized by default as synced.
	if bp.noStatusCheck {
		bp.observersProvider.PrintNodesInShards()
		bp.fullHistoryNodesProvider.PrintNodesInShards()
		return
	}

	bp.updateNodesWithSync()
	
	// Refresh observer to shard mapping after node updates
	bp.refreshObserverToShardMapping()
}

func (bp *BaseProcessor) updateNodesWithSync() {
	observers := bp.observersProvider.GetAllNodesWithSyncState()
	observersWithSyncStatus := bp.getNodesWithSyncStatus(observers)
	bp.observersProvider.UpdateNodesBasedOnSyncState(observersWithSyncStatus)

	fullHistoryNodes := bp.fullHistoryNodesProvider.GetAllNodesWithSyncState()
	fullHistoryNodesWithSyncStatus := bp.getNodesWithSyncStatus(fullHistoryNodes)
	bp.fullHistoryNodesProvider.UpdateNodesBasedOnSyncState(fullHistoryNodesWithSyncStatus)
}

func (bp *BaseProcessor) getNodesWithSyncStatus(nodes []*proxyData.NodeData) []*proxyData.NodeData {
	nodesToReturn := make([]*proxyData.NodeData, 0)
	for _, node := range nodes {
		isSynced, err := bp.isNodeSynced(node)
		if err != nil {
			log.Warn("cannot get node status. will mark as inactive", "address", node.Address, "error", err)
			isSynced = false
		}

		node.IsSynced = isSynced
		nodesToReturn = append(nodesToReturn, node)
	}

	return nodesToReturn
}

func (bp *BaseProcessor) isNodeSynced(node *proxyData.NodeData) (bool, error) {
	nodeStatusResponse, httpCode, err := bp.nodeStatusFetcher(node.Address)
	if err != nil {
		return false, err
	}
	if httpCode != http.StatusOK {
		return false, fmt.Errorf("observer %s responded with code %d", node.Address, httpCode)
	}

	nonce := nodeStatusResponse.Data.Metrics.Nonce
	probableHighestNonce := nodeStatusResponse.Data.Metrics.ProbableHighestNonce
	isReadyForVMQueries := parseBool(nodeStatusResponse.Data.Metrics.AreVmQueriesReady)

	// In some cases, the probableHighestNonce can be lower than the nonce. In this case we consider the node as synced
	// as the nonce metric can be updated faster than the other one
	probableHighestNonceLessThanOrEqualToNonce := probableHighestNonce <= nonce

	// In normal conditions, the node's nonce should be equal to or very close to the probable highest nonce
	nonceDifferenceBelowThreshold := probableHighestNonce-nonce < nodeSyncedNonceDifferenceThreshold

	// If any of the above 2 conditions are met, the node is considered synced
	isNodeSynced := nonceDifferenceBelowThreshold || probableHighestNonceLessThanOrEqualToNonce

	log.Info("node status",
		"address", node.Address,
		"shard", node.ShardId,
		"nonce", nonce,
		"probable highest nonce", probableHighestNonce,
		"is synced", isNodeSynced,
		"is ready for VM Queries", isReadyForVMQueries,
		"is snapshotless", node.IsSnapshotless,
		"is fallback", node.IsFallback)

	if !isReadyForVMQueries {
		isNodeSynced = false
	}

	return isNodeSynced, nil
}

// checkNodeStatus returns both sync status and reachability status
func (bp *BaseProcessor) checkNodeStatus(node *proxyData.NodeData) (bool, bool, error) {
	nodeStatusResponse, httpCode, err := bp.nodeStatusFetcher(node.Address)
	if err != nil {
		// Categorize the error
		if isConnectionError(err) {
			// Node is unreachable due to connection issues
			log.Debug("node unreachable due to connection error", "address", node.Address, "error", err)
			return false, false, err
		}
		// Other errors (e.g., parsing errors) - node might be reachable but problematic
		log.Debug("node error (non-connection)", "address", node.Address, "error", err)
		return false, true, err
	}
	if httpCode != http.StatusOK {
		// Node is reachable but returned error status
		return false, true, fmt.Errorf("observer %s responded with code %d", node.Address, httpCode)
	}

	nonce := nodeStatusResponse.Data.Metrics.Nonce
	probableHighestNonce := nodeStatusResponse.Data.Metrics.ProbableHighestNonce
	isReadyForVMQueries := parseBool(nodeStatusResponse.Data.Metrics.AreVmQueriesReady)

	// In some cases, the probableHighestNonce can be lower than the nonce. In this case we consider the node as synced
	// as the nonce metric can be updated faster than the other one
	probableHighestNonceLessThanOrEqualToNonce := probableHighestNonce <= nonce

	// In normal conditions, the node's nonce should be equal to or very close to the probable highest nonce
	nonceDifferenceBelowThreshold := probableHighestNonce-nonce < nodeSyncedNonceDifferenceThreshold

	// If any of the above 2 conditions are met, the node is considered synced
	isNodeSynced := nonceDifferenceBelowThreshold || probableHighestNonceLessThanOrEqualToNonce

	log.Info("node status",
		"address", node.Address,
		"shard", node.ShardId,
		"nonce", nonce,
		"probable highest nonce", probableHighestNonce,
		"is synced", isNodeSynced,
		"is reachable", true,
		"is ready for VM Queries", isReadyForVMQueries,
		"is snapshotless", node.IsSnapshotless,
		"is fallback", node.IsFallback)

	if !isReadyForVMQueries {
		isNodeSynced = false
	}

	return isNodeSynced, true, nil
}

func (bp *BaseProcessor) getNodeStatusResponseFromAPI(url string) (*proxyData.NodeStatusAPIResponse, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeoutDurationForNodeStatus)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/node/status", nil)
	if err != nil {
		return nil, http.StatusNotFound, err
	}

	resp, err := bp.httpClient.Do(req)
	if err != nil {
		return nil, http.StatusNotFound, err
	}

	defer func() {
		if resp != nil && resp.Body != nil {
			log.LogIfError(resp.Body.Close())
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, nil
	}

	responseBodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}

	var nodeStatusResponse proxyData.NodeStatusAPIResponse

	err = json.Unmarshal(responseBodyBytes, &nodeStatusResponse)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}

	return &nodeStatusResponse, resp.StatusCode, nil
}

func parseBool(metricValue string) bool {
	return strconv.FormatBool(true) == metricValue
}

// IsInterfaceNil returns true if there is no value under the interface
func (bp *BaseProcessor) IsInterfaceNil() bool {
	return bp == nil
}

// Close will handle the closing of the cache update go routine
func (bp *BaseProcessor) Close() error {
	if bp.cancelFunc != nil {
		bp.cancelFunc()
	}

	return nil
}

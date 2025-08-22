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
	}
	bp.nodeStatusFetcher = bp.getNodeStatusResponseFromAPI

	if noStatusCheck {
		log.Info("Proxy started with no status check! The provided observers will always be considered synced!")
	}

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

// CallGetRestEndPoint calls an external end point (sends a request on a node) with circuit breaker support
func (bp *BaseProcessor) CallGetRestEndPoint(
	address string,
	path string,
	value interface{},
) (int, error) {
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
			// If we never got a response, assume it's a connection issue
			return http.StatusNotFound, execErr
		}
		return responseStatusCode, execErr
	}

	return http.StatusOK, nil
}

// executeWithCircuitBreaker executes a function with circuit breaker protection
func (bp *BaseProcessor) executeWithCircuitBreaker(address string, fn func() error) error {
	if bp.circuitBreakerManager == nil {
		return fn()
	}

	return bp.circuitBreakerManager.Execute(address, fn)
}

// CallGetRestEndPointWithFailover calls observers with automatic failover on failures
func (bp *BaseProcessor) CallGetRestEndPointWithFailover(
	shardID uint32,
	path string,
	value interface{},
	dataAvailability proxyData.ObserverDataAvailabilityType,
) (int, error) {
	observers, err := bp.GetObservers(shardID, dataAvailability)
	if err != nil {
		return http.StatusServiceUnavailable, fmt.Errorf("failed to get observers for shard %d: %w", shardID, err)
	}

	var lastErr error
	for _, observer := range observers {
		// Skip observers with open circuit breakers
		if bp.circuitBreakerManager != nil && !bp.circuitBreakerManager.CanExecute(observer.Address) {
			log.Debug("skipping observer with open circuit breaker", "address", observer.Address)
			continue
		}

		statusCode, err := bp.CallGetRestEndPoint(observer.Address, path, value)
		if err == nil {
			log.Debug("successful request to observer", "address", observer.Address, "path", path)
			return statusCode, nil
		}

		lastErr = err
		log.Debug("failed request to observer, trying next", "address", observer.Address, "path", path, "error", err.Error())
	}

	if lastErr != nil {
		return http.StatusServiceUnavailable, fmt.Errorf("all observers failed for shard %d: %w", shardID, lastErr)
	}

	return http.StatusServiceUnavailable, fmt.Errorf("no observers available for shard %d", shardID)
}

// CallPostRestEndPointWithFailover calls observers with automatic failover on failures
func (bp *BaseProcessor) CallPostRestEndPointWithFailover(
	shardID uint32,
	path string,
	data interface{},
	response interface{},
	dataAvailability proxyData.ObserverDataAvailabilityType,
) (int, error) {
	observers, err := bp.GetObservers(shardID, dataAvailability)
	if err != nil {
		return http.StatusServiceUnavailable, fmt.Errorf("failed to get observers for shard %d: %w", shardID, err)
	}

	var lastErr error
	for _, observer := range observers {
		// Skip observers with open circuit breakers
		if bp.circuitBreakerManager != nil && !bp.circuitBreakerManager.CanExecute(observer.Address) {
			log.Debug("skipping observer with open circuit breaker", "address", observer.Address)
			continue
		}

		statusCode, err := bp.CallPostRestEndPoint(observer.Address, path, data, response)
		if err == nil {
			log.Debug("successful request to observer", "address", observer.Address, "path", path)
			return statusCode, nil
		}

		lastErr = err
		log.Debug("failed request to observer, trying next", "address", observer.Address, "path", path, "error", err.Error())
	}

	if lastErr != nil {
		return http.StatusServiceUnavailable, fmt.Errorf("all observers failed for shard %d: %w", shardID, lastErr)
	}

	return http.StatusServiceUnavailable, fmt.Errorf("no observers available for shard %d", shardID)
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

package process

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
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

const (
	nodeSyncedNonceDifferenceThreshold      = 10
	crossShardNonceDifferenceThreshold      = 100
	stepDelayForCheckingNodesSyncState      = 1 * time.Minute
	timeoutDurationForNodeStatus            = 2 * time.Second
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

	httpClient        *http.Client
	requestTimeoutSec int
}

// NewBaseProcessor creates a new instance of BaseProcessor struct
func NewBaseProcessor(
	requestTimeoutSec int,
	shardCoord common.Coordinator,
	observersProvider observer.NodesProviderHandler,
	fullHistoryNodesProvider observer.NodesProviderHandler,
	pubKeyConverter core.PubkeyConverter,
	noStatusCheck bool,
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

	httpClient := newHTTPClient(requestTimeoutSec)

	bp := &BaseProcessor{
		shardCoordinator:               shardCoord,
		observersProvider:              observersProvider,
		fullHistoryNodesProvider:       fullHistoryNodesProvider,
		httpClient:                     httpClient,
		requestTimeoutSec:              requestTimeoutSec,
		pubKeyConverter:                pubKeyConverter,
		shardIDs:                       computeShardIDs(shardCoord),
		delayForCheckingNodesSyncState: stepDelayForCheckingNodesSyncState,
		chanTriggerNodesState:          make(chan struct{}),
		noStatusCheck:                  noStatusCheck,
	}
	bp.nodeStatusFetcher = bp.getNodeStatusResponseFromAPI

	if noStatusCheck {
		log.Info("Proxy started with no status check! The provided observers will always be considered synced!")
	}

	return bp, nil
}

func newHTTPClient(requestTimeoutSec int) *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}

	return &http.Client{
		Timeout:   time.Duration(requestTimeoutSec) * time.Second,
		Transport: transport,
	}
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

// CallGetRestEndPoint calls an external end point (sends a request on a node)
func (bp *BaseProcessor) CallGetRestEndPoint(
	address string,
	path string,
	value interface{},
) (int, error) {

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(bp.requestTimeoutSec)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address+path, nil)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	userAgent := "Multiversx Proxy / 1.0.0 <Requesting data from nodes>"
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := bp.httpClient.Do(req)
	if err != nil {
		bp.triggerNodesSyncCheck(address)
		if isTimeoutError(err) {
			return http.StatusRequestTimeout, err
		}

		return http.StatusNotFound, err
	}

	defer func() {
		errNotCritical := resp.Body.Close()
		if errNotCritical != nil {
			log.Warn("base process GET: close body", "error", errNotCritical.Error())
		}
	}()

	responseBodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	err = json.Unmarshal(responseBodyBytes, value)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	responseStatusCode := resp.StatusCode
	if responseStatusCode == http.StatusOK { // everything ok, return status ok and the expected response
		return responseStatusCode, nil
	}

	// status response not ok, return the error
	return responseStatusCode, errors.New(string(responseBodyBytes))
}

// CallPostRestEndPoint calls an external end point (sends a request on a node)
func (bp *BaseProcessor) CallPostRestEndPoint(
	address string,
	path string,
	data interface{},
	response interface{},
) (int, error) {

	buff, err := json.Marshal(data)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(bp.requestTimeoutSec)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address+path, bytes.NewReader(buff))
	if err != nil {
		return http.StatusInternalServerError, err
	}

	userAgent := "Multiversx Proxy / 1.0.0 <Posting to nodes>"
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := bp.httpClient.Do(req)
	if err != nil {
		bp.triggerNodesSyncCheck(address)
		if isTimeoutError(err) {
			return http.StatusRequestTimeout, err
		}

		return http.StatusNotFound, err
	}

	defer func() {
		errNotCritical := resp.Body.Close()
		if errNotCritical != nil {
			log.Warn("base process POST: close body", "error", errNotCritical.Error())
		}
	}()

	responseBodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	responseStatusCode := resp.StatusCode
	if responseStatusCode == http.StatusOK { // everything ok, return status ok and the expected response
		return responseStatusCode, json.Unmarshal(responseBodyBytes, response)
	}

	// status response not ok, return the error
	genericApiResponse := proxyData.GenericAPIResponse{}
	err = json.Unmarshal(responseBodyBytes, &genericApiResponse)
	if err != nil {
		return responseStatusCode, fmt.Errorf("error unmarshaling response: %w", err)
	}

	return responseStatusCode, errors.New(genericApiResponse.Error)
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

	bp.handleNodes()
	for {
		timer.Reset(bp.delayForCheckingNodesSyncState)

		select {
		case <-timer.C:
		case <-bp.chanTriggerNodesState:
		case <-ctx.Done():
			log.Info("finishing BaseProcessor nodes state update...")
			return
		}

		bp.handleNodes()
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
	bp.crossValidateNodesByNonce(observersWithSyncStatus)
	bp.observersProvider.UpdateNodesBasedOnSyncState(observersWithSyncStatus)

	fullHistoryNodes := bp.fullHistoryNodesProvider.GetAllNodesWithSyncState()
	fullHistoryNodesWithSyncStatus := bp.getNodesWithSyncStatus(fullHistoryNodes)
	bp.crossValidateNodesByNonce(fullHistoryNodesWithSyncStatus)
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

// crossValidateNodesByNonce compares nonces between nodes in the same shard
// and marks nodes as out-of-sync if they're significantly behind the highest nonce in their shard
func (bp *BaseProcessor) crossValidateNodesByNonce(nodes []*proxyData.NodeData) {
	// Group nodes by shard and find highest nonce per shard
	highestNoncePerShard := make(map[uint32]uint64)
	for _, node := range nodes {
		if !node.IsSynced {
			continue
		}
		if node.Nonce > highestNoncePerShard[node.ShardId] {
			highestNoncePerShard[node.ShardId] = node.Nonce
		}
	}

	// Mark nodes as out-of-sync if they're significantly behind the highest nonce in their shard
	for _, node := range nodes {
		if !node.IsSynced {
			continue
		}
		highestNonce := highestNoncePerShard[node.ShardId]
		if highestNonce > node.Nonce && highestNonce-node.Nonce > crossShardNonceDifferenceThreshold {
			log.Warn("node is behind other nodes in same shard, marking as out-of-sync",
				"address", node.Address,
				"shard", node.ShardId,
				"nonce", node.Nonce,
				"highest nonce in shard", highestNonce,
				"difference", highestNonce-node.Nonce)
			node.IsSynced = false
		}
	}
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

	// Store nonce for cross-shard validation
	node.Nonce = nonce

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

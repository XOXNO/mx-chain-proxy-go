package process_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multiversx/mx-chain-core-go/core"
	"github.com/multiversx/mx-chain-core-go/core/sharding"
	"github.com/multiversx/mx-chain-proxy-go/data"
	"github.com/multiversx/mx-chain-proxy-go/process"
	"github.com/multiversx/mx-chain-proxy-go/process/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testStruct struct {
	Nonce int
	Name  string
}

func createTestHttpServer(
	matchingPath string,
	response []byte,
) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if req.Method == "GET" {
			if req.URL.String() == matchingPath {
				_, _ = rw.Write(response)
			}
		}

		if req.Method == "POST" {
			buf := new(bytes.Buffer)
			_, _ = buf.ReadFrom(req.Body)
			_, _ = rw.Write(buf.Bytes())
		}
	}))
}

func TestNewBaseProcessor_WithInvalidRequestTimeoutShouldErr(t *testing.T) {
	t.Parallel()

	bp, err := process.NewBaseProcessor(
		-5,
		&mock.ShardCoordinatorMock{},
		&mock.ObserversProviderStub{},
		&mock.ObserversProviderStub{},
		&mock.PubKeyConverterMock{},
		false,
	)

	assert.Nil(t, bp)
	assert.Equal(t, process.ErrInvalidRequestTimeout, err)
}

func TestNewBaseProcessor_WithNilShardCoordinatorShouldErr(t *testing.T) {
	t.Parallel()

	bp, err := process.NewBaseProcessor(
		5,
		nil,
		&mock.ObserversProviderStub{},
		&mock.ObserversProviderStub{},
		&mock.PubKeyConverterMock{},
		false,
	)

	assert.Nil(t, bp)
	assert.Equal(t, process.ErrNilShardCoordinator, err)
}

func TestNewBaseProcessor_WithNilObserversProviderShouldErr(t *testing.T) {
	t.Parallel()

	bp, err := process.NewBaseProcessor(
		5,
		&mock.ShardCoordinatorMock{},
		&mock.ObserversProviderStub{},
		nil,
		&mock.PubKeyConverterMock{},
		false,
	)

	assert.Nil(t, bp)
	assert.True(t, errors.Is(err, process.ErrNilNodesProvider))
}

func TestNewBaseProcessor_WithNilFullHistoryNodesProviderShouldErr(t *testing.T) {
	t.Parallel()

	bp, err := process.NewBaseProcessor(
		5,
		&mock.ShardCoordinatorMock{},
		nil,
		&mock.ObserversProviderStub{},
		&mock.PubKeyConverterMock{},
		false,
	)

	assert.Nil(t, bp)
	assert.True(t, errors.Is(err, process.ErrNilNodesProvider))
}

func TestNewBaseProcessor_WithOkValuesShouldWork(t *testing.T) {
	t.Parallel()

	bp, err := process.NewBaseProcessor(
		5,
		&mock.ShardCoordinatorMock{},
		&mock.ObserversProviderStub{},
		&mock.ObserversProviderStub{},
		&mock.PubKeyConverterMock{},
		false,
	)

	assert.NotNil(t, bp)
	assert.Nil(t, err)
}

//------- GetObservers

func TestBaseProcessor_GetObserversEmptyListShouldWork(t *testing.T) {
	t.Parallel()

	observersSlice := []*data.NodeData{{Address: "addr1"}}
	bp, _ := process.NewBaseProcessor(
		5,
		&mock.ShardCoordinatorMock{},
		&mock.ObserversProviderStub{
			GetNodesByShardIdCalled: func(_ uint32, _ data.ObserverDataAvailabilityType) ([]*data.NodeData, error) {
				return observersSlice, nil
			},
		},
		&mock.ObserversProviderStub{},
		&mock.PubKeyConverterMock{},
		false,
	)
	observers, err := bp.GetObservers(0, data.AvailabilityAll)

	assert.Nil(t, err)
	assert.Equal(t, observersSlice, observers)
}

//------- ComputeShardId

func TestBaseProcessor_ComputeShardId(t *testing.T) {
	t.Parallel()

	observersList := []*data.NodeData{
		{
			Address: "address1",
			ShardId: 0,
		},
		{
			Address: "address2",
			ShardId: 1,
		},
	}

	msc, _ := sharding.NewMultiShardCoordinator(3, 0)
	bp, _ := process.NewBaseProcessor(
		5,
		msc,
		&mock.ObserversProviderStub{
			GetNodesByShardIdCalled: func(_ uint32, _ data.ObserverDataAvailabilityType) ([]*data.NodeData, error) {
				return observersList, nil
			},
		},
		&mock.ObserversProviderStub{},
		&mock.PubKeyConverterMock{},
		false,
	)

	//there are 2 shards, compute ID should correctly process
	addressInShard0 := []byte{0}
	shardID, err := bp.ComputeShardId(addressInShard0)
	assert.Nil(t, err)
	assert.Equal(t, uint32(0), shardID)

	addressInShard1 := []byte{1}
	shardID, err = bp.ComputeShardId(addressInShard1)
	assert.Nil(t, err)
	assert.Equal(t, uint32(1), shardID)
}

//------- Calls

func TestBaseProcessor_CallGetRestEndPoint(t *testing.T) {
	ts := &testStruct{
		Nonce: 10000,
		Name:  "a test struct to be sent and received",
	}
	response, _ := json.Marshal(ts)

	server := createTestHttpServer("/some/path", response)
	fmt.Printf("Server: %s\n", server.URL)
	defer server.Close()

	tsRecovered := &testStruct{}
	bp, _ := process.NewBaseProcessor(
		5,
		&mock.ShardCoordinatorMock{},
		&mock.ObserversProviderStub{},
		&mock.ObserversProviderStub{},
		&mock.PubKeyConverterMock{},
		false,
	)
	_, err := bp.CallGetRestEndPoint(server.URL, "/some/path", tsRecovered)

	assert.Nil(t, err)
	assert.Equal(t, ts, tsRecovered)
}

func TestBaseProcessor_CallGetRestEndPointShouldTimeout(t *testing.T) {
	ts := &testStruct{
		Nonce: 10000,
		Name:  "a test struct to be sent and received",
	}
	response, _ := json.Marshal(ts)

	testServer := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		time.Sleep(1200 * time.Millisecond)
		_, _ = rw.Write(response)
	}))
	fmt.Printf("Server: %s\n", testServer.URL)
	defer testServer.Close()

	tsRecovered := &testStruct{}
	bp, _ := process.NewBaseProcessor(
		1,
		&mock.ShardCoordinatorMock{},
		&mock.ObserversProviderStub{},
		&mock.ObserversProviderStub{},
		&mock.PubKeyConverterMock{},
		false,
	)
	_, err := bp.CallGetRestEndPoint(testServer.URL, "/some/path", tsRecovered)

	assert.NotEqual(t, ts.Name, tsRecovered.Name)
	assert.NotNil(t, err)
}

func TestBaseProcessor_CallPostRestEndPoint(t *testing.T) {
	ts := &testStruct{
		Nonce: 10000,
		Name:  "a test struct to be sent",
	}
	tsRecv := &testStruct{}

	server := createTestHttpServer("/some/path", nil)
	fmt.Printf("Server: %s\n", server.URL)
	defer server.Close()

	bp, _ := process.NewBaseProcessor(
		5,
		&mock.ShardCoordinatorMock{},
		&mock.ObserversProviderStub{},
		&mock.ObserversProviderStub{},
		&mock.PubKeyConverterMock{},
		false,
	)
	rc, err := bp.CallPostRestEndPoint(server.URL, "/some/path", ts, tsRecv)

	assert.Nil(t, err)
	assert.Equal(t, ts, tsRecv)
	assert.Equal(t, http.StatusOK, rc)
}

func TestBaseProcessor_CallPostRestEndPointShouldTimeout(t *testing.T) {
	ts := &testStruct{
		Nonce: 10000,
		Name:  "a test struct to be sent",
	}
	tsRecv := &testStruct{}

	testServer := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		time.Sleep(1200 * time.Millisecond)
		tsBytes, _ := json.Marshal(ts)
		_, _ = rw.Write(tsBytes)
	}))

	fmt.Printf("Server: %s\n", testServer.URL)
	defer testServer.Close()

	bp, _ := process.NewBaseProcessor(
		1,
		&mock.ShardCoordinatorMock{},
		&mock.ObserversProviderStub{},
		&mock.ObserversProviderStub{},
		&mock.PubKeyConverterMock{},
		false,
	)
	rc, err := bp.CallPostRestEndPoint(testServer.URL, "/some/path", ts, tsRecv)

	assert.NotEqual(t, tsRecv.Name, ts.Name)
	assert.NotNil(t, err)
	assert.Equal(t, http.StatusRequestTimeout, rc)
}

func TestBaseProcessor_GetAllObserversWithOkValuesShouldPass(t *testing.T) {
	t.Parallel()

	statusResponse := data.StatusResponse{
		Message: "",
		Error:   "",
		Running: true,
	}

	statusResponseBytes, err := json.Marshal(statusResponse)
	assert.Nil(t, err)

	server := createTestHttpServer("/node/status", statusResponseBytes)
	fmt.Printf("Server: %s\n", server.URL)
	defer server.Close()

	var observersList []*data.NodeData
	observersList = append(observersList, &data.NodeData{
		ShardId: 0,
		Address: server.URL,
	})

	bp, _ := process.NewBaseProcessor(
		5,
		&mock.ShardCoordinatorMock{},
		&mock.ObserversProviderStub{
			GetAllNodesCalled: func(_ data.ObserverDataAvailabilityType) ([]*data.NodeData, error) {
				return observersList, nil
			},
		},
		&mock.ObserversProviderStub{},
		&mock.PubKeyConverterMock{},
		false,
	)

	assert.Nil(t, err)

	observers, _ := bp.GetAllObservers(data.AvailabilityAll)
	assert.Nil(t, err)
	assert.Equal(t, server.URL, observers[0].Address)
}

func TestBaseProcessor_GetObserversOnePerShardShouldWork(t *testing.T) {
	t.Parallel()

	expectedResult := []string{
		"shard 0 - id 0",
		"shard 1 - id 0",
		"shard meta - id 0",
	}

	observersListShard0 := []*data.NodeData{
		{Address: "shard 0 - id 0"},
		{Address: "shard 0 - id 1"},
	}
	observersListShard1 := []*data.NodeData{
		{Address: "shard 1 - id 0"},
		{Address: "shard 1 - id 1"},
	}
	observersListShardMeta := []*data.NodeData{
		{Address: "shard meta - id 0"},
		{Address: "shard meta - id 1"},
	}

	bp, _ := process.NewBaseProcessor(
		5,
		&mock.ShardCoordinatorMock{NumShards: 2},
		&mock.ObserversProviderStub{
			GetNodesByShardIdCalled: func(shardId uint32, dataAvailability data.ObserverDataAvailabilityType) ([]*data.NodeData, error) {
				switch shardId {
				case 0:
					return observersListShard0, nil
				case 1:
					return observersListShard1, nil
				case core.MetachainShardId:
					return observersListShardMeta, nil
				}

				return nil, nil
			},
		},
		&mock.ObserversProviderStub{},
		&mock.PubKeyConverterMock{},
		false,
	)

	observers, err := bp.GetObserversOnePerShard(data.AvailabilityAll)
	assert.NoError(t, err)

	for i := 0; i < len(observers); i++ {
		assert.Equal(t, expectedResult[i], observers[i].Address)
	}
	assert.Equal(t, len(expectedResult), len(observers))
}

func TestBaseProcessor_GetObserversOnePerShardOneShardHasNoObserverShouldWork(t *testing.T) {
	t.Parallel()

	expectedResult := []string{
		"shard 0 - id 0",
		"shard meta - id 0",
	}

	observersListShard0 := []*data.NodeData{
		{Address: "shard 0 - id 0"},
		{Address: "shard 0 - id 1"},
	}
	var observersListShard1 []*data.NodeData
	observersListShardMeta := []*data.NodeData{
		{Address: "shard meta - id 0"},
		{Address: "shard meta - id 1"},
	}

	bp, _ := process.NewBaseProcessor(
		5,
		&mock.ShardCoordinatorMock{NumShards: 2},
		&mock.ObserversProviderStub{
			GetNodesByShardIdCalled: func(shardId uint32, dataAvailability data.ObserverDataAvailabilityType) ([]*data.NodeData, error) {
				switch shardId {
				case 0:
					return observersListShard0, nil
				case 1:
					return observersListShard1, nil
				case core.MetachainShardId:
					return observersListShardMeta, nil
				}

				return nil, nil
			},
		},
		&mock.ObserversProviderStub{},
		&mock.PubKeyConverterMock{},
		false,
	)

	observers, err := bp.GetObserversOnePerShard(data.AvailabilityAll)
	assert.NoError(t, err)

	for i := 0; i < len(observers); i++ {
		assert.Equal(t, expectedResult[i], observers[i].Address)
	}
	assert.Equal(t, len(expectedResult), len(observers))
}

func TestBaseProcessor_GetObserversOnePerShardMetachainHasNoObserverShouldWork(t *testing.T) {
	t.Parallel()

	expectedResult := []string{
		"shard 0 - id 0",
		"shard 1 - id 0",
	}

	observersListShard0 := []*data.NodeData{
		{Address: "shard 0 - id 0"},
		{Address: "shard 0 - id 1"},
	}
	observersListShard1 := []*data.NodeData{
		{Address: "shard 1 - id 0"},
		{Address: "shard 1 - id 0"},
	}
	var observersListShardMeta []*data.NodeData

	bp, _ := process.NewBaseProcessor(
		5,
		&mock.ShardCoordinatorMock{NumShards: 2},
		&mock.ObserversProviderStub{
			GetNodesByShardIdCalled: func(shardId uint32, dataAvailability data.ObserverDataAvailabilityType) ([]*data.NodeData, error) {
				switch shardId {
				case 0:
					return observersListShard0, nil
				case 1:
					return observersListShard1, nil
				case core.MetachainShardId:
					return observersListShardMeta, nil
				}

				return nil, nil
			},
		},
		&mock.ObserversProviderStub{},
		&mock.PubKeyConverterMock{},
		false,
	)

	observers, err := bp.GetObserversOnePerShard(data.AvailabilityAll)
	assert.NoError(t, err)

	for i := 0; i < len(observers); i++ {
		assert.Equal(t, expectedResult[i], observers[i].Address)
	}
	assert.Equal(t, len(expectedResult), len(observers))
}

func TestBaseProcessor_GetFullHistoryNodesOnePerShardShouldWork(t *testing.T) {
	t.Parallel()

	expectedResult := []string{
		"shard 0 - id 0",
		"shard 1 - id 0",
		"shard meta - id 0",
	}

	observersListShard0 := []*data.NodeData{
		{Address: "shard 0 - id 0"},
		{Address: "shard 0 - id 1"},
	}
	observersListShard1 := []*data.NodeData{
		{Address: "shard 1 - id 0"},
		{Address: "shard 1 - id 1"},
	}
	observersListShardMeta := []*data.NodeData{
		{Address: "shard meta - id 0"},
		{Address: "shard meta - id 1"},
	}

	bp, _ := process.NewBaseProcessor(
		5,
		&mock.ShardCoordinatorMock{NumShards: 2},
		&mock.ObserversProviderStub{},
		&mock.ObserversProviderStub{
			GetNodesByShardIdCalled: func(shardId uint32, dataAvailability data.ObserverDataAvailabilityType) ([]*data.NodeData, error) {
				switch shardId {
				case 0:
					return observersListShard0, nil
				case 1:
					return observersListShard1, nil
				case core.MetachainShardId:
					return observersListShardMeta, nil
				}

				return nil, nil
			},
		},
		&mock.PubKeyConverterMock{},
		false,
	)

	observers, err := bp.GetFullHistoryNodesOnePerShard(data.AvailabilityAll)
	assert.NoError(t, err)

	for i := 0; i < len(observers); i++ {
		assert.Equal(t, expectedResult[i], observers[i].Address)
	}
	assert.Equal(t, len(expectedResult), len(observers))
}

func TestBaseProcessor_GetShardIDs(t *testing.T) {
	t.Parallel()

	bp, _ := process.NewBaseProcessor(
		5,
		&mock.ShardCoordinatorMock{NumShards: 3},
		&mock.ObserversProviderStub{},
		&mock.ObserversProviderStub{},
		&mock.PubKeyConverterMock{},
		false,
	)

	expected := []uint32{0, 1, 2, core.MetachainShardId}
	require.Equal(t, expected, bp.GetShardIDs())
}

func TestBaseProcessor_HandleNodesSyncStateShouldSetNodeOutOfSyncIfVMQueriesNotReady(t *testing.T) {
	numTimesUpdateNodesWasCalled := uint32(0)

	bp, _ := process.NewBaseProcessor(
		5,
		&mock.ShardCoordinatorMock{},
		&mock.ObserversProviderStub{
			GetAllNodesWithSyncStateCalled: func() []*data.NodeData {
				return []*data.NodeData{
					{Address: "address0", ShardId: 0, IsSynced: true},
					{Address: "address1", ShardId: 0, IsSynced: true},
				}
			},
			UpdateNodesBasedOnSyncStateCalled: func(nodesWithSyncStatus []*data.NodeData) {
				require.Equal(t, &data.NodeData{Address: "address0", IsSynced: false, Nonce: 10}, nodesWithSyncStatus[0])
				require.Equal(t, &data.NodeData{Address: "address1", IsSynced: false, Nonce: 10}, nodesWithSyncStatus[1])
				atomic.AddUint32(&numTimesUpdateNodesWasCalled, 1)
			},
		},
		&mock.ObserversProviderStub{},
		&mock.PubKeyConverterMock{},
		false,
	)

	bp.SetNodeStatusFetcher(func(url string) (*data.NodeStatusAPIResponse, int, error) {
		if url == "address0" {
			return getResponseForNodeStatus(true, "false"), 200, nil
		}
		if url == "address1" {
			return getResponseForNodeStatus(false, ""), 200, nil
		}
		return nil, 400, nil
	})

	bp.SetDelayForCheckingNodesSyncState(5 * time.Millisecond)
	bp.StartNodesSyncStateChecks()

	time.Sleep(50 * time.Millisecond)

	require.GreaterOrEqual(t, atomic.LoadUint32(&numTimesUpdateNodesWasCalled), uint32(0))

	_ = bp.Close()
	time.Sleep(50 * time.Millisecond)
}

func TestBaseProcessor_HandleNodesSyncStateShouldTreatObserverThatWasDown(t *testing.T) {
	numTimesUpdateNodesWasCalled := uint32(0)
	numTimesGetStatusWasCalled := uint32(0)

	bp, _ := process.NewBaseProcessor(
		5,
		&mock.ShardCoordinatorMock{},
		&mock.ObserversProviderStub{
			GetAllNodesWithSyncStateCalled: func() []*data.NodeData {
				numTimesCalled := atomic.LoadUint32(&numTimesGetStatusWasCalled)
				isSynced := numTimesCalled%2 == 0

				return []*data.NodeData{
					{Address: "address0", ShardId: 0, IsSynced: isSynced},
				}
			},
			UpdateNodesBasedOnSyncStateCalled: func(nodesWithSyncStatus []*data.NodeData) {
				defer func() {
					atomic.AddUint32(&numTimesUpdateNodesWasCalled, 1)
				}()

				numTimesCalled := atomic.LoadUint32(&numTimesGetStatusWasCalled)
				if numTimesCalled <= 5 {
					require.True(t, nodesWithSyncStatus[0].IsSynced)
					return
				}

				if numTimesCalled <= 10 {
					require.False(t, nodesWithSyncStatus[0].IsSynced)
					return
				}

				require.True(t, nodesWithSyncStatus[0].IsSynced)
			},
		},
		&mock.ObserversProviderStub{},
		&mock.PubKeyConverterMock{},
		false,
	)

	bp.SetNodeStatusFetcher(func(url string) (*data.NodeStatusAPIResponse, int, error) {
		defer func() {
			atomic.AddUint32(&numTimesGetStatusWasCalled, 1)
		}()

		numTimesCalled := atomic.LoadUint32(&numTimesGetStatusWasCalled)
		/*
		   calls 0 -> 5 : online
		   calls 5 -> 10 : offline
		   calls 10+ : online
		*/

		if numTimesCalled < 5 {
			return getResponseForNodeStatus(true, "true"), 200, nil
		}

		if numTimesCalled < 10 {
			return getResponseForNodeStatus(false, ""), 200, nil
		}

		return getResponseForNodeStatus(true, "true"), 200, nil
	})

	bp.SetDelayForCheckingNodesSyncState(5 * time.Millisecond)
	bp.StartNodesSyncStateChecks()

	time.Sleep(200 * time.Millisecond)

	require.GreaterOrEqual(t, atomic.LoadUint32(&numTimesUpdateNodesWasCalled), uint32(0))

	_ = bp.Close()
	time.Sleep(50 * time.Millisecond)
}

func TestBaseProcessor_HandleNodesSyncStateShouldBeTriggeredEarlierIfANodeIsOffline(t *testing.T) {
	numTimesUpdateNodesWasCalled := uint32(0)
	numTimesGetStatusWasCalled := uint32(0)

	bp, _ := process.NewBaseProcessor(
		5,
		&mock.ShardCoordinatorMock{},
		&mock.ObserversProviderStub{
			GetAllNodesWithSyncStateCalled: func() []*data.NodeData {
				numTimesCalled := atomic.LoadUint32(&numTimesGetStatusWasCalled)
				isSynced := numTimesCalled%2 == 0

				return []*data.NodeData{
					{Address: "address0", ShardId: 0, IsSynced: isSynced},
				}
			},
			UpdateNodesBasedOnSyncStateCalled: func(nodesWithSyncStatus []*data.NodeData) {
				defer func() {
					atomic.AddUint32(&numTimesUpdateNodesWasCalled, 1)
				}()
				require.True(t, nodesWithSyncStatus[0].IsSynced)
			},
		},
		&mock.ObserversProviderStub{},
		&mock.PubKeyConverterMock{},
		false,
	)

	bp.SetNodeStatusFetcher(func(url string) (*data.NodeStatusAPIResponse, int, error) {
		defer func() {
			atomic.AddUint32(&numTimesGetStatusWasCalled, 1)
		}()

		return getResponseForNodeStatus(true, "true"), 200, nil
	})

	bp.SetDelayForCheckingNodesSyncState(200 * time.Millisecond)
	bp.StartNodesSyncStateChecks()

	go func() {
		// trigger a HTTP error that will trigger the nodes sync state checks
		time.Sleep(90 * time.Millisecond)
		_, _ = bp.CallGetRestEndPoint("address1", "/node/status", nil)
	}()

	time.Sleep(300 * time.Millisecond)

	// we should have 3 checks: one at the start, one trigger by the http error and one after the 200 ms sleep
	require.Equal(t, uint32(3), atomic.LoadUint32(&numTimesUpdateNodesWasCalled))

	_ = bp.Close()
	time.Sleep(50 * time.Millisecond)
}

func TestBaseProcessor_HandleNodesSyncStateShouldConsiderNodeAsOnlineIfProbableNonceIsLowerThanNonce(t *testing.T) {

	numTimesUpdateNodesWasCalled := uint32(0)

	bp, _ := process.NewBaseProcessor(
		5,
		&mock.ShardCoordinatorMock{},
		&mock.ObserversProviderStub{
			GetAllNodesWithSyncStateCalled: func() []*data.NodeData {
				return []*data.NodeData{
					{Address: "address0", ShardId: 0, IsSynced: true},
				}
			},
			UpdateNodesBasedOnSyncStateCalled: func(nodesWithSyncStatus []*data.NodeData) {
				require.Equal(t, &data.NodeData{Address: "address0", IsSynced: true, Nonce: 37}, nodesWithSyncStatus[0])
				atomic.AddUint32(&numTimesUpdateNodesWasCalled, 1)
			},
		},
		&mock.ObserversProviderStub{},
		&mock.PubKeyConverterMock{},
		false,
	)

	bp.SetNodeStatusFetcher(func(url string) (*data.NodeStatusAPIResponse, int, error) {
		return &data.NodeStatusAPIResponse{
			Data: data.NodeStatusAPIResponseData{
				Metrics: data.NodeStatusResponse{
					Nonce:                37,
					ProbableHighestNonce: 36,
					AreVmQueriesReady:    "true",
				},
			},
		}, 200, nil

	})
	bp.SetDelayForCheckingNodesSyncState(50 * time.Millisecond)
	bp.StartNodesSyncStateChecks()

	time.Sleep(50 * time.Millisecond)

	require.GreaterOrEqual(t, atomic.LoadUint32(&numTimesUpdateNodesWasCalled), uint32(1))

	_ = bp.Close()
	time.Sleep(50 * time.Millisecond)
}

func TestBaseProcessor_HandleNodesSyncState(t *testing.T) {

	numTimesUpdateNodesWasCalled := uint32(0)

	bp, _ := process.NewBaseProcessor(
		5,
		&mock.ShardCoordinatorMock{},
		&mock.ObserversProviderStub{
			GetAllNodesWithSyncStateCalled: func() []*data.NodeData {
				return []*data.NodeData{
					{Address: "address0", ShardId: 0, IsSynced: true},
					{Address: "address1", ShardId: 0, IsSynced: false},
				}
			},
			UpdateNodesBasedOnSyncStateCalled: func(nodesWithSyncStatus []*data.NodeData) {
				require.Equal(t, &data.NodeData{Address: "address0", IsSynced: true, Nonce: 10}, nodesWithSyncStatus[0])
				require.Equal(t, &data.NodeData{Address: "address1", IsSynced: false, Nonce: 10}, nodesWithSyncStatus[1])
				atomic.AddUint32(&numTimesUpdateNodesWasCalled, 1)
			},
		},
		&mock.ObserversProviderStub{
			GetAllNodesWithSyncStateCalled: func() []*data.NodeData {
				return []*data.NodeData{
					{Address: "fhaddress0", ShardId: 0, IsSynced: true},
					{Address: "fhaddress1", ShardId: 0, IsSynced: false},
				}
			},
			UpdateNodesBasedOnSyncStateCalled: func(nodesWithSyncStatus []*data.NodeData) {
				require.Equal(t, &data.NodeData{Address: "fhaddress0", IsSynced: true, Nonce: 10}, nodesWithSyncStatus[0])
				require.Equal(t, &data.NodeData{Address: "fhaddress1", IsSynced: false, Nonce: 10}, nodesWithSyncStatus[1])
				atomic.AddUint32(&numTimesUpdateNodesWasCalled, 1)
			},
		},
		&mock.PubKeyConverterMock{},
		false,
	)

	bp.SetNodeStatusFetcher(func(url string) (*data.NodeStatusAPIResponse, int, error) {
		if url == "address0" {
			return getResponseForNodeStatus(true, "true"), 200, nil
		}
		if url == "address1" {
			return getResponseForNodeStatus(false, "true"), 200, nil
		}
		if url == "fhaddress0" {
			return getResponseForNodeStatus(true, "true"), 200, nil
		}
		if url == "fhaddress1" {
			return getResponseForNodeStatus(false, "true"), 200, nil
		}

		return nil, 400, nil
	})
	bp.SetDelayForCheckingNodesSyncState(5 * time.Millisecond)
	bp.StartNodesSyncStateChecks()

	time.Sleep(50 * time.Millisecond)

	require.GreaterOrEqual(t, atomic.LoadUint32(&numTimesUpdateNodesWasCalled), uint32(2))

	_ = bp.Close()
	time.Sleep(50 * time.Millisecond)
}

func TestBaseProcessor_HandleNodesSyncStateCrossValidation(t *testing.T) {
	t.Parallel()

	numTimesUpdateNodesWasCalled := uint32(0)

	bp, _ := process.NewBaseProcessor(
		5,
		&mock.ShardCoordinatorMock{},
		&mock.ObserversProviderStub{
			GetAllNodesWithSyncStateCalled: func() []*data.NodeData {
				return []*data.NodeData{
					{Address: "address0", ShardId: 0, IsSynced: true},           // will have nonce 1000
					{Address: "address1", ShardId: 0, IsSynced: true},           // will have nonce 800 (200 behind, should be marked out-of-sync)
					{Address: "address2", ShardId: 0, IsSynced: true},           // will have nonce 950 (50 behind, should stay synced)
					{Address: "address_meta", ShardId: 4294967295, IsSynced: true}, // metachain, nonce 500
				}
			},
			UpdateNodesBasedOnSyncStateCalled: func(nodesWithSyncStatus []*data.NodeData) {
				// address0 has highest nonce, should be synced
				require.Equal(t, "address0", nodesWithSyncStatus[0].Address)
				require.True(t, nodesWithSyncStatus[0].IsSynced)
				require.Equal(t, uint64(1000), nodesWithSyncStatus[0].Nonce)

				// address1 is 200 blocks behind (> 100 threshold), should be out-of-sync
				require.Equal(t, "address1", nodesWithSyncStatus[1].Address)
				require.False(t, nodesWithSyncStatus[1].IsSynced)
				require.Equal(t, uint64(800), nodesWithSyncStatus[1].Nonce)

				// address2 is only 50 blocks behind (< 100 threshold), should stay synced
				require.Equal(t, "address2", nodesWithSyncStatus[2].Address)
				require.True(t, nodesWithSyncStatus[2].IsSynced)
				require.Equal(t, uint64(950), nodesWithSyncStatus[2].Nonce)

				// address_meta is in different shard, not affected by shard 0 nonces
				require.Equal(t, "address_meta", nodesWithSyncStatus[3].Address)
				require.True(t, nodesWithSyncStatus[3].IsSynced)
				require.Equal(t, uint64(500), nodesWithSyncStatus[3].Nonce)

				atomic.AddUint32(&numTimesUpdateNodesWasCalled, 1)
			},
		},
		&mock.ObserversProviderStub{},
		&mock.PubKeyConverterMock{},
		false,
	)

	bp.SetNodeStatusFetcher(func(url string) (*data.NodeStatusAPIResponse, int, error) {
		switch url {
		case "address0":
			return &data.NodeStatusAPIResponse{
				Data: data.NodeStatusAPIResponseData{
					Metrics: data.NodeStatusResponse{
						Nonce:                1000,
						ProbableHighestNonce: 1000,
						AreVmQueriesReady:    "true",
					},
				},
			}, 200, nil
		case "address1":
			return &data.NodeStatusAPIResponse{
				Data: data.NodeStatusAPIResponseData{
					Metrics: data.NodeStatusResponse{
						Nonce:                800, // 200 behind highest in shard
						ProbableHighestNonce: 800, // thinks it's synced
						AreVmQueriesReady:    "true",
					},
				},
			}, 200, nil
		case "address2":
			return &data.NodeStatusAPIResponse{
				Data: data.NodeStatusAPIResponseData{
					Metrics: data.NodeStatusResponse{
						Nonce:                950, // 50 behind highest in shard
						ProbableHighestNonce: 950,
						AreVmQueriesReady:    "true",
					},
				},
			}, 200, nil
		case "address_meta":
			return &data.NodeStatusAPIResponse{
				Data: data.NodeStatusAPIResponseData{
					Metrics: data.NodeStatusResponse{
						Nonce:                500,
						ProbableHighestNonce: 500,
						AreVmQueriesReady:    "true",
					},
				},
			}, 200, nil
		}
		return nil, 400, nil
	})

	bp.SetDelayForCheckingNodesSyncState(5 * time.Millisecond)
	bp.StartNodesSyncStateChecks()

	time.Sleep(50 * time.Millisecond)

	require.GreaterOrEqual(t, atomic.LoadUint32(&numTimesUpdateNodesWasCalled), uint32(1))

	_ = bp.Close()
	time.Sleep(50 * time.Millisecond)
}

func TestBaseProcessor_NoStatusCheck(t *testing.T) {

	numPrintNodesInShardsCalled := uint32(0)
	bp, _ := process.NewBaseProcessor(
		5,
		&mock.ShardCoordinatorMock{},
		&mock.ObserversProviderStub{
			GetAllNodesWithSyncStateCalled: func() []*data.NodeData {
				require.Fail(t, "should have not been called")
				return nil
			},
			UpdateNodesBasedOnSyncStateCalled: func(nodesWithSyncStatus []*data.NodeData) {
				require.Fail(t, "should have not been called")
			},
			PrintNodesInShardsCalled: func() {
				atomic.AddUint32(&numPrintNodesInShardsCalled, 1)
			},
		},
		&mock.ObserversProviderStub{},
		&mock.PubKeyConverterMock{},
		true,
	)

	bp.SetNodeStatusFetcher(func(url string) (*data.NodeStatusAPIResponse, int, error) {
		require.Fail(t, "should have not been called")

		return nil, 400, nil
	})
	bp.SetDelayForCheckingNodesSyncState(5 * time.Millisecond)
	bp.StartNodesSyncStateChecks()

	time.Sleep(50 * time.Millisecond)

	require.GreaterOrEqual(t, atomic.LoadUint32(&numPrintNodesInShardsCalled), uint32(1))

	_ = bp.Close()
	time.Sleep(50 * time.Millisecond)
}

func getResponseForNodeStatus(synced bool, vmQueriesReadyStr string) *data.NodeStatusAPIResponse {
	nonce, probableHighestNonce := uint64(10), uint64(11)
	if !synced {
		probableHighestNonce = 37
	}

	obj := data.NodeStatusAPIResponse{
		Data: data.NodeStatusAPIResponseData{
			Metrics: data.NodeStatusResponse{
				Nonce:                nonce,
				ProbableHighestNonce: probableHighestNonce,
				AreVmQueriesReady:    vmQueriesReadyStr,
			},
		},
	}

	return &obj
}

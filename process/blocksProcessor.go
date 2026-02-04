package process

import (
	"fmt"
	"sync"

	"github.com/multiversx/mx-chain-core-go/core/check"
	"github.com/multiversx/mx-chain-core-go/data/api"
	"github.com/multiversx/mx-chain-proxy-go/common"
	"github.com/multiversx/mx-chain-proxy-go/data"
)

const (
	blockByRoundPath = "/block/by-round"
)

// BlocksProcessor handles blocks retrieving from all shards
type BlocksProcessor struct {
	proc Processor
}

// NewBlocksProcessor creates a new block processor
func NewBlocksProcessor(proc Processor) (*BlocksProcessor, error) {
	if check.IfNil(proc) {
		return nil, ErrNilCoreProcessor
	}

	return &BlocksProcessor{
		proc: proc,
	}, nil
}

// GetBlocksByRound return all blocks(from all shards) by a specific round. For each shard, a block is requested
// (from only one observer) and added in a slice of blocks => should have max blocks = no of shards.
// If there are more observers in a shard which can be queried for a block by round, we get the block from
// the first one which responds (no sanity checks are performed)
func (bp *BlocksProcessor) GetBlocksByRound(round uint64, options common.BlockQueryOptions) (*data.BlocksApiResponse, error) {
	shardIDs := bp.proc.GetShardIDs()
	ret := &data.BlocksApiResponse{
		Data: data.BlocksApiResponsePayload{
			Blocks: make([]*api.Block, 0, len(shardIDs)),
		},
	}

	path := common.BuildUrlWithBlockQueryOptions(fmt.Sprintf("%s/%d", blockByRoundPath, round), options)

	type shardResult struct {
		block    *api.Block
		observer *data.NodeData
		err      error
	}

	results := make(chan shardResult, len(shardIDs))
	var wg sync.WaitGroup

	for _, shardID := range shardIDs {
		shardID := shardID
		wg.Add(1)
		go func() {
			defer wg.Done()
			observers, err := bp.proc.GetObservers(shardID, data.AvailabilityAll)
			if err != nil {
				results <- shardResult{err: err}
				return
			}

			for _, observer := range observers {
				block, err := bp.getBlockFromObserver(observer, path)
				if err != nil {
					log.Error("block request failed", "shard id", observer.ShardId, "observer", observer.Address, "error", err.Error())
					continue
				}

				results <- shardResult{block: block, observer: observer}
				return
			}

			results <- shardResult{}
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var firstErr error
	for res := range results {
		if res.err != nil && firstErr == nil {
			firstErr = res.err
			continue
		}
		if res.block == nil || res.observer == nil {
			continue
		}

		log.Info("block requested successfully", "shard id", res.observer.ShardId, "observer", res.observer.Address, "round", round)
		ret.Data.Blocks = append(ret.Data.Blocks, res.block)
	}

	if firstErr != nil {
		return nil, firstErr
	}

	return ret, nil
}

func (bp *BlocksProcessor) getBlockFromObserver(observer *data.NodeData, path string) (*api.Block, error) {
	var response data.BlockApiResponse

	_, err := bp.proc.CallGetRestEndPoint(observer.Address, path, &response)
	if err != nil {
		return nil, err
	}

	return &response.Data.Block, nil
}

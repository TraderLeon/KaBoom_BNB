package eventsync

import (
	"context"
	"github.com/cross-space-official/common/logger"
	"github.com/cross-space-official/common/utils"
	"github.com/cross-space-official/kaboom-service/configs"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

type SyncClient interface {
	GetEthClient() *ethclient.Client
	TryFetchLogs(ctx context.Context, addresses []string, topics []string, startingBlockHeight uint64, endingBlockHeight *uint64, retryCount int) []types.Log
}

type evmEventSyncClient struct {
	chainID string
	client  *ethclient.Client
	config  configs.OnchainClientConfig
}

func (c *evmEventSyncClient) GetEthClient() *ethclient.Client {
	return c.client
}

func (c *evmEventSyncClient) TryFetchLogs(ctx context.Context, addresses []string, topics []string, startingBlockHeight uint64, endingBlockHeight *uint64, retryCount int) []types.Log {
	addresses = utils.Filter(addresses, func(address string) bool {
		return len(strings.TrimSpace(address)) > 0
	})

	query := ethereum.FilterQuery{
		FromBlock: new(big.Int).SetUint64(startingBlockHeight),
		Topics:    [][]common.Hash{utils.Map(topics, common.HexToHash)},
		Addresses: utils.Map(addresses, common.HexToAddress),
	}

	if endingBlockHeight != nil {
		query.ToBlock = new(big.Int).SetUint64(*endingBlockHeight)
	}

	historyLogs, err := c.client.FilterLogs(ctx, query)
	if err != nil {
		logger.GetLoggerEntry(ctx).Errorf("chain %s error getting history log, %v, from %v", c.chainID, err, startingBlockHeight)
		if strings.Contains(err.Error(), "Log response size exceeded.") {
			pattern := regexp.MustCompile(`\[0x([0-9a-fA-F]+), 0x([0-9a-fA-F]+)\]`)
			matches := pattern.FindStringSubmatch(err.Error())

			// if api response without suggested block height
			if len(matches) != 3 {
				suggestedEndingBlockHeight := startingBlockHeight
				return c.TryFetchLogs(ctx, addresses, topics, startingBlockHeight, &suggestedEndingBlockHeight, retryCount+1)
			}

			// iterate over blocks
			suggestedStartingBlockHeight, _ := strconv.ParseUint(matches[1], 16, 64)
			suggestedEndingBlockHeight, err := strconv.ParseUint(matches[2], 16, 64)

			// Handle bug case from api response
			if suggestedStartingBlockHeight > suggestedEndingBlockHeight {
				suggestedEndingBlockHeight = suggestedStartingBlockHeight
			}
			if err != nil || retryCount >= 3 {
				suggestedEndingBlockHeight = startingBlockHeight + (suggestedEndingBlockHeight-startingBlockHeight)/2
			}
			logger.GetLoggerEntry(ctx).Infof("chain %s, try get until block height: %d", c.chainID, suggestedEndingBlockHeight)
			firstHistoryLogs := c.TryFetchLogs(ctx, addresses, topics, startingBlockHeight, &suggestedEndingBlockHeight, retryCount+1)
			secondHistoryLogs := c.TryFetchLogs(ctx, addresses, topics, suggestedEndingBlockHeight+1, endingBlockHeight, retryCount+1)
			return append(firstHistoryLogs, secondHistoryLogs...)
		} else {
			return nil
		}
	}

	return historyLogs
}

func NewEventSyncClient(
	config configs.OnchainClientConfig,
) SyncClient {
	client, err := NewEthClient(config)
	if err != nil {
		panic(err)
	}

	if client == nil {
		return nil
	}

	return &evmEventSyncClient{
		chainID: config.ChainID,
		client:  client,
		config:  config,
	}
}

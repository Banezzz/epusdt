package task

import (
	"context"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/Banezzz/ezpay/config"
	"github.com/Banezzz/ezpay/model/mdb"
	"github.com/Banezzz/ezpay/util/log"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

type evmLogHandler func(*ethclient.Client, types.Log, int64)
type evmNodeResolver func() (*mdb.RpcNode, bool)

const (
	evmBackfillBlockWindow uint64 = 1200
	evmBackfillChunkSize   uint64 = 200
)

// runEvmWsLogListener connects to wsURL, subscribes to Transfer logs,
// and dispatches each log to handleLog. It retries on transient errors
// with exponential backoff. The ctx lets the caller trigger a clean
// exit — e.g. when admin disables the chain, the caller cancels the
// context and the function returns instead of reconnecting forever.
func runEvmWsLogListener(ctx context.Context, logPrefix string, resolveNode evmNodeResolver, query ethereum.FilterQuery, handleLog evmLogHandler) {
	const (
		minBackoff = 2 * time.Second
		maxBackoff = 60 * time.Second
		rejoinWait = 3 * time.Second
	)
	failWait := minBackoff

	for {
		if ctx.Err() != nil {
			return
		}

		node, ok := resolveNode()
		if !ok || node == nil {
			if !sleepOrDone(ctx, failWait) {
				return
			}
			failWait = nextBackoff(failWait, maxBackoff)
			continue
		}

		wsURL := strings.TrimSpace(node.Url)
		log.Sugar.Infof("%s connecting to %s (rpc_node=%d)", logPrefix, wsURL, node.ID)
		client, err := ethclient.Dial(wsURL)
		if err != nil {
			markRpcNodeDown(node, logPrefix, err)
			log.Sugar.Warnf("%s dial: %v, retry in %s", logPrefix, err, failWait)
			if !sleepOrDone(ctx, failWait) {
				return
			}
			failWait = nextBackoff(failWait, maxBackoff)
			continue
		}

		backfillRecentEVMLogs(ctx, client, logPrefix, query, handleLog)

		logsCh := make(chan types.Log, 256)
		sub, err := client.SubscribeFilterLogs(ctx, query, logsCh)
		if err != nil {
			client.Close()
			markRpcNodeDown(node, logPrefix, err)
			log.Sugar.Warnf("%s subscribe: %v, retry in %s", logPrefix, err, failWait)
			if !sleepOrDone(ctx, failWait) {
				return
			}
			failWait = nextBackoff(failWait, maxBackoff)
			continue
		}
		failWait = minBackoff

		log.Sugar.Infof("%s connected, subscribed to Transfer logs", logPrefix)

		if recvLoop(ctx, client, sub, logsCh, logPrefix, handleLog) {
			markRpcNodeDown(node, logPrefix, nil)
		}

		if ctx.Err() != nil {
			return
		}
		if !sleepOrDone(ctx, rejoinWait) {
			return
		}
	}
}

func evmRecipientTopicHashes(wallets []mdb.WalletAddress) []common.Hash {
	seen := make(map[string]common.Hash)
	for _, w := range wallets {
		address := strings.TrimSpace(w.Address)
		if !common.IsHexAddress(address) {
			continue
		}
		addr := common.HexToAddress(address)
		seen[strings.ToLower(addr.Hex())] = common.BytesToHash(addr.Bytes())
	}
	if len(seen) == 0 {
		return nil
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	topics := make([]common.Hash, 0, len(keys))
	for _, key := range keys {
		topics = append(topics, seen[key])
	}
	return topics
}

func evmRecipientFingerprint(wallets []mdb.WalletAddress) string {
	topics := evmRecipientTopicHashes(wallets)
	if len(topics) == 0 {
		return ""
	}
	parts := make([]string, 0, len(topics))
	for _, topic := range topics {
		parts = append(parts, topic.Hex())
	}
	return strings.Join(parts, ",")
}

func evmTransferQuery(contracts []common.Address, recipientTopics []common.Hash) ethereum.FilterQuery {
	return ethereum.FilterQuery{
		Addresses: contracts,
		Topics: [][]common.Hash{
			{transferEventHash},
			nil,
			recipientTopics,
		},
	}
}

func backfillRecentEVMLogs(ctx context.Context, client *ethclient.Client, logPrefix string, query ethereum.FilterQuery, handleLog evmLogHandler) {
	latest, err := client.HeaderByNumber(ctx, nil)
	if err != nil {
		log.Sugar.Warnf("%s backfill latest block: %v", logPrefix, err)
		return
	}
	if latest == nil || latest.Number == nil {
		log.Sugar.Warnf("%s backfill latest block missing number", logPrefix)
		return
	}

	latestNumber := latest.Number.Uint64()
	confirmations := config.GetEVMConfirmations()
	if latestNumber+1 <= confirmations {
		return
	}

	toBlock := latestNumber + 1 - confirmations
	fromBlock := uint64(0)
	if toBlock > evmBackfillBlockWindow {
		fromBlock = toBlock - evmBackfillBlockWindow
	}

	processed := 0
	for start := fromBlock; start <= toBlock; {
		if ctx.Err() != nil {
			return
		}

		end := start + evmBackfillChunkSize - 1
		if end > toBlock {
			end = toBlock
		}

		chunkQuery := query
		chunkQuery.FromBlock = new(big.Int).SetUint64(start)
		chunkQuery.ToBlock = new(big.Int).SetUint64(end)
		logs, err := client.FilterLogs(ctx, chunkQuery)
		if err != nil {
			log.Sugar.Warnf("%s backfill logs blocks=%d-%d: %v", logPrefix, start, end, err)
			return
		}

		for _, vLog := range logs {
			if ctx.Err() != nil {
				return
			}
			blockTsMs, ok := waitForConfirmedEVMLog(ctx, client, vLog, logPrefix)
			if !ok {
				continue
			}
			handleLog(client, vLog, blockTsMs)
			processed++
		}

		if end == toBlock {
			break
		}
		start = end + 1
	}

	if processed > 0 {
		log.Sugar.Infof("%s backfilled %d log(s) from blocks %d-%d", logPrefix, processed, fromBlock, toBlock)
	}
}

func recvLoop(ctx context.Context, client *ethclient.Client, sub ethereum.Subscription, logsCh <-chan types.Log, logPrefix string, handleLog evmLogHandler) bool {
	defer func() {
		sub.Unsubscribe()
		client.Close()
	}()

	for {
		select {
		case <-ctx.Done():
			log.Sugar.Infof("%s context cancelled, stopping", logPrefix)
			return false
		case err := <-sub.Err():
			if err != nil {
				log.Sugar.Warnf("%s subscription error: %v, reconnecting", logPrefix, err)
			} else {
				log.Sugar.Warnf("%s subscription closed, reconnecting", logPrefix)
			}
			return true
		case vLog, ok := <-logsCh:
			if !ok {
				log.Sugar.Warnf("%s log channel closed, reconnecting", logPrefix)
				return true
			}
			blockTsMs, ok := waitForConfirmedEVMLog(ctx, client, vLog, logPrefix)
			if !ok {
				continue
			}
			handleLog(client, vLog, blockTsMs)
		}
	}
}

func waitForConfirmedEVMLog(ctx context.Context, client *ethclient.Client, vLog types.Log, logPrefix string) (int64, bool) {
	if vLog.Removed {
		log.Sugar.Warnf("%s dropped removed log tx=%s block=%d", logPrefix, vLog.TxHash.Hex(), vLog.BlockNumber)
		return 0, false
	}

	blockHeader, err := client.HeaderByHash(ctx, vLog.BlockHash)
	if err != nil {
		log.Sugar.Warnf("%s HeaderByHash block=%s: %v", logPrefix, vLog.BlockHash.Hex(), err)
		return 0, false
	}
	if blockHeader == nil || blockHeader.Number == nil {
		log.Sugar.Warnf("%s missing block header for tx=%s block=%d", logPrefix, vLog.TxHash.Hex(), vLog.BlockNumber)
		return 0, false
	}

	confirmations := config.GetEVMConfirmations()
	blockNumber := blockHeader.Number.Uint64()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		latest, err := client.HeaderByNumber(ctx, nil)
		if err != nil {
			log.Sugar.Warnf("%s HeaderByNumber latest: %v", logPrefix, err)
		} else if latest != nil && latest.Number != nil && latest.Number.Uint64() >= blockNumber {
			if latest.Number.Uint64()-blockNumber+1 >= confirmations {
				if !isCanonicalEVMBlock(ctx, client, vLog, blockHeader.Number, logPrefix) {
					return 0, false
				}
				return int64(blockHeader.Time) * 1000, true
			}
		}

		select {
		case <-ctx.Done():
			return 0, false
		case <-ticker.C:
		}
	}
}

func isCanonicalEVMBlock(ctx context.Context, client *ethclient.Client, vLog types.Log, blockNumber *big.Int, logPrefix string) bool {
	canonical, err := client.HeaderByNumber(ctx, new(big.Int).Set(blockNumber))
	if err != nil {
		log.Sugar.Warnf("%s canonical block check failed block=%d: %v", logPrefix, vLog.BlockNumber, err)
		return false
	}
	if canonical == nil || canonical.Hash() != vLog.BlockHash {
		log.Sugar.Warnf("%s dropped non-canonical log tx=%s block=%d", logPrefix, vLog.TxHash.Hex(), vLog.BlockNumber)
		return false
	}
	return true
}

// sleepOrDone waits for d or for ctx cancellation, whichever comes
// first. Returns true if the sleep completed normally, false if ctx
// was cancelled (caller should exit).
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func nextBackoff(cur, max time.Duration) time.Duration {
	n := cur * 2
	if n > max {
		return max
	}
	return n
}

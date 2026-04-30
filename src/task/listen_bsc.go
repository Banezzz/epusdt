package task

import (
	"math/big"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Banezzz/ezpay/model/data"
	"github.com/Banezzz/ezpay/model/mdb"
	"github.com/Banezzz/ezpay/model/service"
	"github.com/Banezzz/ezpay/util/log"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

type bscRecipientSnapshot struct {
	addrs map[string]struct{}
}

var bscWatchedRecipients atomic.Pointer[bscRecipientSnapshot]

// StartBscWebSocketListener drives the BSC listener. Checks chain
// enable status and reloads contract addresses from chain_tokens every
// 10s so admin-side toggles take effect without a restart.
func StartBscWebSocketListener() {
	for {
		if data.IsChainEnabled(mdb.NetworkBsc) {
			if contracts := loadChainTokenContracts(mdb.NetworkBsc, "[BSC-WS]"); len(contracts) > 0 {
				runBscListener(contracts)
			}
		}
		time.Sleep(10 * time.Second)
	}
}

func runBscListener(contracts []common.Address) {
	ctx, cancel := chainEnabledWatchdog(mdb.NetworkBsc, "[BSC-WS]", chainTokenFingerprint(mdb.NetworkBsc))
	defer cancel()

	wallets, err := data.GetAvailableWalletAddressByNetwork(mdb.NetworkBsc)
	if err != nil {
		log.Sugar.Errorf("[BSC-WS] Failed to get wallet addresses: %v", err)
		return
	}
	recipientTopics := evmRecipientTopicHashes(wallets)
	if len(recipientTopics) == 0 {
		log.Sugar.Debug("[BSC-WS] no valid wallet addresses to watch")
		return
	}
	recipientFingerprint := evmRecipientFingerprint(wallets)
	storeBscRecipientsFromWallets(wallets)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w, err := data.GetAvailableWalletAddressByNetwork(mdb.NetworkBsc)
				if err != nil {
					log.Sugar.Warnf("[BSC-WS] refresh wallet addresses: %v", err)
					continue
				}
				if fp := evmRecipientFingerprint(w); fp != recipientFingerprint {
					log.Sugar.Info("[BSC-WS] wallet addresses changed, reconnecting")
					cancel()
					return
				}
				storeBscRecipientsFromWallets(w)
			}
		}
	}()

	log.Sugar.Infof("[BSC-WS] watching %d contract(s), %d recipient(s)", len(contracts), len(recipientTopics))

	query := evmTransferQuery(contracts, recipientTopics)

	runEvmWsLogListener(ctx, "[BSC-WS]", func() (*mdb.RpcNode, bool) {
		return resolveChainWsNode(mdb.NetworkBsc, "[BSC-WS]")
	}, query, func(_ *ethclient.Client, vLog types.Log, blockTsMs int64) {
		if len(vLog.Topics) < 3 {
			return
		}

		event := vLog.Topics[0].String()
		if event != transferEventHash.String() {
			return
		}

		amount := new(big.Int).SetBytes(vLog.Data)

		toAddr := common.HexToAddress(vLog.Topics[2].Hex())

		if !isWatchedBscRecipient(toAddr) {
			return
		}

		service.TryProcessEvmERC20Transfer(mdb.NetworkBsc, vLog.Address, toAddr, amount, vLog.TxHash.Hex(), blockTsMs)
	})
}

func storeBscRecipientsFromWallets(wallets []mdb.WalletAddress) int {
	m := make(map[string]struct{})
	for _, w := range wallets {
		a := strings.TrimSpace(w.Address)
		if !common.IsHexAddress(a) {
			continue
		}
		m[strings.ToLower(common.HexToAddress(a).Hex())] = struct{}{}
	}
	bscWatchedRecipients.Store(&bscRecipientSnapshot{addrs: m})
	return len(m)
}

func isWatchedBscRecipient(to common.Address) bool {
	snap := bscWatchedRecipients.Load()
	if snap == nil || len(snap.addrs) == 0 {
		return false
	}
	_, ok := snap.addrs[strings.ToLower(to.Hex())]
	return ok
}

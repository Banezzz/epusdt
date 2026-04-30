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

type polygonRecipientSnapshot struct {
	addrs map[string]struct{}
}

var polygonWatchedRecipients atomic.Pointer[polygonRecipientSnapshot]

// StartPolygonWebSocketListener drives the Polygon listener with
// dynamic chain/token config reload every 10s.
func StartPolygonWebSocketListener() {
	for {
		if data.IsChainEnabled(mdb.NetworkPolygon) {
			if contracts := loadChainTokenContracts(mdb.NetworkPolygon, "[POLYGON-WS]"); len(contracts) > 0 {
				runPolygonListener(contracts)
			}
		}
		time.Sleep(10 * time.Second)
	}
}

func runPolygonListener(contracts []common.Address) {
	ctx, cancel := chainEnabledWatchdog(mdb.NetworkPolygon, "[POLYGON-WS]", chainTokenFingerprint(mdb.NetworkPolygon))
	defer cancel()

	wallets, err := data.GetAvailableWalletAddressByNetwork(mdb.NetworkPolygon)
	if err != nil {
		log.Sugar.Errorf("[POLYGON-WS] Failed to get wallet addresses: %v", err)
		return
	}
	recipientTopics := evmRecipientTopicHashes(wallets)
	if len(recipientTopics) == 0 {
		log.Sugar.Debug("[POLYGON-WS] no valid wallet addresses to watch")
		return
	}
	recipientFingerprint := evmRecipientFingerprint(wallets)
	storePolygonRecipientsFromWallets(wallets)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w, err := data.GetAvailableWalletAddressByNetwork(mdb.NetworkPolygon)
				if err != nil {
					log.Sugar.Warnf("[POLYGON-WS] refresh wallet addresses: %v", err)
					continue
				}
				if fp := evmRecipientFingerprint(w); fp != recipientFingerprint {
					log.Sugar.Info("[POLYGON-WS] wallet addresses changed, reconnecting")
					cancel()
					return
				}
				storePolygonRecipientsFromWallets(w)
			}
		}
	}()

	log.Sugar.Infof("[POLYGON-WS] watching %d contract(s), %d recipient(s)", len(contracts), len(recipientTopics))

	query := evmTransferQuery(contracts, recipientTopics)

	runEvmWsLogListener(ctx, "[POLYGON-WS]", func() (*mdb.RpcNode, bool) {
		return resolveChainWsNode(mdb.NetworkPolygon, "[POLYGON-WS]")
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

		if !isWatchedPolygonRecipient(toAddr) {
			return
		}

		service.TryProcessEvmERC20Transfer(mdb.NetworkPolygon, vLog.Address, toAddr, amount, vLog.TxHash.Hex(), blockTsMs)
	})
}

func storePolygonRecipientsFromWallets(wallets []mdb.WalletAddress) int {
	m := make(map[string]struct{})
	for _, w := range wallets {
		a := strings.TrimSpace(w.Address)
		if !common.IsHexAddress(a) {
			continue
		}
		m[strings.ToLower(common.HexToAddress(a).Hex())] = struct{}{}
	}
	polygonWatchedRecipients.Store(&polygonRecipientSnapshot{addrs: m})
	return len(m)
}

func isWatchedPolygonRecipient(to common.Address) bool {
	snap := polygonWatchedRecipients.Load()
	if snap == nil || len(snap.addrs) == 0 {
		return false
	}
	_, ok := snap.addrs[strings.ToLower(to.Hex())]
	return ok
}

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

type plasmaRecipientSnapshot struct {
	addrs map[string]struct{}
}

var plasmaWatchedRecipients atomic.Pointer[plasmaRecipientSnapshot]

// StartPlasmaWebSocketListener drives the Plasma listener with dynamic
// chain/token config reload every 10s.
func StartPlasmaWebSocketListener() {
	for {
		if data.IsChainEnabled(mdb.NetworkPlasma) {
			if contracts := loadChainTokenContracts(mdb.NetworkPlasma, "[PLASMA-WS]"); len(contracts) > 0 {
				runPlasmaListener(contracts)
			}
		}
		time.Sleep(10 * time.Second)
	}
}

func runPlasmaListener(contracts []common.Address) {
	ctx, cancel := chainEnabledWatchdog(mdb.NetworkPlasma, "[PLASMA-WS]", chainTokenFingerprint(mdb.NetworkPlasma))
	defer cancel()

	wallets, err := data.GetAvailableWalletAddressByNetwork(mdb.NetworkPlasma)
	if err != nil {
		log.Sugar.Errorf("[PLASMA-WS] Failed to get wallet addresses: %v", err)
		return
	}
	recipientTopics := evmRecipientTopicHashes(wallets)
	if len(recipientTopics) == 0 {
		log.Sugar.Debug("[PLASMA-WS] no valid wallet addresses to watch")
		return
	}
	recipientFingerprint := evmRecipientFingerprint(wallets)
	storePlasmaRecipientsFromWallets(wallets)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w, err := data.GetAvailableWalletAddressByNetwork(mdb.NetworkPlasma)
				if err != nil {
					log.Sugar.Warnf("[PLASMA-WS] refresh wallet addresses: %v", err)
					continue
				}
				if fp := evmRecipientFingerprint(w); fp != recipientFingerprint {
					log.Sugar.Info("[PLASMA-WS] wallet addresses changed, reconnecting")
					cancel()
					return
				}
				storePlasmaRecipientsFromWallets(w)
			}
		}
	}()

	log.Sugar.Infof("[PLASMA-WS] watching %d contract(s), %d recipient(s)", len(contracts), len(recipientTopics))

	query := evmTransferQuery(contracts, recipientTopics)

	runEvmWsLogListener(ctx, "[PLASMA-WS]", func() (*mdb.RpcNode, bool) {
		return resolveChainWsNode(mdb.NetworkPlasma, "[PLASMA-WS]")
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

		if !isWatchedPlasmaRecipient(toAddr) {
			return
		}

		service.TryProcessEvmERC20Transfer(mdb.NetworkPlasma, vLog.Address, toAddr, amount, vLog.TxHash.Hex(), blockTsMs)
	})
}

func storePlasmaRecipientsFromWallets(wallets []mdb.WalletAddress) int {
	m := make(map[string]struct{})
	for _, w := range wallets {
		a := strings.TrimSpace(w.Address)
		if !common.IsHexAddress(a) {
			continue
		}
		m[strings.ToLower(common.HexToAddress(a).Hex())] = struct{}{}
	}
	plasmaWatchedRecipients.Store(&plasmaRecipientSnapshot{addrs: m})
	return len(m)
}

func isWatchedPlasmaRecipient(to common.Address) bool {
	snap := plasmaWatchedRecipients.Load()
	if snap == nil || len(snap.addrs) == 0 {
		return false
	}
	_, ok := snap.addrs[strings.ToLower(to.Hex())]
	return ok
}

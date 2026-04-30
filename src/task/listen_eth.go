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

// Transfer 事件签名 — ERC-20 signature, same on every EVM chain.
var transferEventHash = common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")

type ethRecipientSnapshot struct {
	addrs map[string]struct{}
}

var ethWatchedRecipients atomic.Pointer[ethRecipientSnapshot]

func StartEthereumWebSocketListener() {
	// Wait until the chain is enabled AND at least one token contract
	// is configured. Polls every 10s so admin-side toggles kick in
	// without a restart. Once conditions are met we proceed to connect;
	// if the websocket later drops we exit the loop and rely on the
	// process-level restart to reconnect (same as before this refactor).
	for {
		if data.IsChainEnabled(mdb.NetworkEthereum) {
			if contracts := loadChainTokenContracts(mdb.NetworkEthereum, "[ETH-WS]"); len(contracts) > 0 {
				runEthereumListener(contracts)
			}
		}
		time.Sleep(10 * time.Second)
	}
}

func runEthereumListener(contracts []common.Address) {
	ctx, cancel := chainEnabledWatchdog(mdb.NetworkEthereum, "[ETH-WS]", chainTokenFingerprint(mdb.NetworkEthereum))
	defer cancel()

	wallets, err := data.GetAvailableWalletAddressByNetwork(mdb.NetworkEthereum)
	if err != nil {
		log.Sugar.Errorf("[ETH-WS] Failed to get wallet addresses: %v", err)
		return
	}
	recipientTopics := evmRecipientTopicHashes(wallets)
	if len(recipientTopics) == 0 {
		log.Sugar.Debug("[ETH-WS] no valid wallet addresses to watch")
		return
	}
	recipientFingerprint := evmRecipientFingerprint(wallets)
	StoreEthRecipientsFromWallets(wallets)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w, err := data.GetAvailableWalletAddressByNetwork(mdb.NetworkEthereum)
				if err != nil {
					log.Sugar.Warnf("[ETH-WS] refresh wallet addresses: %v", err)
					continue
				}
				if fp := evmRecipientFingerprint(w); fp != recipientFingerprint {
					log.Sugar.Info("[ETH-WS] wallet addresses changed, reconnecting")
					cancel()
					return
				}
				StoreEthRecipientsFromWallets(w)
			}
		}
	}()

	log.Sugar.Infof("[ETH-WS] watching %d contract(s), %d recipient(s)", len(contracts), len(recipientTopics))

	query := evmTransferQuery(contracts, recipientTopics)

	runEvmWsLogListener(ctx, "[ETH-WS]", func() (*mdb.RpcNode, bool) {
		return resolveChainWsNode(mdb.NetworkEthereum, "[ETH-WS]")
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
		if !isWatchedEthRecipient(toAddr) {
			return
		}

		service.TryProcessEvmERC20Transfer(mdb.NetworkEthereum, vLog.Address, toAddr, amount, vLog.TxHash.Hex(), blockTsMs)
	})
}

func StoreEthRecipientsFromWallets(wallets []mdb.WalletAddress) int {
	m := make(map[string]struct{})
	for _, w := range wallets {
		a := strings.TrimSpace(w.Address)
		if !common.IsHexAddress(a) {
			continue
		}
		m[strings.ToLower(common.HexToAddress(a).Hex())] = struct{}{}
	}
	ethWatchedRecipients.Store(&ethRecipientSnapshot{addrs: m})
	return len(m)
}

func isWatchedEthRecipient(to common.Address) bool {
	snap := ethWatchedRecipients.Load()
	if snap == nil || len(snap.addrs) == 0 {
		return false
	}
	_, ok := snap.addrs[strings.ToLower(to.Hex())]
	return ok
}

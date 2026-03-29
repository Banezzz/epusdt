package service

import (
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/assimon/luuu/model/data"
	"github.com/assimon/luuu/model/request"
	"github.com/assimon/luuu/telegram"
	"github.com/assimon/luuu/util/constant"
	"github.com/assimon/luuu/util/http_client"
	"github.com/assimon/luuu/util/json"
	"github.com/assimon/luuu/util/log"
	"github.com/dromara/carbon/v2"
	"github.com/gookit/goutil/stdutil"
	"github.com/shopspring/decimal"
)

const UsdtTrc20ApiUri = "https://apilist.tronscanapi.com/api/transfer/trc20"

type UsdtTrc20Resp struct {
	PageSize int    `json:"page_size"`
	Code     int    `json:"code"`
	Data     []Data `json:"data"`
}

type TokenInfo struct {
	TokenID      string `json:"tokenId"`
	TokenAbbr    string `json:"tokenAbbr"`
	TokenName    string `json:"tokenName"`
	TokenDecimal int    `json:"tokenDecimal"`
	TokenCanShow int    `json:"tokenCanShow"`
	TokenType    string `json:"tokenType"`
	TokenLogo    string `json:"tokenLogo"`
	TokenLevel   string `json:"tokenLevel"`
	IssuerAddr   string `json:"issuerAddr"`
	Vip          bool   `json:"vip"`
}

type Data struct {
	Amount         string `json:"amount"`
	ApprovalAmount string `json:"approval_amount"`
	BlockTimestamp int64  `json:"block_timestamp"`
	Block          int    `json:"block"`
	From           string `json:"from"`
	To             string `json:"to"`
	Hash           string `json:"hash"`
	Confirmed      int    `json:"confirmed"`
	ContractType   string `json:"contract_type"`
	ContracTType   int    `json:"contractType"`
	Revert         int    `json:"revert"`
	ContractRet    string `json:"contract_ret"`
	EventType      string `json:"event_type"`
	IssueAddress   string `json:"issue_address"`
	Decimals       int    `json:"decimals"`
	TokenName      string `json:"token_name"`
	ID             string `json:"id"`
	Direction      int    `json:"direction"`
}

// Trc20CallBack polls transfers for one wallet and matches them to active orders.
func Trc20CallBack(token string, wg *sync.WaitGroup) {
	defer wg.Done()
	defer func() {
		if err := recover(); err != nil {
			log.Sugar.Error(err)
		}
	}()

	client := http_client.GetHttpClient()
	startTime := carbon.Now().AddHours(-24).TimestampMilli()
	endTime := carbon.Now().TimestampMilli()
	resp, err := client.R().SetQueryParams(map[string]string{
		"sort":            "-timestamp",
		"limit":           "50",
		"start":           "0",
		"direction":       "2",
		"db_version":      "1",
		"trc20Id":         "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
		"address":         token,
		"start_timestamp": stdutil.ToString(startTime),
		"end_timestamp":   stdutil.ToString(endTime),
	}).Get(UsdtTrc20ApiUri)
	if err != nil {
		panic(err)
	}
	if resp.StatusCode() != http.StatusOK {
		panic(err)
	}

	var trc20Resp UsdtTrc20Resp
	if err = json.Cjson.Unmarshal(resp.Body(), &trc20Resp); err != nil {
		panic(err)
	}
	if trc20Resp.PageSize <= 0 {
		return
	}

	for _, transfer := range trc20Resp.Data {
		if transfer.To != token || transfer.ContractRet != "SUCCESS" {
			continue
		}

		decimalQuant, err := decimal.NewFromString(transfer.Amount)
		if err != nil {
			panic(err)
		}
		decimalDivisor := decimal.NewFromFloat(1000000)
		amount := decimalQuant.Div(decimalDivisor).InexactFloat64()
		tradeId, err := data.GetTradeIdByWalletAddressAndAmount(token, amount)
		if err != nil {
			panic(err)
		}
		if tradeId == "" {
			continue
		}

		order, err := data.GetOrderInfoByTradeId(tradeId)
		if err != nil {
			panic(err)
		}
		createTime := order.CreatedAt.TimestampMilli()
		if transfer.BlockTimestamp < createTime {
			panic("orders cannot actually be matched")
		}

		req := &request.OrderProcessingRequest{
			Token:              token,
			TradeId:            tradeId,
			Amount:             amount,
			BlockTransactionId: transfer.Hash,
		}
		if err = OrderProcessing(req); err != nil {
			if errors.Is(err, constant.OrderBlockAlreadyProcess) || errors.Is(err, constant.OrderStatusConflict) {
				log.Sugar.Infof("[task] skip already resolved transfer, trade_id=%s, block_transaction_id=%s, err=%v", tradeId, transfer.Hash, err)
				continue
			}
			panic(err)
		}

		msgTpl := `
<b>馃摙馃摙鏈夋柊鐨勪氦鏄撴敮浠樻垚鍔燂紒</b>
<pre>浜ゆ槗鍙凤細%s</pre>
<pre>璁㈠崟鍙凤細%s</pre>
<pre>璇锋眰鏀粯閲戦锛?f cny</pre>
<pre>瀹為檯鏀粯閲戦锛?f usdt</pre>
<pre>閽卞寘鍦板潃锛?s</pre>
<pre>璁㈠崟鍒涘缓鏃堕棿锛?s</pre>
<pre>鏀粯鎴愬姛鏃堕棿锛?s</pre>
`
		msg := fmt.Sprintf(msgTpl, order.TradeId, order.OrderId, order.Amount, order.ActualAmount, order.Token, order.CreatedAt.ToDateTimeString(), carbon.Now().ToDateTimeString())
		telegram.SendToBot(msg)
	}
}

package service

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/assimon/luuu/config"
	"github.com/assimon/luuu/model/dao"
	"github.com/assimon/luuu/model/data"
	"github.com/assimon/luuu/model/mdb"
	"github.com/assimon/luuu/model/request"
	"github.com/assimon/luuu/model/response"
	"github.com/assimon/luuu/util/constant"
	"github.com/assimon/luuu/util/log"
	"github.com/assimon/luuu/util/math"
	"github.com/dromara/carbon/v2"
	"github.com/shopspring/decimal"
)

const (
	CnyMinimumPaymentAmount  = 0.01
	UsdtMinimumPaymentAmount = 0.01
	UsdtAmountPerIncrement   = 0.01
	IncrementalMaximumNumber = 100
)

var gCreateTransactionLock sync.Mutex
var gOrderProcessingLock sync.Mutex

// CreateTransaction creates a new payment order.
func CreateTransaction(req *request.CreateTransactionRequest) (*response.CreateTransactionResponse, error) {
	gCreateTransactionLock.Lock()
	defer gCreateTransactionLock.Unlock()

	payAmount := math.MustParsePrecFloat64(req.Amount, 2)
	decimalPayAmount := decimal.NewFromFloat(payAmount)
	decimalRate := decimal.NewFromFloat(config.GetUsdtRate())
	decimalUsdt := decimalPayAmount.Div(decimalRate)
	if decimalPayAmount.Cmp(decimal.NewFromFloat(CnyMinimumPaymentAmount)) == -1 {
		return nil, constant.PayAmountErr
	}
	if decimalUsdt.Cmp(decimal.NewFromFloat(UsdtMinimumPaymentAmount)) == -1 {
		return nil, constant.PayAmountErr
	}

	exist, err := data.GetOrderInfoByOrderId(req.OrderId)
	if err != nil {
		return nil, err
	}
	if exist.ID > 0 {
		return nil, constant.OrderAlreadyExists
	}

	walletAddress, err := data.GetAvailableWalletAddress()
	if err != nil {
		return nil, err
	}
	if len(walletAddress) <= 0 {
		return nil, constant.NotAvailableWalletAddress
	}

	tradeId := GenerateCode()
	amount := math.MustParsePrecFloat64(decimalUsdt.InexactFloat64(), 2)
	availableToken, availableAmount, err := ReserveAvailableWalletAndAmount(tradeId, amount, walletAddress)
	if err != nil {
		return nil, err
	}
	if availableToken == "" {
		return nil, constant.NotAvailableAmountErr
	}

	tx := dao.Mdb.Begin()
	order := &mdb.Orders{
		TradeId:      tradeId,
		OrderId:      req.OrderId,
		Amount:       req.Amount,
		ActualAmount: availableAmount,
		Token:        availableToken,
		Status:       mdb.StatusWaitPay,
		NotifyUrl:    req.NotifyUrl,
		RedirectUrl:  req.RedirectUrl,
	}
	if err = data.CreateOrderWithTransaction(tx, order); err != nil {
		tx.Rollback()
		_ = data.UnLockTransaction(availableToken, availableAmount)
		return nil, err
	}
	if err = tx.Commit().Error; err != nil {
		tx.Rollback()
		_ = data.UnLockTransaction(availableToken, availableAmount)
		return nil, err
	}

	expirationTime := carbon.Now().AddMinutes(config.GetOrderExpirationTime()).Timestamp()
	resp := &response.CreateTransactionResponse{
		TradeId:        order.TradeId,
		OrderId:        order.OrderId,
		Amount:         order.Amount,
		ActualAmount:   order.ActualAmount,
		Token:          order.Token,
		ExpirationTime: expirationTime,
		PaymentUrl:     fmt.Sprintf("%s/pay/checkout-counter/%s", config.GetAppUri(), order.TradeId),
	}
	return resp, nil
}

// OrderProcessing marks an order as paid and releases its sqlite reservation.
func OrderProcessing(req *request.OrderProcessingRequest) error {
	gOrderProcessingLock.Lock()
	defer gOrderProcessingLock.Unlock()

	tx := dao.Mdb.Begin()
	exist, err := data.GetOrderByBlockIdWithTransaction(tx, req.BlockTransactionId)
	if err != nil {
		tx.Rollback()
		return err
	}
	if exist.ID > 0 {
		tx.Rollback()
		return constant.OrderBlockAlreadyProcess
	}

	updated, err := data.OrderSuccessWithTransaction(tx, req)
	if err != nil {
		tx.Rollback()
		return err
	}
	if !updated {
		tx.Rollback()
		return constant.OrderStatusConflict
	}
	if err = tx.Commit().Error; err != nil {
		tx.Rollback()
		return err
	}

	if err = data.UnLockTransaction(req.Token, req.Amount); err != nil {
		log.Sugar.Warnf("[order] unlock transaction after pay success failed, trade_id=%s, err=%v", req.TradeId, err)
	}
	return nil
}

// ReserveAvailableWalletAndAmount finds and locks a token+amount pair.
func ReserveAvailableWalletAndAmount(tradeId string, amount float64, walletAddress []mdb.WalletAddress) (string, float64, error) {
	availableToken := ""
	availableAmount := amount

	tryLockWalletFunc := func(targetAmount float64) (string, error) {
		for _, address := range walletAddress {
			err := data.LockTransaction(address.Token, tradeId, targetAmount, config.GetOrderExpirationTimeDuration())
			if err == nil {
				return address.Token, nil
			}
			if errors.Is(err, data.ErrTransactionLocked) {
				continue
			}
			return "", err
		}
		return "", nil
	}

	for i := 0; i < IncrementalMaximumNumber; i++ {
		token, err := tryLockWalletFunc(availableAmount)
		if err != nil {
			return "", 0, err
		}
		if token == "" {
			decimalOldAmount := decimal.NewFromFloat(availableAmount)
			decimalIncr := decimal.NewFromFloat(UsdtAmountPerIncrement)
			availableAmount = decimalOldAmount.Add(decimalIncr).InexactFloat64()
			continue
		}
		availableToken = token
		break
	}
	return availableToken, availableAmount, nil
}

// GenerateCode creates a unique trade id.
func GenerateCode() string {
	date := time.Now().Format("20060102")
	r := rand.Intn(1000)
	return fmt.Sprintf("%s%d%03d", date, time.Now().UnixNano()/1e6, r)
}

// GetOrderInfoByTradeId returns a validated order.
func GetOrderInfoByTradeId(tradeId string) (*mdb.Orders, error) {
	order, err := data.GetOrderInfoByTradeId(tradeId)
	if err != nil {
		return nil, err
	}
	if order.ID <= 0 {
		return nil, constant.OrderNotExists
	}
	return order, nil
}

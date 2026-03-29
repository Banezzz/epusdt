package mq

import (
	"errors"
	"net/http"
	"time"

	"github.com/assimon/luuu/config"
	"github.com/assimon/luuu/model/dao"
	"github.com/assimon/luuu/model/data"
	"github.com/assimon/luuu/model/mdb"
	"github.com/assimon/luuu/model/response"
	"github.com/assimon/luuu/util/http_client"
	"github.com/assimon/luuu/util/log"
	"github.com/assimon/luuu/util/sign"
)

const batchSize = 100

func runOrderExpirationLoop() {
	runLoop("order_expiration", processExpiredOrders)
}

func runOrderCallbackLoop() {
	runLoop("order_callback", dispatchPendingCallbacks)
}

func runTransactionLockCleanupLoop() {
	runLoop("transaction_lock_cleanup", cleanupExpiredTransactionLocks)
}

func runLoop(name string, fn func()) {
	safeRun(name, fn)
	ticker := time.NewTicker(config.GetQueuePollInterval())
	defer ticker.Stop()

	for range ticker.C {
		safeRun(name, fn)
	}
}

func safeRun(name string, fn func()) {
	defer func() {
		if err := recover(); err != nil {
			log.Sugar.Errorf("[mq] %s panic: %v", name, err)
		}
	}()
	fn()
}

func processExpiredOrders() {
	expirationCutoff := time.Now().Add(-config.GetOrderExpirationTimeDuration())
	for {
		var orders []mdb.Orders
		err := dao.Mdb.Model(&mdb.Orders{}).
			Where("status = ?", mdb.StatusWaitPay).
			Where("created_at <= ?", expirationCutoff).
			Order("id asc").
			Limit(batchSize).
			Find(&orders).Error
		if err != nil {
			log.Sugar.Errorf("[mq] query expired orders failed: %v", err)
			return
		}
		if len(orders) == 0 {
			return
		}

		for _, order := range orders {
			expired, err := data.UpdateOrderIsExpirationById(order.ID, expirationCutoff)
			if err != nil {
				log.Sugar.Errorf("[mq] expire order failed, trade_id=%s, err=%v", order.TradeId, err)
				continue
			}
			if !expired {
				continue
			}
			if err = data.UnLockTransaction(order.Token, order.ActualAmount); err != nil {
				log.Sugar.Warnf("[mq] release expired transaction lock failed, trade_id=%s, err=%v", order.TradeId, err)
			}
		}

		if len(orders) < batchSize {
			return
		}
	}
}

func dispatchPendingCallbacks() {
	maxRetry := config.GetOrderNoticeMaxRetry()
	orders, err := data.GetPendingCallbackOrders(maxRetry, batchSize)
	if err != nil {
		log.Sugar.Errorf("[mq] query callback orders failed: %v", err)
		return
	}

	now := time.Now()
	for _, order := range orders {
		if !isCallbackDue(&order, now, maxRetry) {
			continue
		}
		if _, loaded := callbackInflight.LoadOrStore(order.TradeId, struct{}{}); loaded {
			continue
		}

		select {
		case callbackLimiter <- struct{}{}:
			go processCallback(order)
		default:
			callbackInflight.Delete(order.TradeId)
			return
		}
	}
}

func processCallback(order mdb.Orders) {
	defer func() {
		<-callbackLimiter
		callbackInflight.Delete(order.TradeId)
	}()

	freshOrder, err := data.GetOrderInfoByTradeId(order.TradeId)
	if err != nil {
		log.Sugar.Errorf("[mq] reload callback order failed, trade_id=%s, err=%v", order.TradeId, err)
		return
	}
	if freshOrder.ID <= 0 || freshOrder.Status != mdb.StatusPaySuccess || freshOrder.CallBackConfirm != mdb.CallBackConfirmNo {
		return
	}

	if err = sendOrderCallback(freshOrder); err != nil {
		log.Sugar.Warnf("[mq] callback request failed, trade_id=%s, attempt=%d, err=%v", freshOrder.TradeId, freshOrder.CallbackNum+1, err)
		freshOrder.CallBackConfirm = mdb.CallBackConfirmNo
	} else {
		freshOrder.CallBackConfirm = mdb.CallBackConfirmOk
	}

	if err = data.SaveCallBackOrdersResp(freshOrder); err != nil {
		log.Sugar.Errorf("[mq] save callback result failed, trade_id=%s, err=%v", freshOrder.TradeId, err)
	}
}

func sendOrderCallback(order *mdb.Orders) error {
	client := http_client.GetHttpClient()
	orderResp := response.OrderNotifyResponse{
		TradeId:            order.TradeId,
		OrderId:            order.OrderId,
		Amount:             order.Amount,
		ActualAmount:       order.ActualAmount,
		Token:              order.Token,
		BlockTransactionId: order.BlockTransactionId,
		Status:             mdb.StatusPaySuccess,
	}
	signature, err := sign.Get(orderResp, config.GetApiAuthToken())
	if err != nil {
		return err
	}
	orderResp.Signature = signature

	resp, err := client.R().
		SetHeader("powered-by", "Epusdt(https://github.com/GMwalletApp/epusdt)").
		SetBody(orderResp).
		Post(order.NotifyUrl)
	if err != nil {
		return err
	}
	if resp.StatusCode() != http.StatusOK {
		return errors.New(resp.Status())
	}
	if string(resp.Body()) != "ok" {
		return errors.New("not ok")
	}
	return nil
}

func cleanupExpiredTransactionLocks() {
	if err := data.CleanupExpiredTransactionLocks(); err != nil {
		log.Sugar.Errorf("[mq] cleanup expired transaction locks failed: %v", err)
	}
}

func isCallbackDue(order *mdb.Orders, now time.Time, maxRetry int) bool {
	if order.CallBackConfirm != mdb.CallBackConfirmNo {
		return false
	}
	if order.CallbackNum > maxRetry {
		return false
	}
	if order.CallbackNum == 0 {
		return true
	}
	nextRunAt := order.UpdatedAt.StdTime().Add(callbackRetryDelay(order.CallbackNum))
	return !nextRunAt.After(now)
}

func callbackRetryDelay(attempts int) time.Duration {
	if attempts <= 0 {
		return 0
	}

	delay := config.GetCallbackRetryBaseDuration()
	maxDelay := 5 * time.Minute
	for i := 1; i < attempts; i++ {
		if delay >= maxDelay/2 {
			return maxDelay
		}
		delay *= 2
	}
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}

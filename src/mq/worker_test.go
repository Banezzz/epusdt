package mq

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assimon/luuu/internal/testutil"
	"github.com/assimon/luuu/model/dao"
	"github.com/assimon/luuu/model/data"
	"github.com/assimon/luuu/model/mdb"
)

func TestProcessExpiredOrdersExpiresWaitingOrdersAndReleasesLocks(t *testing.T) {
	cleanup := testutil.SetupTestDatabases(t)
	defer cleanup()

	order := &mdb.Orders{
		TradeId:      "trade_expired",
		OrderId:      "order_expired",
		Amount:       1,
		ActualAmount: 1,
		Token:        "wallet_1",
		Status:       mdb.StatusWaitPay,
		NotifyUrl:    "https://merchant.example/callback",
	}
	if err := dao.Mdb.Create(order).Error; err != nil {
		t.Fatalf("create expired order: %v", err)
	}
	if err := dao.Mdb.Model(order).UpdateColumn("created_at", time.Now().Add(-20*time.Minute)).Error; err != nil {
		t.Fatalf("age expired order: %v", err)
	}
	if err := data.LockTransaction(order.Token, order.TradeId, order.ActualAmount, time.Hour); err != nil {
		t.Fatalf("lock expired order: %v", err)
	}

	recentOrder := &mdb.Orders{
		TradeId:      "trade_recent",
		OrderId:      "order_recent",
		Amount:       1,
		ActualAmount: 1.01,
		Token:        "wallet_1",
		Status:       mdb.StatusWaitPay,
		NotifyUrl:    "https://merchant.example/callback",
	}
	if err := dao.Mdb.Create(recentOrder).Error; err != nil {
		t.Fatalf("create recent order: %v", err)
	}
	if err := data.LockTransaction(recentOrder.Token, recentOrder.TradeId, recentOrder.ActualAmount, time.Hour); err != nil {
		t.Fatalf("lock recent order: %v", err)
	}

	processExpiredOrders()

	expired, err := data.GetOrderInfoByTradeId(order.TradeId)
	if err != nil {
		t.Fatalf("reload expired order: %v", err)
	}
	if expired.Status != mdb.StatusExpired {
		t.Fatalf("expired order status = %d, want %d", expired.Status, mdb.StatusExpired)
	}
	lockTradeID, err := data.GetTradeIdByWalletAddressAndAmount(order.Token, order.ActualAmount)
	if err != nil {
		t.Fatalf("expired order lock lookup: %v", err)
	}
	if lockTradeID != "" {
		t.Fatalf("expired order lock still exists: %s", lockTradeID)
	}

	recent, err := data.GetOrderInfoByTradeId(recentOrder.TradeId)
	if err != nil {
		t.Fatalf("reload recent order: %v", err)
	}
	if recent.Status != mdb.StatusWaitPay {
		t.Fatalf("recent order status = %d, want %d", recent.Status, mdb.StatusWaitPay)
	}
	lockTradeID, err = data.GetTradeIdByWalletAddressAndAmount(recentOrder.Token, recentOrder.ActualAmount)
	if err != nil {
		t.Fatalf("recent order lock lookup: %v", err)
	}
	if lockTradeID != recentOrder.TradeId {
		t.Fatalf("recent order lock = %s, want %s", lockTradeID, recentOrder.TradeId)
	}
}

func TestProcessExpiredOrdersKeepsPaidOrdersPaid(t *testing.T) {
	cleanup := testutil.SetupTestDatabases(t)
	defer cleanup()

	order := &mdb.Orders{
		TradeId:            "trade_paid",
		OrderId:            "order_paid",
		Amount:             1,
		ActualAmount:       1,
		Token:              "wallet_1",
		Status:             mdb.StatusPaySuccess,
		NotifyUrl:          "https://merchant.example/callback",
		BlockTransactionId: "block_paid",
		CallBackConfirm:    mdb.CallBackConfirmNo,
	}
	if err := dao.Mdb.Create(order).Error; err != nil {
		t.Fatalf("create paid order: %v", err)
	}
	if err := dao.Mdb.Model(order).UpdateColumn("created_at", time.Now().Add(-20*time.Minute)).Error; err != nil {
		t.Fatalf("age paid order: %v", err)
	}

	processExpiredOrders()

	current, err := data.GetOrderInfoByTradeId(order.TradeId)
	if err != nil {
		t.Fatalf("reload paid order: %v", err)
	}
	if current.Status != mdb.StatusPaySuccess {
		t.Fatalf("paid order status = %d, want %d", current.Status, mdb.StatusPaySuccess)
	}
	if current.BlockTransactionId != "block_paid" {
		t.Fatalf("paid order block transaction id = %s, want block_paid", current.BlockTransactionId)
	}
}

func TestDispatchPendingCallbacksHonorsBackoffAndPersistsSuccess(t *testing.T) {
	cleanup := testutil.SetupTestDatabases(t)
	defer cleanup()

	callbackLimiter = make(chan struct{}, 1)
	callbackInflight = sync.Map{}

	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	order := &mdb.Orders{
		TradeId:            "trade_callback",
		OrderId:            "order_callback",
		Amount:             1,
		ActualAmount:       1,
		Token:              "wallet_1",
		Status:             mdb.StatusPaySuccess,
		NotifyUrl:          server.URL,
		BlockTransactionId: "block_callback",
		CallbackNum:        1,
		CallBackConfirm:    mdb.CallBackConfirmNo,
	}
	if err := dao.Mdb.Create(order).Error; err != nil {
		t.Fatalf("create callback order: %v", err)
	}

	dispatchPendingCallbacks()
	time.Sleep(200 * time.Millisecond)
	if got := atomic.LoadInt32(&requestCount); got != 0 {
		t.Fatalf("unexpected callback count before backoff elapsed: %d", got)
	}

	if err := dao.Mdb.Model(order).UpdateColumn("updated_at", time.Now().Add(-2*time.Second)).Error; err != nil {
		t.Fatalf("age callback order: %v", err)
	}
	dispatchPendingCallbacks()

	waitFor(t, 3*time.Second, func() bool {
		current, err := data.GetOrderInfoByTradeId(order.TradeId)
		if err != nil || current.ID <= 0 {
			return false
		}
		return current.CallBackConfirm == mdb.CallBackConfirmOk && current.CallbackNum == 2
	})

	if got := atomic.LoadInt32(&requestCount); got != 1 {
		t.Fatalf("callback request count = %d, want 1", got)
	}
}

func waitFor(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not satisfied before timeout")
}

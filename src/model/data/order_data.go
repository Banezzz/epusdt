package data

import (
	"errors"
	"time"

	"github.com/assimon/luuu/model/dao"
	"github.com/assimon/luuu/model/mdb"
	"github.com/assimon/luuu/model/request"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrTransactionLocked = errors.New("transaction amount is already locked")

func normalizeLockAmount(amount float64) (int64, string) {
	value := decimal.NewFromFloat(amount).Round(2)
	return value.Shift(2).IntPart(), value.StringFixed(2)
}

// GetOrderInfoByOrderId fetches an order by merchant order id.
func GetOrderInfoByOrderId(orderId string) (*mdb.Orders, error) {
	order := new(mdb.Orders)
	err := dao.Mdb.Model(order).Limit(1).Find(order, "order_id = ?", orderId).Error
	return order, err
}

// GetOrderInfoByTradeId fetches an order by epusdt trade id.
func GetOrderInfoByTradeId(tradeId string) (*mdb.Orders, error) {
	order := new(mdb.Orders)
	err := dao.Mdb.Model(order).Limit(1).Find(order, "trade_id = ?", tradeId).Error
	return order, err
}

// CreateOrderWithTransaction creates an order in the active database transaction.
func CreateOrderWithTransaction(tx *gorm.DB, order *mdb.Orders) error {
	return tx.Model(order).Create(order).Error
}

// GetOrderByBlockIdWithTransaction fetches an order by blockchain tx id.
func GetOrderByBlockIdWithTransaction(tx *gorm.DB, blockId string) (*mdb.Orders, error) {
	order := new(mdb.Orders)
	err := tx.Model(order).Limit(1).Find(order, "block_transaction_id = ?", blockId).Error
	return order, err
}

// OrderSuccessWithTransaction marks an order as paid only if it is still waiting for payment.
func OrderSuccessWithTransaction(tx *gorm.DB, req *request.OrderProcessingRequest) (bool, error) {
	result := tx.Model(&mdb.Orders{}).
		Where("trade_id = ?", req.TradeId).
		Where("status = ?", mdb.StatusWaitPay).
		Updates(map[string]interface{}{
			"block_transaction_id": req.BlockTransactionId,
			"status":               mdb.StatusPaySuccess,
			"callback_confirm":     mdb.CallBackConfirmNo,
		})
	return result.RowsAffected > 0, result.Error
}

// GetPendingCallbackOrders returns orders that still need callback delivery.
func GetPendingCallbackOrders(maxRetry int, limit int) ([]mdb.Orders, error) {
	var orders []mdb.Orders
	query := dao.Mdb.Model(&mdb.Orders{}).
		Where("callback_num <= ?", maxRetry).
		Where("callback_confirm = ?", mdb.CallBackConfirmNo).
		Where("status = ?", mdb.StatusPaySuccess).
		Order("updated_at asc")
	if limit > 0 {
		query = query.Limit(limit)
	}
	err := query.Find(&orders).Error
	return orders, err
}

// SaveCallBackOrdersResp persists a callback attempt result.
func SaveCallBackOrdersResp(order *mdb.Orders) error {
	return dao.Mdb.Model(order).
		Where("id = ?", order.ID).
		Where("callback_confirm = ?", mdb.CallBackConfirmNo).
		Updates(map[string]interface{}{
			"callback_num":     gorm.Expr("callback_num + ?", 1),
			"callback_confirm": order.CallBackConfirm,
		}).Error
}

// UpdateOrderIsExpirationById expires an order only if it is still pending and already timed out.
func UpdateOrderIsExpirationById(id uint64, expirationCutoff time.Time) (bool, error) {
	result := dao.Mdb.Model(mdb.Orders{}).
		Where("id = ?", id).
		Where("status = ?", mdb.StatusWaitPay).
		Where("created_at <= ?", expirationCutoff).
		Update("status", mdb.StatusExpired)
	return result.RowsAffected > 0, result.Error
}

// GetTradeIdByWalletAddressAndAmount resolves the reserved trade id by token and amount.
func GetTradeIdByWalletAddressAndAmount(token string, amount float64) (string, error) {
	scaledAmount, _ := normalizeLockAmount(amount)
	var lock mdb.TransactionLock
	err := dao.RuntimeDB.Model(&mdb.TransactionLock{}).
		Where("token = ?", token).
		Where("amount_scaled = ?", scaledAmount).
		Where("expires_at > ?", time.Now()).
		Limit(1).
		Find(&lock).Error
	if err != nil {
		return "", err
	}
	if lock.ID <= 0 {
		return "", nil
	}
	return lock.TradeId, nil
}

// LockTransaction reserves a token+amount pair in sqlite until expiration.
func LockTransaction(token, tradeId string, amount float64, expirationTime time.Duration) error {
	scaledAmount, amountText := normalizeLockAmount(amount)
	now := time.Now()
	lock := &mdb.TransactionLock{
		Token:        token,
		AmountScaled: scaledAmount,
		AmountText:   amountText,
		TradeId:      tradeId,
		ExpiresAt:    now.Add(expirationTime),
	}

	return dao.RuntimeDB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("token = ?", token).
			Where("amount_scaled = ?", scaledAmount).
			Where("expires_at <= ?", now).
			Delete(&mdb.TransactionLock{}).Error; err != nil {
			return err
		}
		if err := tx.Where("trade_id = ?", tradeId).Delete(&mdb.TransactionLock{}).Error; err != nil {
			return err
		}

		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(lock)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrTransactionLocked
		}
		return nil
	})
}

// UnLockTransaction releases the reservation for token+amount.
func UnLockTransaction(token string, amount float64) error {
	scaledAmount, _ := normalizeLockAmount(amount)
	return dao.RuntimeDB.Where("token = ?", token).Where("amount_scaled = ?", scaledAmount).Delete(&mdb.TransactionLock{}).Error
}

func UnLockTransactionByTradeId(tradeId string) error {
	return dao.RuntimeDB.Where("trade_id = ?", tradeId).Delete(&mdb.TransactionLock{}).Error
}

func CleanupExpiredTransactionLocks() error {
	return dao.RuntimeDB.Where("expires_at <= ?", time.Now()).Delete(&mdb.TransactionLock{}).Error
}

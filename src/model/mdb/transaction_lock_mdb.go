package mdb

import "time"

type TransactionLock struct {
	ID           uint64    `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Token        string    `gorm:"column:token;uniqueIndex:transaction_lock_token_amount_uindex,priority:1" json:"token"`
	AmountScaled int64     `gorm:"column:amount_scaled;uniqueIndex:transaction_lock_token_amount_uindex,priority:2" json:"amount_scaled"`
	AmountText   string    `gorm:"column:amount_text" json:"amount_text"`
	TradeId      string    `gorm:"column:trade_id;index:transaction_lock_trade_id_index" json:"trade_id"`
	ExpiresAt    time.Time `gorm:"column:expires_at;index:transaction_lock_expires_at_index" json:"expires_at"`
	CreatedAt    time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (t *TransactionLock) TableName() string {
	return "transaction_lock"
}

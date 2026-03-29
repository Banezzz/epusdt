package mdb

const (
	StatusWaitPay     = 1
	StatusPaySuccess  = 2
	StatusExpired     = 3
	CallBackConfirmOk = 1
	CallBackConfirmNo = 2
)

type Orders struct {
	TradeId            string  `gorm:"column:trade_id;uniqueIndex:orders_trade_id_uindex" json:"trade_id"`
	OrderId            string  `gorm:"column:order_id;uniqueIndex:orders_order_id_uindex" json:"order_id"`
	BlockTransactionId string  `gorm:"index:orders_block_transaction_id_index;column:block_transaction_id" json:"block_transaction_id"`
	Amount             float64 `gorm:"column:amount" json:"amount"`
	ActualAmount       float64 `gorm:"column:actual_amount" json:"actual_amount"`
	Token              string  `gorm:"column:token" json:"token"`
	Status             int     `gorm:"column:status;default:1" json:"status"`
	NotifyUrl          string  `gorm:"column:notify_url" json:"notify_url"`
	RedirectUrl        string  `gorm:"column:redirect_url" json:"redirect_url"`
	CallbackNum        int     `gorm:"column:callback_num;default:0" json:"callback_num"`
	CallBackConfirm    int     `gorm:"column:callback_confirm;default:2" json:"callback_confirm"`
	BaseModel
}

func (o *Orders) TableName() string {
	return "orders"
}

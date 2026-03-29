package mdb

const (
	TokenStatusEnable  = 1
	TokenStatusDisable = 2
)

type WalletAddress struct {
	Token  string `gorm:"column:token;uniqueIndex:wallet_address_token_uindex" json:"token"`
	Status int64  `gorm:"column:status;default:1" json:"status"`
	BaseModel
}

func (w *WalletAddress) TableName() string {
	return "wallet_address"
}

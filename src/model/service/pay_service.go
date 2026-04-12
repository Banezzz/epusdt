package service

import (
	"errors"

	"github.com/assimon/luuu/config"
	"github.com/assimon/luuu/model/data"
	"github.com/assimon/luuu/model/mdb"
	"github.com/assimon/luuu/model/response"
)

var ErrOrder = errors.New("不存在待支付订单或已过期")

// GetCheckoutCounterByTradeId returns checkout info for a pending order.
func GetCheckoutCounterByTradeId(tradeId string) (*response.CheckoutCounterResponse, error) {
	orderInfo, err := data.GetOrderInfoByTradeId(tradeId)
	if err != nil {
		return nil, err
	}
	if orderInfo.ID <= 0 || orderInfo.Status != mdb.StatusWaitPay {
		return nil, ErrOrder
	}

	resp := &response.CheckoutCounterResponse{
		TradeId:        orderInfo.TradeId,
		Amount:         orderInfo.Amount,
		ActualAmount:   orderInfo.ActualAmount,
		Token:          orderInfo.Token,
		Currency:       orderInfo.Currency,
		ReceiveAddress: orderInfo.ReceiveAddress,
		Network:        orderInfo.Network,
		ExpirationTime: orderInfo.CreatedAt.AddMinutes(config.GetOrderExpirationTime()).TimestampMilli(),
		RedirectUrl:    orderInfo.RedirectUrl,
		CreatedAt:      orderInfo.CreatedAt.TimestampMilli(),
		IsSelected:     orderInfo.IsSelected,
	}
	return resp, nil
}

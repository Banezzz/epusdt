package comm

import (
	"net/http"

	"github.com/Banezzz/ezpay/model/response"
	"github.com/Banezzz/ezpay/model/service"
	"github.com/labstack/echo/v4"
)

// CheckoutCounter 收银台
// @Summary      Checkout counter page
// @Description  Render the payment checkout counter JSON response for a given trade
// @Tags         Payment
// @Produce      json
// @Param        trade_id path string true "Trade ID"
// @Success      200 {object} response.CheckoutCounterResponse
// @Router       /pay/checkout-counter-resp/{trade_id} [get]
func (c *BaseCommController) CheckoutCounter(ctx echo.Context) (err error) {
	tradeId := ctx.Param("trade_id")
	resp, err := service.GetCheckoutCounterByTradeId(tradeId)
	if err != nil {
		if err == service.ErrOrder {
			// Unknown trade id: render the page with empty payload
			// (client side shows a friendly "order not found" screen).
			emptyResp := response.CheckoutCounterResponse{}
			return c.SucJson(ctx, emptyResp)
		}
		return ctx.String(http.StatusInternalServerError, err.Error())
	}

	return c.SucJson(ctx, resp)
}

// CheckStatus 支付状态检测
// @Summary      Check payment status
// @Description  Check the payment status of an order by trade ID
// @Tags         Payment
// @Produce      json
// @Param        trade_id path string true "Trade ID"
// @Success      200 {object} response.ApiResponse{data=response.CheckStatusResponse}
// @Failure      400 {object} response.ApiResponse
// @Router       /pay/check-status/{trade_id} [get]
func (c *BaseCommController) CheckStatus(ctx echo.Context) (err error) {
	tradeId := ctx.Param("trade_id")
	order, err := service.GetOrderInfoByTradeId(tradeId)
	if err != nil {
		return c.FailJson(ctx, err)
	}
	resp := response.CheckStatusResponse{
		TradeId: order.TradeId,
		Status:  order.Status,
	}
	return c.SucJson(ctx, resp)
}

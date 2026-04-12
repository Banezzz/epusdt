package comm

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"

	"github.com/assimon/luuu/config"
	"github.com/assimon/luuu/model/response"
	"github.com/assimon/luuu/model/service"
	"github.com/labstack/echo/v4"
)

// CheckoutCounter 收银台
func (c *BaseCommController) CheckoutCounter(ctx echo.Context) (err error) {
	tradeId := ctx.Param("trade_id")
	resp, err := service.GetCheckoutCounterByTradeId(tradeId)
	if err != nil {
		if err == service.ErrOrder {
			tmpl, err := template.ParseFiles(filepath.Join(config.StaticFilePath, "index.html"))
			if err != nil {
				return ctx.String(http.StatusOK, err.Error())
			}
			emptyResp := response.CheckoutCounterResponse{}
			return tmpl.Execute(ctx.Response(), emptyResp)
		}
		return ctx.String(http.StatusOK, err.Error())
	}
	tmpl, err := template.ParseFiles(filepath.Join(config.StaticFilePath, "index.html"))
	if err != nil {
		return ctx.String(http.StatusOK, err.Error())
	}

	jsonByte, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return ctx.String(http.StatusOK, err.Error())
	}
	fmt.Printf("%v\n", string(jsonByte))

	return tmpl.Execute(ctx.Response(), resp)
}

// CheckStatus 支付状态检测
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

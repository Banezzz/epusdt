package service

import (
	"strings"

	"github.com/Banezzz/ezpay/model/data"
)

func IsSupportedPaymentAsset(network, token string) bool {
	if strings.TrimSpace(network) == "" || strings.TrimSpace(token) == "" {
		return false
	}
	row, err := data.GetEnabledChainTokenBySymbol(network, token)
	return err == nil && row.ID > 0
}

package service

import (
	"strings"

	"github.com/GMWalletApp/epusdt/model/data"
)

func IsSupportedPaymentAsset(network, token string) bool {
	if strings.TrimSpace(network) == "" || strings.TrimSpace(token) == "" {
		return false
	}
	row, err := data.GetEnabledChainTokenBySymbol(network, token)
	return err == nil && row.ID > 0
}

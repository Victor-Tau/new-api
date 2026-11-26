package setting

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

const (
	StripeSmallTopUpFeeOption        = "StripeSmallTopUpFeeSettings"
	DefaultStripeSmallTopUpFeeOption = `{"amount":0,"waiver_cny":50}`
)

type StripeSmallTopUpFeeSettings struct {
	Amount    decimal.Decimal
	WaiverCNY decimal.Decimal
}

func ParseStripeTopUpFeeSettings(raw string) (StripeSmallTopUpFeeSettings, error) {
	var values struct {
		Amount    *decimal.Decimal `json:"amount"`
		WaiverCNY *decimal.Decimal `json:"waiver_cny"`
	}
	if err := common.UnmarshalJsonStr(raw, &values); err != nil {
		return StripeSmallTopUpFeeSettings{}, common.NewMessage("Invalid parameters")
	}
	for _, amount := range []*decimal.Decimal{values.Amount, values.WaiverCNY} {
		if amount == nil || amount.IsNegative() || amount.Exponent() < -2 || amount.Exponent() > 11 ||
			amount.GreaterThan(decimal.New(999999999999, -2)) {
			return StripeSmallTopUpFeeSettings{}, common.NewMessage("Invalid parameters")
		}
	}
	return StripeSmallTopUpFeeSettings{Amount: *values.Amount, WaiverCNY: *values.WaiverCNY}, nil
}

var StripeApiSecret = ""
var StripeWebhookSecret = ""
var StripePriceId = ""
var StripeUnitPrice = 8.0
var StripeMinTopUp = 1
var StripePromotionCodesEnabled = false

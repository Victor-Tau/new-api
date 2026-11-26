package controller

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v81"
	"gorm.io/gorm"
)

func TestTopUpQuotaValidation(t *testing.T) {
	oldQuotaPerUnit := common.QuotaPerUnit
	oldDisplayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	common.QuotaPerUnit = 500000
	t.Cleanup(func() {
		common.QuotaPerUnit = oldQuotaPerUnit
		operation_setting.GetGeneralSetting().QuotaDisplayType = oldDisplayType
	})

	testCases := []struct {
		name        string
		displayType string
		amount      int64
		wantQuota   int
		wantErr     bool
	}{
		{
			name:        "currency amount below limit",
			displayType: operation_setting.QuotaDisplayTypeUSD,
			amount:      4294,
			wantQuota:   2_147_000_000,
		},
		{
			name:        "currency amount above limit",
			displayType: operation_setting.QuotaDisplayTypeUSD,
			amount:      4295,
			wantQuota:   2_147_500_000,
		},
		{
			name:        "token amount preserves settlement truncation",
			displayType: operation_setting.QuotaDisplayTypeTokens,
			amount:      2_147_500_000,
			wantQuota:   2_147_500_000,
		},
		{
			name:        "token amount above legacy int32 range",
			displayType: operation_setting.QuotaDisplayTypeTokens,
			amount:      4_294_500_000,
			wantQuota:   4_294_500_000,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			operation_setting.GetGeneralSetting().QuotaDisplayType = tc.displayType
			quota, err := getTopUpQuota(tc.amount)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantQuota, quota)
		})
	}
}

func TestValidateTopUpQuotaReturnsMaximumAmount(t *testing.T) {
	oldQuotaPerUnit := common.QuotaPerUnit
	oldDisplayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	common.QuotaPerUnit = 500000
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD
	t.Cleanup(func() {
		common.QuotaPerUnit = oldQuotaPerUnit
		operation_setting.GetGeneralSetting().QuotaDisplayType = oldDisplayType
	})

	maxAmount := decimal.NewFromInt(common.MaxWalletQuota).
		Div(decimal.NewFromFloat(common.QuotaPerUnit)).
		Floor().IntPart()

	_, err := validateTopUpQuota(maxAmount)
	require.NoError(t, err)
	_, err = validateTopUpQuota(maxAmount + 1)
	require.EqualError(t, err, fmt.Sprintf("Top-up amount cannot exceed %d", maxAmount))
}

func TestRequestAmountRejectsTopUpThatCannotBeSettled(t *testing.T) {
	oldQuotaPerUnit := common.QuotaPerUnit
	oldDisplayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	common.QuotaPerUnit = 500000
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD
	t.Cleanup(func() {
		common.QuotaPerUnit = oldQuotaPerUnit
		operation_setting.GetGeneralSetting().QuotaDisplayType = oldDisplayType
	})

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	maxAmount := decimal.NewFromInt(common.MaxWalletQuota).
		Div(decimal.NewFromFloat(common.QuotaPerUnit)).
		Floor().IntPart()
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/user/amount",
		strings.NewReader(fmt.Sprintf(`{"amount":%d}`, maxAmount+1)),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")

	RequestAmount(ctx)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.JSONEq(t, fmt.Sprintf(`{"message":"error","data":"Top-up amount cannot exceed %d","message_key":"Top-up amount cannot exceed {{max}}","message_params":{"max":%d}}`, maxAmount, maxAmount), recorder.Body.String())
}

func TestRequestAmountRejectsTopUpThatWouldOverflowWallet(t *testing.T) {
	oldQuotaPerUnit := common.QuotaPerUnit
	oldDisplayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	oldDB := model.DB
	common.QuotaPerUnit = 500000
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}))
	model.DB = db
	t.Cleanup(func() {
		common.QuotaPerUnit = oldQuotaPerUnit
		operation_setting.GetGeneralSetting().QuotaDisplayType = oldDisplayType
		model.DB = oldDB
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			require.NoError(t, sqlDB.Close())
		}
	})

	require.NoError(t, model.DB.Create(&model.User{
		Id:       42,
		Username: "topup_capacity_user",
		Quota:    common.MaxWalletQuota - 100_000,
		Status:   common.UserStatusEnabled,
	}).Error)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("id", 42)
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/user/amount",
		strings.NewReader(`{"amount":1}`),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")

	RequestAmount(ctx)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.JSONEq(t, `{"message":"error","data":"Top-up quota limit exceeded","message_key":"Top-up quota limit exceeded"}`, recorder.Body.String())
}

func TestValidateCreditedQuotaRejectsOverflow(t *testing.T) {
	_, err := validateCreditedQuota(decimal.NewFromInt(int64(common.MaxWalletQuota / 2)))
	require.NoError(t, err)
	_, err = validateCreditedQuota(decimal.Zero)
	require.EqualError(t, err, "Top-up quota must be greater than 0")
	_, err = validateCreditedQuota(decimal.NewFromInt(common.MaxWalletQuota + 1))
	require.EqualError(
		t,
		err,
		"Top-up quota exceeds the range the system can represent",
	)
}

func TestStripeVIPDiscountChangesCheckoutPriceAndPreservesCredit(t *testing.T) {
	oldQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	t.Cleanup(func() { common.QuotaPerUnit = oldQuotaPerUnit })

	base := &stripe.Price{Product: &stripe.Product{ID: "prod_wallet"}, Currency: "usd", UnitAmount: 500}
	item, err := stripeMembershipLineItem(base, 4, 0.97)
	require.NoError(t, err)
	assert.EqualValues(t, 1940, *item.PriceData.UnitAmount)
	assert.EqualValues(t, 1, *item.Quantity)
	assert.Equal(t, "prod_wallet", *item.PriceData.Product)
	level := 2
	assert.True(t, decimal.NewFromInt(2000000).Equal(getStripeCreditedQuota(stripeWalletAmount(4, 0.97, &level))))
	assert.True(t, decimal.NewFromInt(1940000).Equal(getStripeCreditedQuota(stripeWalletAmount(4, 0.97, nil))))

	_, err = validateCreditedQuota(getStripeCreditedQuota(decimal.NewFromInt(common.MaxWalletQuota)))
	require.Error(t, err)
}

type stripeTopUpTestTransport func(*http.Request) (*http.Response, error)

func (transport stripeTopUpTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return transport(req)
}

func TestStripeSmallTopUpFeePreviewAndCheckout(t *testing.T) {
	db := setupManageUserTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.TopUp{}))
	level := 2
	user := model.User{Username: "stripe-topup", Group: "VIP2", VIPLevel: &level}
	require.NoError(t, db.Create(&user).Error)
	previousOptions := common.OptionMap
	previousRate := operation_setting.USDExchangeRate
	previousDisplay := operation_setting.GetQuotaDisplayType()
	previousPrice, previousKey, previousUnitPrice := setting.StripePriceId, setting.StripeApiSecret, setting.StripeUnitPrice
	previousBackend := stripe.GetBackend(stripe.APIBackend)
	t.Cleanup(func() {
		common.OptionMap = previousOptions
		operation_setting.USDExchangeRate = previousRate
		operation_setting.GetGeneralSetting().QuotaDisplayType = previousDisplay
		setting.StripePriceId, setting.StripeApiSecret, setting.StripeUnitPrice = previousPrice, previousKey, previousUnitPrice
		stripe.SetBackend(stripe.APIBackend, previousBackend)
	})
	common.OptionMap = map[string]string{}
	operation_setting.USDExchangeRate = 5
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeCNY
	setting.StripePriceId, setting.StripeApiSecret, setting.StripeUnitPrice = "price_test", "sk_test_fixture", 1.18

	require.Error(t, model.UpdateOption(setting.StripeSmallTopUpFeeOption, `{"amount":-1,"waiver_cny":50}`))
	require.Error(t, model.UpdateOption(setting.StripeSmallTopUpFeeOption, `{"amount":1,"waiver_cny":50.001}`))

	for _, tc := range []struct {
		name       string
		quantity   int
		settings   string
		fee        string
		topUpMinor string
		total      string
	}{
		{name: "existing installation does not charge a fee", quantity: 10, topUpMinor: "1145", total: "11.45"},
		{name: "default threshold includes exactly fifty CNY", quantity: 10, settings: `{"amount":2.35,"waiver_cny":50}`, fee: "2.35", topUpMinor: "1145", total: "13.80"},
		{name: "above the default threshold is waived", quantity: 11, settings: `{"amount":2.35,"waiver_cny":50}`, topUpMinor: "1259", total: "12.59"},
		{name: "custom threshold charges below the line", quantity: 11, settings: `{"amount":2.35,"waiver_cny":60.50}`, fee: "2.35", topUpMinor: "1259", total: "14.94"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.settings != "" {
				require.NoError(t, model.UpdateOption(setting.StripeSmallTopUpFeeOption, tc.settings))
				var option model.Option
				require.NoError(t, db.First(&option, &model.Option{Key: setting.StripeSmallTopUpFeeOption}).Error)
				assert.Equal(t, tc.settings, option.Value)
			}
			transport := stripeTopUpTestTransport(func(req *http.Request) (*http.Response, error) {
				var body string
				switch req.URL.Path {
				case "/v1/prices/price_test":
					body = `{"id":"price_test","currency":"hkd","unit_amount":118,"billing_scheme":"per_unit","product":"prod_wallet"}`
				case "/v1/checkout/sessions":
					require.NoError(t, req.ParseForm())
					assert.Equal(t, tc.topUpMinor, req.Form.Get("line_items[0][price_data][unit_amount]"))
					if tc.fee == "" {
						assert.Empty(t, req.Form.Get("line_items[1][quantity]"))
					} else {
						assert.Equal(t, "235", req.Form.Get("line_items[1][price_data][unit_amount]"))
						assert.Equal(t, "hkd", req.Form.Get("line_items[1][price_data][currency]"))
						assert.Equal(t, "Small top-up service fee", req.Form.Get("line_items[1][price_data][product_data][name]"))
					}
					body = `{"id":"cs_test_fixture","currency":"hkd","url":"https://checkout.stripe.com/test"}`
				default:
					return nil, fmt.Errorf("unexpected Stripe request: %s", req.URL.Path)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
			})
			stripe.SetBackend(stripe.APIBackend, stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{
				HTTPClient:        &http.Client{Transport: transport},
				MaxNetworkRetries: stripe.Int64(0),
			}))
			body := fmt.Sprintf(`{"amount":%d,"payment_method":"stripe"}`, tc.quantity)
			preview := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(preview)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/user/stripe/amount", strings.NewReader(body))
			ctx.Set("id", user.Id)
			RequestStripeAmount(ctx)
			var quote struct {
				Data       string
				ServiceFee string `json:"service_fee"`
				Currency   string
			}
			require.NoError(t, common.Unmarshal(preview.Body.Bytes(), &quote), preview.Body.String())
			assert.Equal(t, tc.total, quote.Data)
			assert.Equal(t, tc.fee, quote.ServiceFee)
			if tc.fee != "" {
				assert.Equal(t, "HKD", quote.Currency)
			}
			start := httptest.NewRecorder()
			ctx, _ = gin.CreateTestContext(start)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/user/stripe/pay", strings.NewReader(body))
			ctx.Set("id", user.Id)
			RequestStripePay(ctx)
			require.Contains(t, start.Body.String(), "https://checkout.stripe.com/test")
			var stored model.TopUp
			require.NoError(t, db.Last(&stored).Error)
			assert.Equal(t, float64(tc.quantity), stored.Money)
			assert.Equal(t, common.TopUpStatusPending, stored.Status)
		})
	}
}

func TestStripeReferralRebateUsesCapturedPayment(t *testing.T) {
	_, _, invitee := setupAdminUserTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.TopUp{}, &model.SubscriptionOrder{}, &model.ReferralRebate{}, &model.GrowthEvent{}, &model.UserGrowth{}))
	require.NoError(t, model.LOG_DB.AutoMigrate(&model.Log{}))
	compliance := operation_setting.GetPaymentSetting()
	original := *compliance
	compliance.ComplianceConfirmed = true
	compliance.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	t.Cleanup(func() { *compliance = original })

	rate := 2500
	inviter := model.User{
		Username: "stripe-referrer", AffCode: "stripe-aff", ReferralRebateEnabled: true,
		ReferralRebateRate: &rate,
	}
	require.NoError(t, model.DB.Create(&inviter).Error)
	require.NoError(t, model.DB.Model(invitee).UpdateColumn("inviter_id", inviter.Id).Error)
	order := model.TopUp{
		UserId: invitee.Id, TradeNo: "stripe-referral-checkout", Amount: 10, Money: 10,
		PaymentProvider: model.PaymentProviderStripe, Status: common.TopUpStatusPending,
	}
	require.NoError(t, order.Insert())

	// Credit 10 wallet units for the order, but the customer paid only $4.
	var data stripe.EventData
	require.NoError(t, common.Unmarshal([]byte(`{"object":{"amount_total":400,"currency":"usd"}}`), &data))
	event := stripe.Event{Type: stripe.EventTypeCheckoutSessionCompleted, Data: &data}
	fulfillOrder(t.Context(), event, order.TradeNo, "", "")
	fulfillOrder(t.Context(), event, order.TradeNo, "", "")
	stored, err := model.GetUserById(inviter.Id, false)
	require.NoError(t, err)
	assert.Equal(t, common.QuotaFromFloat(common.QuotaPerUnit), stored.Quota)

	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/log/self?p=1&type=1", nil)
	c.Set("id", inviter.Id)
	c.Set("role", common.RoleCommonUser)
	GetUserLogs(c)
	require.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), "Referral rebate / Invited user managed-user")
	assert.Contains(t, response.Body.String(), `"type":1`)
}

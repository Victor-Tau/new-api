package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
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

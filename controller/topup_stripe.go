package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/checkout/session"
	"github.com/stripe/stripe-go/v81/price"
	"github.com/stripe/stripe-go/v81/webhook"
	"github.com/thanhpk/randstr"
)

var stripeAdaptor = &StripeAdaptor{}

// StripePayRequest represents a payment request for Stripe checkout.
type StripePayRequest struct {
	// Amount is the quantity of units to purchase.
	Amount int64 `json:"amount"`
	// PaymentMethod specifies the payment method (e.g., "stripe").
	PaymentMethod string `json:"payment_method"`
	// SuccessURL is the optional custom URL to redirect after successful payment.
	// If empty, defaults to the server's console log page.
	SuccessURL string `json:"success_url,omitempty"`
	// CancelURL is the optional custom URL to redirect when payment is canceled.
	// If empty, defaults to the server's console topup page.
	CancelURL string `json:"cancel_url,omitempty"`
}

type StripeAdaptor struct {
}

func (*StripeAdaptor) RequestAmount(c *gin.Context, req *StripePayRequest) {
	if req.Amount < getStripeMinTopup() {
		paymentError(c, common.NewMessage("Top-up amount cannot be less than {{min}}", map[string]any{"min": getStripeMinTopup()}))
		return
	}
	if req.Amount > 10000 {
		paymentError(c, common.NewMessage("Top-up amount cannot exceed {{max}}", map[string]any{"max": 10000}))
		return
	}
	id := c.GetInt("id")
	membership, err := model.GetUserVIPMembership(id)
	if err != nil {
		paymentError(c, common.NewMessage("Failed to get the user group"))
		return
	}
	ratio := common.GetUserTopupGroupRatio(membership.Group, membership.VIPLevel)
	if rejectInvalidCreditedQuota(c, id, getStripeCreditedQuota(stripeWalletAmount(req.Amount, ratio, membership.VIPLevel))) {
		return
	}
	fee, err := stripeSmallTopUpFee(req.Amount)
	if err != nil {
		logger.LogError(c.Request.Context(), common.LogText("Failed to calculate Stripe top-up service fee user_id=%d error=%q", id, err.Error()))
		paymentError(c, common.NewMessage("Failed to calculate payment amount"))
		return
	}
	if fee.IsPositive() {
		if membership.VIPLevel == nil {
			ratio = 1
		}
		_, total, currency, err := stripeTopUpCheckoutItems(c.Request.Context(), req.Amount, ratio, fee)
		if err != nil {
			logger.LogError(c.Request.Context(), common.LogText("Failed to calculate Stripe top-up service fee user_id=%d error=%q", id, err.Error()))
			paymentError(c, common.NewMessage("Failed to calculate payment amount"))
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"message": "success", "data": total.StringFixed(stripeChargeScale(currency)),
			"service_fee": fee.String(), "currency": strings.ToUpper(currency),
		})
		return
	}
	payMoney := getStripePayMoney(float64(req.Amount), ratio)
	if payMoney <= 0.01 {
		paymentError(c, common.NewMessage("Payment amount is too low"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": strconv.FormatFloat(payMoney, 'f', 2, 64)})
}

func (*StripeAdaptor) RequestPay(c *gin.Context, req *StripePayRequest) {
	if req.PaymentMethod != model.PaymentMethodStripe {
		paymentError(c, common.NewMessage("Unsupported payment channel"))
		return
	}
	if req.Amount < getStripeMinTopup() {
		common.ApiErrorStatus(c, http.StatusOK, common.NewMessage("Top-up amount cannot be less than {{min}}", map[string]any{"min": getStripeMinTopup()}), gin.H{"data": 10})
		return
	}
	if req.Amount > 10000 {
		common.ApiErrorStatus(c, http.StatusOK, common.NewMessage("Top-up amount cannot exceed {{max}}", map[string]any{"max": 10000}), gin.H{"data": 10})
		return
	}

	if req.SuccessURL != "" && common.ValidateRedirectURL(req.SuccessURL) != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Payment success redirect URL is not in the trusted domain list"), gin.H{"data": ""})
		return
	}

	if req.CancelURL != "" && common.ValidateRedirectURL(req.CancelURL) != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Payment cancel redirect URL is not in the trusted domain list"), gin.H{"data": ""})
		return
	}

	id := c.GetInt("id")
	user, err := model.GetUserById(id, false)
	if err != nil || user == nil {
		paymentError(c, common.NewMessage("User does not exist"))
		return
	}
	ratio := common.GetUserTopupGroupRatio(user.Group, user.VIPLevel)
	walletAmount := stripeWalletAmount(req.Amount, ratio, user.VIPLevel)
	if rejectInvalidCreditedQuota(c, id, getStripeCreditedQuota(walletAmount)) {
		return
	}

	reference := fmt.Sprintf("new-api-ref-%d-%d-%s", user.Id, time.Now().UnixMilli(), randstr.String(4))
	referenceId := "ref_" + common.Sha1([]byte(reference))

	checkoutRatio := ratio
	if user.VIPLevel == nil {
		checkoutRatio = 1
	}
	fee, err := stripeSmallTopUpFee(req.Amount)
	if err != nil {
		logger.LogError(c.Request.Context(), common.LogText("Failed to calculate Stripe top-up service fee user_id=%d error=%q", id, err.Error()))
		paymentError(c, common.NewMessage("Failed to start payment"))
		return
	}
	checkout, err := genStripeLink(c.Request.Context(), referenceId, user.StripeCustomer, user.Email, req.Amount, checkoutRatio, fee, req.SuccessURL, req.CancelURL)
	if err != nil {
		logger.LogError(c.Request.Context(), common.LogText("Stripe failed to create Checkout Session user_id=%d trade_no=%s amount=%d error=%q", id, referenceId, req.Amount, err.Error()))
		paymentError(c, common.NewMessage("Failed to start payment"))
		return
	}

	topUp := &model.TopUp{
		UserId:          id,
		Amount:          req.Amount,
		Money:           walletAmount.InexactFloat64(), // Stripe orders store the credited amount in Money.
		TradeNo:         referenceId,
		PaymentMethod:   model.PaymentMethodStripe,
		PaymentProvider: model.PaymentProviderStripe,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
	}
	err = topUp.Insert()
	if err != nil {
		logger.LogError(c.Request.Context(), common.LogText("Stripe failed to create top-up order user_id=%d trade_no=%s amount=%d error=%q", id, referenceId, req.Amount, err.Error()))
		paymentError(c, common.NewMessage("Failed to create order"))
		return
	}
	total := decimal.NewFromInt(checkout.AmountSubtotal).Shift(-stripeChargeScale(string(checkout.Currency)))
	logger.LogInfo(c.Request.Context(), common.LogText("Stripe top-up order created user_id=%d trade_no=%s amount=%d money=%.2f", id, referenceId, req.Amount, total.InexactFloat64()))
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"pay_link": checkout.URL,
		},
	})
}

func RequestStripeAmount(c *gin.Context) {
	var req StripePayRequest
	err := c.ShouldBindJSON(&req)
	if err != nil {
		paymentError(c, common.NewMessage("Invalid parameters"))
		return
	}
	stripeAdaptor.RequestAmount(c, &req)
}

func RequestStripePay(c *gin.Context) {
	var req StripePayRequest
	err := c.ShouldBindJSON(&req)
	if err != nil {
		paymentError(c, common.NewMessage("Invalid parameters"))
		return
	}
	stripeAdaptor.RequestPay(c, &req)
}

func StripeWebhook(c *gin.Context) {
	ctx := c.Request.Context()
	if !isStripeWebhookEnabled() {
		logger.LogWarn(ctx, common.LogText("Stripe webhook rejected reason=webhook_disabled path=%q client_ip=%s", c.Request.RequestURI, c.ClientIP()))
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.LogError(ctx, common.LogText("Stripe webhook failed to read request body path=%q client_ip=%s error=%q", c.Request.RequestURI, c.ClientIP(), err.Error()))
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}

	signature := c.GetHeader("Stripe-Signature")
	logger.LogInfo(ctx, common.LogText("Stripe webhook received request path=%q client_ip=%s signature=%q body=%q", c.Request.RequestURI, c.ClientIP(), signature, string(payload)))
	event, err := webhook.ConstructEventWithOptions(payload, signature, setting.StripeWebhookSecret, webhook.ConstructEventOptions{
		IgnoreAPIVersionMismatch: true,
	})

	if err != nil {
		logger.LogWarn(ctx, common.LogText("Stripe webhook signature verification failed path=%q client_ip=%s error=%q", c.Request.RequestURI, c.ClientIP(), err.Error()))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	callerIp := c.ClientIP()
	logger.LogInfo(ctx, common.LogText("Stripe webhook signature verified event_type=%s client_ip=%s path=%q", string(event.Type), callerIp, c.Request.RequestURI))
	switch event.Type {
	case stripe.EventTypeCheckoutSessionCompleted:
		sessionCompleted(ctx, event, callerIp)
	case stripe.EventTypeCheckoutSessionExpired:
		sessionExpired(ctx, event)
	case stripe.EventTypeCheckoutSessionAsyncPaymentSucceeded:
		sessionAsyncPaymentSucceeded(ctx, event, callerIp)
	case stripe.EventTypeCheckoutSessionAsyncPaymentFailed:
		sessionAsyncPaymentFailed(ctx, event, callerIp)
	default:
		logger.LogInfo(ctx, common.LogText("Stripe webhook ignored event event_type=%s client_ip=%s", string(event.Type), callerIp))
	}

	c.Status(http.StatusOK)
}

func sessionCompleted(ctx context.Context, event stripe.Event, callerIp string) {
	customerId := event.GetObjectValue("customer")
	referenceId := event.GetObjectValue("client_reference_id")
	status := event.GetObjectValue("status")
	if "complete" != status {
		logger.LogWarn(ctx, common.LogText("Stripe checkout.completed has unexpected status, ignored trade_no=%s status=%s client_ip=%s", referenceId, status, callerIp))
		return
	}

	paymentStatus := event.GetObjectValue("payment_status")
	if paymentStatus != "paid" {
		logger.LogInfo(ctx, common.LogText("Stripe Checkout payment not completed, waiting for async result trade_no=%s payment_status=%s client_ip=%s", referenceId, paymentStatus, callerIp))
		return
	}

	fulfillOrder(ctx, event, referenceId, customerId, callerIp)
}

// sessionAsyncPaymentSucceeded handles delayed payment methods (bank transfer, SEPA, etc.)
// that confirm payment after the checkout session completes.
func sessionAsyncPaymentSucceeded(ctx context.Context, event stripe.Event, callerIp string) {
	customerId := event.GetObjectValue("customer")
	referenceId := event.GetObjectValue("client_reference_id")
	logger.LogInfo(ctx, common.LogText("Stripe async payment succeeded trade_no=%s client_ip=%s", referenceId, callerIp))

	fulfillOrder(ctx, event, referenceId, customerId, callerIp)
}

// sessionAsyncPaymentFailed marks orders as failed when delayed payment methods
// ultimately fail (e.g. bank transfer not received, SEPA rejected).
func sessionAsyncPaymentFailed(ctx context.Context, event stripe.Event, callerIp string) {
	referenceId := event.GetObjectValue("client_reference_id")
	logger.LogWarn(ctx, common.LogText("Stripe async payment failed trade_no=%s client_ip=%s", referenceId, callerIp))

	if len(referenceId) == 0 {
		logger.LogWarn(ctx, common.LogText("Stripe async payment failed event missing order number client_ip=%s", callerIp))
		return
	}

	LockOrder(referenceId)
	defer UnlockOrder(referenceId)

	topUp := model.GetTopUpByTradeNo(referenceId)
	if topUp == nil {
		logger.LogWarn(ctx, common.LogText("Stripe async payment failed but local order not found trade_no=%s client_ip=%s", referenceId, callerIp))
		return
	}

	if topUp.PaymentProvider != model.PaymentProviderStripe {
		logger.LogWarn(ctx, common.LogText("Stripe async payment failed but order payment gateway mismatch trade_no=%s payment_provider=%s client_ip=%s", referenceId, topUp.PaymentProvider, callerIp))
		return
	}

	if topUp.Status != common.TopUpStatusPending {
		logger.LogInfo(ctx, common.LogText("Stripe async payment failed but order not pending, ignored trade_no=%s status=%s client_ip=%s", referenceId, topUp.Status, callerIp))
		return
	}

	topUp.Status = common.TopUpStatusFailed
	if err := topUp.Update(); err != nil {
		logger.LogError(ctx, common.LogText("Stripe failed to mark top-up order as failed trade_no=%s client_ip=%s error=%q", referenceId, callerIp, err.Error()))
		return
	}
	logger.LogInfo(ctx, common.LogText("Stripe top-up order marked as failed trade_no=%s client_ip=%s", referenceId, callerIp))
}

// fulfillOrder is the shared logic for crediting quota after payment is confirmed.
func fulfillOrder(ctx context.Context, event stripe.Event, referenceId string, customerId string, callerIp string) {
	if len(referenceId) == 0 {
		logger.LogWarn(ctx, common.LogText("Stripe order completion missing order number client_ip=%s", callerIp))
		return
	}

	LockOrder(referenceId)
	defer UnlockOrder(referenceId)
	payload := map[string]any{
		"customer":     customerId,
		"amount_total": event.GetObjectValue("amount_total"),
		"currency":     strings.ToUpper(event.GetObjectValue("currency")),
		"event_type":   string(event.Type),
	}
	if err := model.CompleteSubscriptionOrder(referenceId, common.GetJsonString(payload), model.PaymentProviderStripe, ""); err == nil {
		logger.LogInfo(ctx, common.LogText("Stripe subscription order processed trade_no=%s event_type=%s client_ip=%s", referenceId, string(event.Type), callerIp))
		return
	} else if err != nil && !errors.Is(err, model.ErrSubscriptionOrderNotFound) {
		logger.LogError(ctx, common.LogText("Stripe subscription order processing failed trade_no=%s event_type=%s client_ip=%s error=%q", referenceId, string(event.Type), callerIp, err.Error()))
		return
	}

	payment := stripeWalletPayment(event.Data.Raw)
	err := model.Recharge(referenceId, customerId, callerIp, payment)
	if err != nil {
		logger.LogError(ctx, common.LogText("Stripe top-up processing failed trade_no=%s event_type=%s client_ip=%s error=%q", referenceId, string(event.Type), callerIp, err.Error()))
		return
	}

	logger.LogInfo(ctx, common.LogText("Stripe top-up succeeded trade_no=%s amount_total=%.2f currency=%s event_type=%s client_ip=%s", referenceId, payment.Amount.InexactFloat64(), payment.Currency, string(event.Type), callerIp))
}

func stripeWalletPayment(data []byte) model.WalletTopUpPayment {
	var checkout struct {
		Amount      int64  `json:"amount_total"`
		Currency    string `json:"currency"`
		Presentment *struct {
			Amount   int64  `json:"presentment_amount"`
			Currency string `json:"presentment_currency"`
		} `json:"presentment_details"`
	}
	if err := common.Unmarshal(data, &checkout); err != nil {
		return model.WalletTopUpPayment{}
	}
	amount, currency := checkout.Amount, strings.ToUpper(checkout.Currency)
	if checkout.Presentment != nil && checkout.Presentment.Currency != "" {
		amount, currency = checkout.Presentment.Amount, strings.ToUpper(checkout.Presentment.Currency)
	}
	return model.ParseWalletTopUpPayment(decimal.NewFromInt(amount).Shift(-stripeChargeScale(currency)).String(), currency)
}

func stripeChargeScale(currency string) int32 {
	// These currencies have zero-decimal Stripe charge amounts. ISK and
	// UGX still use two decimals for backward compatibility.
	switch strings.ToUpper(currency) {
	case "BIF", "CLP", "DJF", "GNF", "JPY", "KMF", "KRW", "MGA", "PYG", "RWF", "VND", "VUV", "XAF", "XOF", "XPF":
		return 0
	}
	return 2
}

func sessionExpired(ctx context.Context, event stripe.Event) {
	referenceId := event.GetObjectValue("client_reference_id")
	status := event.GetObjectValue("status")
	if "expired" != status {
		logger.LogWarn(ctx, common.LogText("Stripe checkout.expired has unexpected status, ignored trade_no=%s status=%s", referenceId, status))
		return
	}

	if len(referenceId) == 0 {
		logger.LogWarn(ctx, common.LogText("Stripe checkout.expired missing order number"))
		return
	}

	// Subscription order expiration
	LockOrder(referenceId)
	defer UnlockOrder(referenceId)
	if err := model.ExpireSubscriptionOrder(referenceId, model.PaymentProviderStripe); err == nil {
		logger.LogInfo(ctx, common.LogText("Stripe subscription order expired trade_no=%s", referenceId))
		return
	} else if err != nil && !errors.Is(err, model.ErrSubscriptionOrderNotFound) {
		logger.LogError(ctx, common.LogText("Stripe subscription order expiry processing failed trade_no=%s error=%q", referenceId, err.Error()))
		return
	}

	err := model.UpdatePendingTopUpStatus(referenceId, model.PaymentProviderStripe, common.TopUpStatusExpired)
	if errors.Is(err, model.ErrTopUpNotFound) {
		logger.LogWarn(ctx, common.LogText("Stripe top-up order not found, cannot mark as expired trade_no=%s", referenceId))
		return
	}
	if err != nil {
		logger.LogError(ctx, common.LogText("Stripe top-up order expiry processing failed trade_no=%s error=%q", referenceId, err.Error()))
		return
	}

	logger.LogInfo(ctx, common.LogText("Stripe top-up order expired trade_no=%s", referenceId))
}

func genStripeLink(ctx context.Context, referenceId string, customerId string, email string, amount int64, ratio float64, fee decimal.Decimal, successURL string, cancelURL string) (*stripe.CheckoutSession, error) {
	if !strings.HasPrefix(setting.StripeApiSecret, "sk_") && !strings.HasPrefix(setting.StripeApiSecret, "rk_") {
		return nil, errors.New("invalid Stripe API key")
	}

	if successURL == "" {
		successURL = paymentReturnPath("/usage-logs")
	}
	if cancelURL == "" {
		cancelURL = paymentReturnPath("/wallet")
	}

	items, _, _, err := stripeTopUpCheckoutItems(ctx, amount, ratio, fee)
	if err != nil {
		return nil, err
	}
	params := &stripe.CheckoutSessionParams{
		ClientReferenceID:   stripe.String(referenceId),
		SuccessURL:          stripe.String(successURL),
		CancelURL:           stripe.String(cancelURL),
		LineItems:           items,
		Mode:                stripe.String(string(stripe.CheckoutSessionModePayment)),
		AllowPromotionCodes: stripe.Bool(setting.StripePromotionCodesEnabled),
	}
	params.Context = ctx

	if customerId == "" {
		if email != "" {
			params.CustomerEmail = stripe.String(email)
		}
		params.CustomerCreation = stripe.String(string(stripe.CheckoutSessionCustomerCreationAlways))
	} else {
		params.Customer = stripe.String(customerId)
	}

	client := session.Client{B: stripe.GetBackend(stripe.APIBackend), Key: setting.StripeApiSecret}
	return client.New(params)
}

func stripeSmallTopUpFee(amount int64) (decimal.Decimal, error) {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[setting.StripeSmallTopUpFeeOption]
	exchangeRate := operation_setting.USDExchangeRate
	displayType := operation_setting.GetQuotaDisplayType()
	quotaPerUnit := common.QuotaPerUnit
	common.OptionMapRWMutex.RUnlock()
	if raw == "" {
		raw = setting.DefaultStripeSmallTopUpFeeOption
	}
	settings, err := setting.ParseStripeTopUpFeeSettings(raw)
	if err != nil || settings.Amount.IsZero() {
		return decimal.Zero, err
	}
	if exchangeRate <= 0 || math.IsNaN(exchangeRate) || math.IsInf(exchangeRate, 0) {
		return decimal.Zero, errors.New("invalid USD to CNY exchange rate")
	}
	principal := decimal.NewFromInt(amount).Mul(decimal.NewFromFloat(exchangeRate))
	waiver := settings.WaiverCNY
	if displayType == operation_setting.QuotaDisplayTypeTokens {
		if quotaPerUnit <= 0 || math.IsNaN(quotaPerUnit) || math.IsInf(quotaPerUnit, 0) {
			return decimal.Zero, errors.New("invalid quota per unit")
		}
		waiver = waiver.Mul(decimal.NewFromFloat(quotaPerUnit))
	}
	if principal.GreaterThan(waiver) {
		return decimal.Zero, nil
	}
	return settings.Amount, nil
}

func stripeTopUpCheckoutItems(ctx context.Context, quantity int64, ratio float64, fee decimal.Decimal) ([]*stripe.CheckoutSessionLineItemParams, decimal.Decimal, string, error) {
	priceID := setting.StripePriceId
	items := []*stripe.CheckoutSessionLineItemParams{{
		Price:    stripe.String(priceID),
		Quantity: stripe.Int64(quantity),
	}}
	if ratio == 1 && fee.IsZero() {
		return items, decimal.Zero, "", nil
	}
	client := price.Client{B: stripe.GetBackend(stripe.APIBackend), Key: setting.StripeApiSecret}
	params := &stripe.PriceParams{}
	params.Context = ctx
	base, err := client.Get(priceID, params)
	if err != nil {
		return nil, decimal.Zero, "", err
	}
	if ratio != 1 {
		items[0], err = stripeMembershipLineItem(base, quantity, ratio)
		if err != nil {
			return nil, decimal.Zero, "", err
		}
	}
	if fee.IsZero() {
		return items, decimal.Zero, "", nil
	}
	if base.Currency == "" || base.Recurring != nil || base.TransformQuantity != nil || base.BillingScheme == stripe.PriceBillingSchemeTiered {
		return nil, decimal.Zero, "", errors.New("Stripe top-up service fee requires a fixed one-time price")
	}
	currency := string(base.Currency)
	scale := stripeChargeScale(currency)
	feeMinor := fee.Shift(scale)
	if !feeMinor.IsInteger() || (currency == "isk" || currency == "ugx") && !fee.IsInteger() {
		return nil, decimal.Zero, "", errors.New("invalid Stripe top-up service fee precision for " + currency)
	}
	items = append(items, &stripe.CheckoutSessionLineItemParams{
		Quantity: stripe.Int64(1),
		PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
			Currency:   stripe.String(currency),
			UnitAmount: stripe.Int64(feeMinor.IntPart()),
			ProductData: &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
				Name: stripe.String("Small top-up service fee"),
			},
		},
	})
	subtotal := stripePriceUnitAmount(base).Mul(decimal.NewFromInt(quantity)).Round(0)
	if items[0].PriceData != nil {
		subtotal = decimal.NewFromInt(*items[0].PriceData.UnitAmount)
	}
	return items, subtotal.Add(feeMinor).Shift(-scale), currency, nil
}

func stripePriceUnitAmount(base *stripe.Price) decimal.Decimal {
	if base.UnitAmountDecimal > 0 {
		return decimal.NewFromFloat(base.UnitAmountDecimal)
	}
	return decimal.NewFromInt(base.UnitAmount)
}

// Use the configured Stripe price's currency, product and tax behavior.
func stripeMembershipLineItem(base *stripe.Price, quantity int64, ratio float64) (*stripe.CheckoutSessionLineItemParams, error) {
	if base.Product == nil || base.Currency == "" || base.Recurring != nil || base.TransformQuantity != nil {
		return nil, errors.New("Stripe VIP pricing requires a fixed one-time price")
	}
	total := stripePriceUnitAmount(base).Mul(decimal.NewFromInt(quantity)).Mul(decimal.NewFromFloat(ratio)).Round(0)
	if !total.IsPositive() || total.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
		return nil, errors.New("invalid Stripe VIP checkout amount")
	}
	data := &stripe.CheckoutSessionLineItemPriceDataParams{
		Currency:   stripe.String(string(base.Currency)),
		Product:    stripe.String(base.Product.ID),
		UnitAmount: stripe.Int64(total.IntPart()),
	}
	if base.TaxBehavior != "" {
		data.TaxBehavior = stripe.String(string(base.TaxBehavior))
	}
	return &stripe.CheckoutSessionLineItemParams{
		Quantity:  stripe.Int64(1),
		PriceData: data,
	}, nil
}

func stripeWalletAmount(amount int64, ratio float64, level *int) decimal.Decimal {
	units := decimal.NewFromInt(amount)
	if level == nil {
		// Custom groups retain their legacy wallet multipliers.
		units = units.Mul(decimal.NewFromFloat(ratio))
	}
	return units
}

func getStripeCreditedQuota(walletAmount decimal.Decimal) decimal.Decimal {
	return walletAmount.Mul(decimal.NewFromFloat(common.QuotaPerUnit))
}

func getStripePayMoney(amount float64, topupGroupRatio float64) float64 {
	originalAmount := amount
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		amount = amount / common.QuotaPerUnit
	}
	// Using float64 for monetary calculations is acceptable here due to the small amounts involved
	if topupGroupRatio == 0 {
		topupGroupRatio = 1
	}
	// apply optional preset discount by the original request amount (if configured), default 1.0
	discount := 1.0
	if ds, ok := operation_setting.GetPaymentSetting().AmountDiscount[int(originalAmount)]; ok {
		if ds > 0 {
			discount = ds
		}
	}
	payMoney := amount * setting.StripeUnitPrice * topupGroupRatio * discount
	return payMoney
}

func getStripeMinTopup() int64 {
	minTopup := setting.StripeMinTopUp
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		minTopup = minTopup * int(common.QuotaPerUnit)
	}
	return int64(minTopup)
}

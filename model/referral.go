package model

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/shopspring/decimal"
	"golang.org/x/text/currency"
	"gorm.io/gorm"
)

const (
	ReferralDefaultRateOption   = "ReferralDefaultRebateRate"
	ReferralExchangeRatesOption = "ReferralRebateExchangeRates"
	ReferralRebateDelayOption   = "ReferralRebateDelayHours"
	InvitationRewardDelayOption = "InvitationRewardDelayHours"
)

var (
	ErrInvalidReferralRate          = common.NewMessage("Rebate rate must be between 0% and 100%, with up to two decimal places")
	ErrInvalidReferralExchangeRates = common.NewMessage("Enter a JSON object of currency codes and positive exchange rates per US dollar")
	ErrInvalidReferralDelay         = common.NewMessage("Enter a non-negative whole number of hours")
	walletPaymentAmountPattern      = regexp.MustCompile(`^[0-9]{1,14}(\.[0-9]{1,6})?$`)
)

type ReferralRebate struct {
	Id                  int    `json:"id"`
	TopUpId             *int   `json:"-" gorm:"uniqueIndex"`
	RedemptionId        *int   `json:"-" gorm:"uniqueIndex"`
	RedemptionQuota     int    `json:"redemption_quota,omitempty"`
	InviterId           int    `json:"-" gorm:"index"`
	InviteeUsername     string `json:"username"`
	PaidAmount          string `json:"paid_amount" gorm:"type:varchar(24)"`
	PaidCurrency        string `json:"paid_currency" gorm:"type:varchar(3)"`
	Rate                int    `json:"rate_bps"`
	Quota               int    `json:"rebate_quota"`
	CreatedAt           int64  `json:"created_at"`
	AvailableAt         int64  `json:"available_at" gorm:"not null;default:0;index"`
	CreditedAt          int64  `json:"credited_at"`
	ExchangeRatePending bool   `json:"exchange_rate_pending,omitempty" gorm:"index"`
	LogPending          bool   `json:"-" gorm:"index"`
}

type InvitationReward struct {
	Id          int
	InviteeId   *int `gorm:"uniqueIndex"` // Nil for rewards already counted on a pre-upgrade wallet.
	InviterId   int  `gorm:"index"`
	Quota       int  `gorm:"type:bigint"`
	CreatedAt   int64
	AvailableAt int64 `gorm:"index:idx_invitation_reward_credit,priority:2"`
	CreditedAt  int64 `gorm:"not null;default:0;index:idx_invitation_reward_credit,priority:1"`
	LogPending  bool  `gorm:"index"`
}

type ReferralRebateRecord struct {
	ReferralRebate
	RecordType string `json:"record_type"`
}

type ReferralInvitee struct {
	Id       int    `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

// WalletTopUpPayment carries the verified amount in the payment currency.
type WalletTopUpPayment struct {
	Amount   decimal.Decimal
	Currency string
}

func ParseWalletTopUpPayment(amount, code string) WalletTopUpPayment {
	code = strings.ToUpper(strings.TrimSpace(code))
	if !walletPaymentAmountPattern.MatchString(amount) || len(code) != 3 {
		return WalletTopUpPayment{}
	}
	if _, err := currency.ParseISO(code); err != nil {
		return WalletTopUpPayment{}
	}
	value, err := decimal.NewFromString(amount)
	if err != nil || !value.IsPositive() {
		return WalletTopUpPayment{}
	}
	return WalletTopUpPayment{Amount: value, Currency: code}
}

func WalletTopUpPaymentFromMinorUnits(amount int64, code string) WalletTopUpPayment {
	unit, err := currency.ParseISO(strings.ToUpper(code))
	if err != nil {
		return WalletTopUpPayment{}
	}
	scale, _ := currency.Standard.Rounding(unit)
	return ParseWalletTopUpPayment(decimal.NewFromInt(amount).Shift(-int32(scale)).String(), code)
}

func ParseReferralRate(value string) (int, error) {
	rate, err := strconv.Atoi(value)
	if err != nil || rate < 0 || rate > 10_000 {
		return 0, ErrInvalidReferralRate
	}
	return rate, nil
}

func parseReferralDelay(value string) (int64, error) {
	hours, err := strconv.ParseUint(value, 10, 31)
	if err != nil {
		return 0, ErrInvalidReferralDelay
	}
	return int64(hours) * 3600, nil
}

func parseReferralExchangeRates(value string) (map[string]decimal.Decimal, error) {
	var raw map[string]float64
	if err := common.UnmarshalJsonStr(value, &raw); err != nil || raw == nil {
		return nil, ErrInvalidReferralExchangeRates
	}
	rates := make(map[string]decimal.Decimal, len(raw))
	for code, rate := range raw {
		if _, err := currency.ParseISO(code); err != nil || len(code) != 3 ||
			code != strings.ToUpper(code) || code == "USD" || rate < 0.000001 || rate > 1_000_000_000 {
			return nil, ErrInvalidReferralExchangeRates
		}
		rates[code] = decimal.NewFromFloat(rate)
	}
	return rates, nil
}

func ReferralDefaultRate() int {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	rate, _ := ParseReferralRate(common.OptionMap[ReferralDefaultRateOption])
	return rate
}

func (user *User) EffectiveReferralRebateRate() int {
	if rate := user.ReferralRebateRate; rate != nil {
		if *rate < 0 || *rate > 10_000 {
			return 0
		}
		return *rate
	}
	return ReferralDefaultRate()
}

func ListReferralInvitees(ctx context.Context, inviterId int, page *common.PageInfo) ([]ReferralInvitee, int64, error) {
	items := make([]ReferralInvitee, 0)
	query := DB.WithContext(ctx).Model(&User{}).Where("inviter_id = ?", inviterId)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := query.Select("id", "username", "email").Order("id DESC").Offset(page.GetStartIdx()).
		Limit(page.GetPageSize()).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	for i := range items {
		email := items[i].Email
		local, domain, valid := strings.Cut(email, "@")
		if !valid || local == "" || domain == "" || strings.Contains(domain, "@") {
			items[i].Email = ""
			continue
		}
		characters := []rune(local)
		if len(characters) <= 2 {
			items[i].Email = "***@" + domain
			continue
		}
		items[i].Email = string(characters[0]) + "***" + string(characters[len(characters)-1]) + "@" + domain
	}
	return items, total, nil
}

func ListReferralRebates(ctx context.Context, inviterId int, page *common.PageInfo, rebatesEnabled, invitationsEnabled bool) ([]ReferralRebateRecord, int64, error) {
	items := make([]ReferralRebateRecord, 0)
	db := DB.WithContext(ctx)
	rebates := db.Model(&ReferralRebate{}).Where("inviter_id = ?", inviterId).
		Select(`'topup_rebate' AS record_type, id, top_up_id, redemption_id, redemption_quota,
			invitee_username, paid_amount, paid_currency, rate, quota, created_at, available_at,
			credited_at, exchange_rate_pending`)
	rewards := db.Model(&InvitationReward{}).Where("invitation_rewards.inviter_id = ?", inviterId).
		Joins("LEFT JOIN users AS invitee ON invitee.id = invitation_rewards.invitee_id AND invitee.deleted_at IS NULL").
		Select(`'invitation_reward' AS record_type, invitation_rewards.id, NULL AS top_up_id,
			NULL AS redemption_id, 0 AS redemption_quota, COALESCE(invitee.username, '') AS invitee_username,
			'0' AS paid_amount, 'USD' AS paid_currency, 0 AS rate, invitation_rewards.quota,
			COALESCE(NULLIF(invitation_rewards.created_at, 0), NULLIF(invitee.created_at, 0), invitation_rewards.available_at) AS created_at,
			invitation_rewards.available_at, invitation_rewards.credited_at, FALSE AS exchange_rate_pending`)
	if !rebatesEnabled {
		rebates = rebates.Where("1 = 0")
	}
	if !invitationsEnabled {
		rewards = rewards.Where("1 = 0")
	}
	query := db.Table("(? UNION ALL ?) AS referral_records", rebates, rewards)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := query.Order("created_at DESC, id DESC, record_type").Offset(page.GetStartIdx()).Limit(page.GetPageSize()).Find(&items).Error
	return items, total, err
}

func creditPaidTopUp(tx *gorm.DB, topUp *TopUp, quota int, updates map[string]any, payment []WalletTopUpPayment, rebate *ReferralRebate) error {
	if err := creditTopUpQuota(tx, topUp.UserId, quota, updates, growthTopup, topUp.TradeNo, topUp.CompleteTime); err != nil {
		return err
	}
	if len(payment) == 0 || !payment[0].Amount.IsPositive() {
		return nil
	}
	*rebate = ReferralRebate{
		TopUpId: &topUp.Id, CreatedAt: topUp.CompleteTime,
		PaidAmount: payment[0].Amount.String(), PaidCurrency: payment[0].Currency,
	}
	return settleReferralRebate(tx, topUp.UserId, payment[0].Amount, rebate)
}

func settleReferralRebate(tx *gorm.DB, inviteeId int, paidAmount decimal.Decimal, rebate *ReferralRebate) error {
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return nil
	}
	var invitee User
	if err := tx.Select("id", "username", "inviter_id").First(&invitee, inviteeId).Error; err != nil {
		return err
	}
	if invitee.InviterId == 0 || invitee.InviterId == invitee.Id {
		return nil
	}
	var inviter User
	err := lockForUpdate(tx).Select("id", "username", "quota", "referral_rebate_enabled", "referral_rebate_rate").
		First(&inviter, invitee.InviterId).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && !inviter.ReferralRebateEnabled) {
		return nil
	}
	if err != nil {
		return err
	}

	rate := inviter.EffectiveReferralRebateRate()
	common.OptionMapRWMutex.RLock()
	delay, _ := parseReferralDelay(common.OptionMap[ReferralRebateDelayOption])
	common.OptionMapRWMutex.RUnlock()
	rebate.InviterId = inviter.Id
	rebate.InviteeUsername = invitee.Username
	rebate.Rate = rate
	rebate.AvailableAt = rebate.CreatedAt + delay
	if rebate.RedemptionId != nil {
		quota := decimal.NewFromInt(int64(rebate.RedemptionQuota)).Mul(decimal.NewFromInt(int64(rate))).Shift(-4)
		rebate.Quota, err = common.WalletQuotaFromDecimalStrict(quota)
		if err != nil {
			return err
		}
	} else if rate > 0 {
		unitsPerDollar := referralExchangeRates()[rebate.PaidCurrency]
		if unitsPerDollar.IsPositive() {
			rebate.Quota, err = rebate.calculateQuota(paidAmount, unitsPerDollar)
			if err != nil {
				common.SysError(fmt.Sprintf("referral rebate unavailable for top-up %d: %v", *rebate.TopUpId, err))
			}
		} else {
			rebate.ExchangeRatePending = true
		}
	}

	if err := tx.Create(rebate).Error; err != nil {
		return err
	}
	if delay == 0 && rebate.Quota > 0 && inviter.Quota <= common.MaxWalletQuota-rebate.Quota {
		return rebate.creditWallet(tx)
	}
	return nil
}

func referralExchangeRates() map[string]decimal.Decimal {
	common.OptionMapRWMutex.RLock()
	configured := common.OptionMap[ReferralExchangeRatesOption]
	cny := operation_setting.USDExchangeRate
	common.OptionMapRWMutex.RUnlock()

	rates, err := parseReferralExchangeRates(configured)
	if err != nil {
		rates = make(map[string]decimal.Decimal)
	}
	rates["USD"] = decimal.NewFromInt(1)
	if !rates["CNY"].IsPositive() && cny >= 0.000001 && cny <= 1_000_000_000 {
		rates["CNY"] = decimal.NewFromFloat(cny)
	}
	return rates
}

func (rebate *ReferralRebate) calculateQuota(amount, unitsPerDollar decimal.Decimal) (int, error) {
	quota := amount.Mul(decimal.NewFromInt(int64(rebate.Rate)).Shift(-4)).
		Mul(decimal.NewFromFloat(common.QuotaPerUnit))
	return common.WalletQuotaFromDecimalStrict(quota.Div(unitsPerDollar))
}

// Call with the inviter row locked, or after selecting an eligible rebate for retry.
func (rebate *ReferralRebate) creditWallet(tx *gorm.DB) error {
	result := tx.Model(&User{}).Where("id = ? AND quota <= ?", rebate.InviterId, common.MaxWalletQuota-rebate.Quota).
		UpdateColumn("quota", gorm.Expr("quota + ?", rebate.Quota))
	if result.Error != nil || result.RowsAffected == 0 {
		return result.Error
	}
	rebate.CreditedAt = common.GetTimestamp()
	rebate.LogPending = true
	return tx.Model(rebate).Updates(map[string]any{
		"credited_at": rebate.CreditedAt,
		"log_pending": true,
	}).Error
}

func (rebate *ReferralRebate) afterCredit() {
	if rebate.CreditedAt == 0 {
		return
	}
	syncCreditUserQuotaCache(rebate.InviterId, rebate.Quota, "referral rebate")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := publishReferralRebateLog(ctx, rebate.Id); err != nil {
		common.SysError(fmt.Sprintf("failed to record referral rebate log for rebate %d: %v", rebate.Id, err))
	}
}

func publishReferralRebateLog(ctx context.Context, id int) error {
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&ReferralRebate{}).Where("id = ? AND log_pending = ?", id, true).
			UpdateColumn("log_pending", false)
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		var rebate ReferralRebate
		if err := tx.First(&rebate, id).Error; err != nil {
			return err
		}
		other := NewLogOther()
		other.SetPublic("referral_rebate_quota", rebate.Quota)
		content := common.NewMessage("Referral rebate / Invited user {{username}} / {{quota}}", map[string]any{
			"username": rebate.InviteeUsername,
			"quota":    logger.FormatQuota(rebate.Quota),
		})
		return insertReferralCreditLog(tx, Log{
			UserId: rebate.InviterId, CreatedAt: rebate.CreditedAt, Type: LogTypeTopup,
			RequestId: fmt.Sprintf("referral-rebate-%d", rebate.Id),
			Content:   other.setContent([]*common.Message{content}), Other: other.JSONString(),
		})
	})
}

// The caller clears the pending flag in its transaction to serialize delivery.
// If the ledger transaction retries after a separate log store commits, reuse that log.
func insertReferralCreditLog(tx *gorm.DB, log Log) error {
	store := LOG_DB.WithContext(tx.Statement.Context)
	if DB == LOG_DB {
		store = tx
	}
	var count int64
	if err := store.Model(&Log{}).Where("request_id = ?", log.RequestId).Count(&count).Error; err != nil || count > 0 {
		return err
	}
	var inviter User
	err := tx.Unscoped().Select("username").First(&inviter, log.UserId).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	log.Username = inviter.Username
	return store.Create(&log).Error
}

// ReconcileReferralRebates credits due rebates and delivers their pending logs.
func ReconcileReferralRebates(ctx context.Context) error {
	rates := referralExchangeRates()
	var unconverted []ReferralRebate
	if err := DB.WithContext(ctx).Where("exchange_rate_pending = ? AND paid_currency IN ?", true, slices.Collect(maps.Keys(rates))).
		Order("id").Limit(200).Find(&unconverted).Error; err != nil {
		return err
	}
	var result error
	for _, rebate := range unconverted {
		amount, err := decimal.NewFromString(rebate.PaidAmount)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		quota, err := rebate.calculateQuota(amount, rates[rebate.PaidCurrency])
		if err != nil {
			common.SysError(fmt.Sprintf("referral rebate %d unavailable: %v", rebate.Id, err))
		}
		err = DB.WithContext(ctx).Model(&rebate).Where("exchange_rate_pending = ?", true).
			Updates(map[string]any{"quota": quota, "exchange_rate_pending": false}).Error
		result = errors.Join(result, err)
	}
	var pending []ReferralRebate
	err := DB.WithContext(ctx).Model(&ReferralRebate{}).
		Joins("JOIN users ON users.id = referral_rebates.inviter_id AND users.deleted_at IS NULL").
		Where("referral_rebates.credited_at = 0 AND referral_rebates.available_at <= ? AND referral_rebates.quota > 0 AND users.quota <= ? - referral_rebates.quota",
			common.GetTimestamp(), common.MaxWalletQuota).
		Order("referral_rebates.id").Limit(200).Find(&pending).Error
	if err != nil {
		return errors.Join(result, err)
	}
	for _, row := range pending {
		var rebate ReferralRebate
		credited := false
		err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := lockForUpdate(tx).First(&rebate, row.Id).Error; err != nil {
				return err
			}
			if rebate.CreditedAt != 0 {
				return nil
			}
			if err := rebate.creditWallet(tx); err != nil {
				return err
			}
			credited = rebate.CreditedAt != 0
			return nil
		})
		if err != nil {
			result = errors.Join(result, err)
		} else if credited {
			syncCreditUserQuotaCache(rebate.InviterId, rebate.Quota, "referral rebate")
		}
	}
	var logIds []int
	if err := DB.WithContext(ctx).Model(&ReferralRebate{}).Where("log_pending = ?", true).Order("id").
		Limit(200).Pluck("id", &logIds).Error; err != nil {
		return errors.Join(result, err)
	}
	for _, id := range logIds {
		result = errors.Join(result, publishReferralRebateLog(ctx, id))
	}
	return result
}

func (reward *InvitationReward) creditWallet(tx *gorm.DB) (bool, error) {
	query := tx.Model(&User{}).Where("id = ? AND quota <= ?", reward.InviterId, common.MaxWalletQuota-reward.Quota)
	columns := map[string]any{"quota": gorm.Expr("quota + ?", reward.Quota)}
	if reward.InviteeId != nil {
		query = query.Where("aff_history <= ?", math.MaxInt64-reward.Quota)
		columns["aff_count"] = gorm.Expr("aff_count + 1")
		columns["aff_history"] = gorm.Expr("aff_history + ?", reward.Quota)
	}
	result := query.UpdateColumns(columns)
	return result.RowsAffected == 1, result.Error
}

func (reward *InvitationReward) afterCredit() {
	syncCreditUserQuotaCache(reward.InviterId, reward.Quota, "invitation reward")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := publishInvitationRewardLog(ctx, reward.Id); err != nil {
		common.SysError(fmt.Sprintf("failed to record invitation reward log for reward %d: %v", reward.Id, err))
	}
}

func publishInvitationRewardLog(ctx context.Context, id int) error {
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&InvitationReward{}).Where("id = ? AND log_pending = ? AND credited_at > 0", id, true).
			UpdateColumn("log_pending", false)
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		var reward InvitationReward
		if err := tx.First(&reward, id).Error; err != nil {
			return err
		}
		other := NewLogOther()
		other.SetPublic("invitation_reward_quota", reward.Quota)
		content := common.NewMessage("Bonus for inviting a user {{quota}}", map[string]any{"quota": logger.FormatQuota(reward.Quota)})
		return insertReferralCreditLog(tx, Log{
			UserId: reward.InviterId, CreatedAt: reward.CreditedAt, Type: LogTypeTopup,
			RequestId: fmt.Sprintf("invitation-reward-%d", reward.Id),
			Content:   other.setContent([]*common.Message{content}), Other: other.JSONString(),
		})
	})
}

func ReconcileInvitationRewards(ctx context.Context) error {
	now := common.GetTimestamp()
	var ids []int
	err := DB.WithContext(ctx).Model(&InvitationReward{}).
		Joins("JOIN users ON users.id = invitation_rewards.inviter_id AND users.deleted_at IS NULL").
		Where("invitation_rewards.credited_at = 0 AND invitation_rewards.available_at <= ? AND users.quota <= ? - invitation_rewards.quota AND (invitation_rewards.invitee_id IS NULL OR users.aff_history <= ? - invitation_rewards.quota)",
			now, common.MaxWalletQuota, int64(math.MaxInt64)).
		Order("invitation_rewards.available_at, invitation_rewards.id").Limit(200).
		Pluck("invitation_rewards.id", &ids).Error
	if err != nil {
		return err
	}
	var result error
	for _, id := range ids {
		var reward InvitationReward
		credited := false
		err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := lockForUpdate(tx).First(&reward, id).Error; err != nil {
				return err
			}
			if reward.CreditedAt != 0 {
				return nil
			}
			var err error
			credited, err = reward.creditWallet(tx)
			if err != nil || !credited {
				return err
			}
			return tx.Model(&reward).Updates(map[string]any{"credited_at": now, "log_pending": true}).Error
		})
		if err != nil {
			result = errors.Join(result, err)
		} else if credited {
			syncCreditUserQuotaCache(reward.InviterId, reward.Quota, "invitation reward")
		}
	}
	var logIds []int
	if err := DB.WithContext(ctx).Model(&InvitationReward{}).Where("log_pending = ?", true).
		Order("id").Limit(200).Pluck("id", &logIds).Error; err != nil {
		return errors.Join(result, err)
	}
	for _, id := range logIds {
		result = errors.Join(result, publishInvitationRewardLog(ctx, id))
	}
	return result
}

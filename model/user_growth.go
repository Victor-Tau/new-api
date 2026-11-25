package model

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const growthBatchSize = 500
const growthBackfillKey = "_internal.GrowthEventsBackfillV1"
const vipLevelBackfillKey = "_internal.UserVIPLevelsV1"
const growthTopup = "topup"
const growthRedemption = "redemption"
const growthAdminAdjustment = "admin_adjustment"

var ErrInvalidGrowthAmount = errors.New("invalid VIP growth amount")
var ErrUserGrowthPermission = errors.New("cannot adjust VIP growth for this user role")

type GrowthEvent struct {
	Id        int64  `json:"id"`
	UserId    int    `json:"-" gorm:"index:idx_growth_user_time,priority:1"`
	Quota     int64  `json:"quota"`
	Source    string `json:"source" gorm:"size:32;not null;uniqueIndex:idx_growth_source_ref,priority:1"`
	RefId     string `json:"-" gorm:"size:255;not null;uniqueIndex:idx_growth_source_ref,priority:2"`
	CreatedAt int64  `json:"created_at" gorm:"index:idx_growth_user_time,priority:2;autoCreateTime:false"`
}

type UserGrowth struct {
	UserId        int `gorm:"primaryKey;autoIncrement:false"`
	Quota         int64
	WindowDays    int
	WindowStart   int64
	ConfigVersion string `gorm:"size:64"`
	NextExpiry    *int64 `gorm:"index"`
	TierPending   bool
}

func (UserGrowth) TableName() string { return "user_growth" }

func (settings VIPSettings) windowStart(now int64) int64 {
	return now - int64(settings.WindowDays)*86400
}

func GrowthAmount(quota int64) decimal.Decimal {
	return decimal.NewFromInt(quota).Div(decimal.NewFromFloat(common.QuotaPerUnit)).Round(2)
}

func (settings VIPSettings) tierForQuota(quota int64) VipTier {
	for level := len(settings.Tiers) - 1; level > 0; level-- {
		tier := settings.Tiers[level]
		threshold := decimal.NewFromFloat(tier.MinAmount).Mul(decimal.NewFromFloat(common.QuotaPerUnit))
		if decimal.NewFromInt(quota).GreaterThanOrEqual(threshold) {
			return tier
		}
	}
	return settings.Tiers[0]
}

// The wallet UPDATE already holds the user row lock when this is called.
func recordGrowthCredit(tx *gorm.DB, userId int, quota int, source, refId string, creditedAt int64) error {
	event := GrowthEvent{UserId: userId, Quota: int64(quota), Source: source, RefId: refId, CreatedAt: creditedAt}
	if err := tx.Create(&event).Error; err != nil {
		return err
	}
	_, err := rebuildUserGrowth(tx, userId, CurrentVIPSettings(), common.GetTimestamp())
	return err
}

func rebuildUserGrowth(tx *gorm.DB, userId int, settings VIPSettings, now int64) (UserGrowth, error) {
	growth, err := calculateUserGrowth(tx, userId, settings, now)
	if err != nil {
		return growth, err
	}
	growth.Quota = max(0, growth.Quota)
	return growth, saveUserGrowth(tx, growth)
}

func calculateUserGrowth(tx *gorm.DB, userId int, settings VIPSettings, now int64) (UserGrowth, error) {
	growth := UserGrowth{
		UserId: userId, WindowDays: settings.WindowDays, WindowStart: settings.windowStart(now),
		ConfigVersion: settings.version, TierPending: true,
	}
	var totals struct {
		Quota int64
		First *int64
	}
	if err := tx.Model(&GrowthEvent{}).
		Where("user_id = ? AND created_at >= ?", userId, growth.WindowStart).
		Select("COALESCE(SUM(quota), 0) AS quota, MIN(created_at) AS first").
		Scan(&totals).Error; err != nil {
		return growth, err
	}
	growth.Quota = totals.Quota
	if totals.First != nil {
		expiry := *totals.First + int64(settings.WindowDays)*86400 + 1
		growth.NextExpiry = &expiry
	}
	return growth, nil
}

func saveUserGrowth(tx *gorm.DB, growth UserGrowth) error {
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"quota", "window_days", "window_start", "config_version", "next_expiry", "tier_pending"}),
	}).Create(&growth).Error
}

// GetUserGrowthAmounts returns the current window totals for a page of users.
func GetUserGrowthAmounts(userIDs []int) (map[int]float64, error) {
	amounts := make(map[int]float64, len(userIDs))
	if len(userIDs) == 0 {
		return amounts, nil
	}
	var totals []struct {
		UserID int
		Quota  int64
	}
	windowStart := CurrentVIPSettings().windowStart(common.GetTimestamp())
	err := DB.Model(&GrowthEvent{}).Where("user_id IN ? AND created_at >= ?", userIDs, windowStart).
		Select("user_id, SUM(quota) AS quota").Group("user_id").Scan(&totals).Error
	if err != nil {
		return nil, err
	}
	for _, total := range totals {
		amounts[total.UserID] = GrowthAmount(max(0, total.Quota)).InexactFloat64()
	}
	return amounts, nil
}

type UserGrowthAdjustment struct {
	Username string
	Before   float64
	Amount   float64
	Group    string
}

func AdjustUserGrowth(userID, operatorRole int, amount decimal.Decimal) (UserGrowthAdjustment, error) {
	var adjustment UserGrowthAdjustment
	if userID <= 0 || amount.IsNegative() || !amount.Equal(amount.Round(2)) {
		return adjustment, ErrInvalidGrowthAmount
	}
	targetQuota, err := common.WalletQuotaFromDecimalStrict(amount.Mul(decimal.NewFromFloat(common.QuotaPerUnit)))
	if err != nil {
		return adjustment, ErrInvalidGrowthAmount
	}

	var originalGroup string
	err = DB.Transaction(func(tx *gorm.DB) error {
		user, err := lockUserForGrowth(tx, userID)
		if err != nil {
			return err
		}
		if operatorRole < common.RoleAdminUser || (operatorRole != common.RoleRootUser && operatorRole <= user.Role) {
			return ErrUserGrowthPermission
		}
		originalGroup = user.Group
		settings := CurrentVIPSettings()
		now := common.GetTimestamp()
		growth, err := calculateUserGrowth(tx, userID, settings, now)
		if err != nil {
			return err
		}
		beforeQuota := max(0, growth.Quota)
		if beforeQuota != int64(targetQuota) {
			// Use the raw total for the difference so adjustments still reach the
			// requested amount when the sum of earlier events has fallen below zero.
			delta := decimal.NewFromInt(int64(targetQuota)).Sub(decimal.NewFromInt(growth.Quota)).BigInt()
			if !delta.IsInt64() {
				return ErrInvalidGrowthAmount
			}
			event := GrowthEvent{UserId: userID, Quota: delta.Int64(), Source: growthAdminAdjustment, RefId: common.GetUUID(), CreatedAt: now}
			if err := tx.Create(&event).Error; err != nil {
				return err
			}
			growth.Quota = int64(targetQuota)
			expiry := now + int64(settings.WindowDays)*86400 + 1
			if growth.NextExpiry == nil || expiry < *growth.NextExpiry {
				growth.NextExpiry = &expiry
			}
			if err := saveUserGrowth(tx, growth); err != nil {
				return err
			}
			if err := applyUserGrowthTier(tx, &user, growth, settings, true); err != nil {
				return err
			}
		}
		adjustment = UserGrowthAdjustment{
			Username: user.Username,
			Before:   GrowthAmount(beforeQuota).InexactFloat64(),
			Amount:   GrowthAmount(max(0, growth.Quota)).InexactFloat64(),
			Group:    user.Group,
		}
		return nil
	})
	if err == nil {
		publishUserGrowthGroupChange(userID, originalGroup, adjustment.Group)
	}
	return adjustment, err
}

func GetUserVIPLevel(userId int) (*int, error) {
	var user User
	err := DB.Select("vip_level").First(&user, userId).Error
	return user.VIPLevel, err
}

func GetUserVIPMembership(userId int) (User, error) {
	var user User
	err := DB.Select("id", "group", "vip_level").First(&user, userId).Error
	return user, err
}

func GetUserGrowth(userId int) (UserGrowth, error) {
	var growth UserGrowth
	err := DB.First(&growth, "user_id = ?", userId).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		settings := CurrentVIPSettings()
		return UserGrowth{WindowDays: settings.WindowDays, WindowStart: settings.windowStart(common.GetTimestamp())}, nil
	}
	return growth, err
}

func GetUserGrowthEvents(userId int, windowStart int64, offset, limit int) ([]GrowthEvent, int64, error) {
	items := make([]GrowthEvent, 0)
	var total int64
	query := DB.Model(&GrowthEvent{}).Where("user_id = ? AND created_at >= ?", userId, windowStart)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := query.Order("created_at DESC, id DESC").Offset(offset).Limit(limit).Find(&items).Error
	return items, total, err
}

func refreshUserTierAfterCredit(userId int) {
	if err := reevaluateUserTier(userId, false); err != nil {
		common.SysError(common.LogText("refresh VIP after credit for user %d: %v", userId, err))
	}
}

func reevaluateUserTier(userId int, maintenance bool) error {
	var from, to string
	err := DB.Transaction(func(tx *gorm.DB) error {
		user, err := lockUserForGrowth(tx, userId)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		settings := CurrentVIPSettings()
		now := common.GetTimestamp()
		var growth UserGrowth
		err = tx.First(&growth, "user_id = ?", userId).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if maintenance && (growth.ConfigVersion != settings.version ||
			(growth.NextExpiry != nil && *growth.NextExpiry <= now)) {
			growth, err = rebuildUserGrowth(tx, userId, settings, now)
			if err != nil {
				return err
			}
		}
		from = user.Group
		if err := applyUserGrowthTier(tx, &user, growth, settings, maintenance); err != nil {
			return err
		}
		to = user.Group
		return nil
	})
	if err != nil {
		return err
	}
	publishUserGrowthGroupChange(userId, from, to)
	return nil
}

func lockUserForGrowth(tx *gorm.DB, userID int) (User, error) {
	var user User
	// Start SQLite as a writer before reading events so concurrent credits
	// and adjustments take the same lock without a stale read snapshot.
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		if err := tx.Model(&User{}).Where("id = ?", userID).
			UpdateColumn("vip_level", gorm.Expr("vip_level")).Error; err != nil {
			return user, err
		}
	}
	err := lockForUpdate(tx).Select("id", "username", "role", "group", "vip_level").First(&user, userID).Error
	return user, err
}

func applyUserGrowthTier(tx *gorm.DB, user *User, growth UserGrowth, settings VIPSettings, allowDowngrade bool) error {
	if growth.ConfigVersion != settings.version {
		return nil
	}
	eligible := settings.tierForQuota(growth.Quota).Level
	if settings.Enabled && user.VIPLevel != nil && eligible != *user.VIPLevel {
		if !allowDowngrade && eligible < *user.VIPLevel {
			return nil
		}
		_, special, ok := common.SplitUserGroup(user.Group)
		if !ok {
			return nil
		}
		target := common.ComposeUserGroup(eligible, special)
		result := tx.Model(&User{}).Where("id = ?", user.Id).
			Where(clause.Eq{Column: clause.Column{Name: "group"}, Value: user.Group}).
			Updates(userGroupColumns(target))
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		user.Group = target
	}
	return tx.Model(&UserGrowth{}).Where("user_id = ?", user.Id).Update("tier_pending", false).Error
}

func publishUserGrowthGroupChange(userID int, from, to string) {
	if from == to {
		return
	}
	if err := RefreshUserGroupCache(userID); err != nil {
		common.SysError(common.LogText("refresh VIP group cache for user %d: %v", userID, err))
	}
	RecordLog(userID, LogTypeSystem, common.NewMessage("Membership changed from {{from}} to {{to}}", map[string]any{"from": from, "to": to}))
}

func RefreshUserGrowths(includeExpiry bool, requireSuccess bool) error {
	settings := CurrentVIPSettings()
	var failure error
	for cursor := 0; ; {
		ids := make([]int, 0, growthBatchSize)
		query := DB.Model(&User{}).
			Joins("LEFT JOIN user_growth ON user_growth.user_id = users.id").
			Where("users.id > ?", cursor)
		conditions := "user_growth.config_version <> ? OR user_growth.tier_pending = ?"
		args := []any{settings.version, true}
		if includeExpiry {
			conditions += " OR user_growth.next_expiry <= ? OR users.vip_level > ?"
			args = append(args, common.GetTimestamp(), 0)
		}
		if err := query.Where("("+conditions+")", args...).Order("users.id").Limit(growthBatchSize).Pluck("users.id", &ids).Error; err != nil {
			return err
		}
		for _, id := range ids {
			if err := reevaluateUserTier(id, true); err != nil {
				if requireSuccess {
					return err
				}
				failure = err
				common.SysError(common.LogText("maintain VIP for user %d: %v", id, err))
			}
			cursor = id
		}
		if len(ids) < growthBatchSize {
			return failure
		}
	}
}

func userGroupColumns(group string) map[string]any {
	return map[string]any{"group": group, "vip_level": common.VIPLevelForGroup(group)}
}

func migrateUserVIPLevels() error {
	done, err := membershipMigrationDone(vipLevelBackfillKey)
	if err != nil || done {
		return err
	}
	for cursor := 0; ; {
		var users []User
		if err := DB.Unscoped().Select("id", "group").Where("id > ?", cursor).Order("id").Limit(growthBatchSize).Find(&users).Error; err != nil {
			return err
		}
		if err := DB.Transaction(func(tx *gorm.DB) error {
			for _, user := range users {
				group := user.Group
				if group == "" || group == "default" {
					group = common.BaseVIPGroup
				}
				if err := tx.Unscoped().Model(&User{}).Where("id = ? AND "+commonGroupCol+" = ?", user.Id, user.Group).
					Updates(userGroupColumns(group)).Error; err != nil {
					return err
				}
				cursor = user.Id
			}
			return nil
		}); err != nil {
			return err
		}
		if len(users) < growthBatchSize {
			return completeMembershipMigration(vipLevelBackfillKey)
		}
	}
}

func membershipMigrationDone(key string) (bool, error) {
	var count int64
	err := DB.Model(&Option{}).Where(commonKeyCol+" = ? AND value = ?", key, "true").Count(&count).Error
	return count > 0, err
}

func completeMembershipMigration(key string) error {
	return DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&Option{Key: key, Value: "true"}).Error
}

func InitializeUserGrowth() error {
	done, err := membershipMigrationDone(growthBackfillKey)
	if done || err != nil {
		return err
	}
	settings := CurrentVIPSettings()
	cutoff := settings.windowStart(common.GetTimestamp())
	topups := DB.Model(&TopUp{}).Where("status = ? AND complete_time >= ? AND user_id > 0 AND trade_no <> ''",
		common.TopUpStatusSuccess, cutoff).
		Where("COALESCE(payment_provider, '') <> ?", PaymentProviderBalance).
		Where("NOT EXISTS (?)", DB.Model(&SubscriptionOrder{}).Select("1").Where("subscription_orders.trade_no = top_ups.trade_no"))
	if err := backfillGrowthEvents(topups, func(order TopUp) (GrowthEvent, error) {
		amount := decimal.NewFromInt(order.Amount)
		switch {
		case order.PaymentProvider == PaymentProviderStripe || (order.PaymentProvider == "" && order.PaymentMethod == PaymentMethodStripe):
			amount = decimal.NewFromFloat(order.Money).Mul(decimal.NewFromFloat(common.QuotaPerUnit))
		case order.PaymentProvider == PaymentProviderCreem || (order.PaymentProvider == "" && order.PaymentMethod == PaymentMethodCreem):
		default:
			amount = amount.Mul(decimal.NewFromFloat(common.QuotaPerUnit))
		}
		quota, err := common.WalletQuotaFromDecimalStrict(amount)
		return GrowthEvent{UserId: order.UserId, Source: growthTopup, Quota: int64(quota), RefId: order.TradeNo, CreatedAt: order.CompleteTime}, err
	}); err != nil {
		return err
	}
	redemptions := DB.Unscoped().Model(&Redemption{}).Where("status = ? AND redeemed_time >= ? AND used_user_id > 0",
		common.RedemptionCodeStatusUsed, cutoff)
	if err := backfillGrowthEvents(redemptions, func(code Redemption) (GrowthEvent, error) {
		return GrowthEvent{UserId: code.UsedUserId, Source: growthRedemption, Quota: int64(code.Quota), RefId: strconv.Itoa(code.Id), CreatedAt: code.RedeemedTime}, nil
	}); err != nil {
		return err
	}
	// Seed users with events and any existing VIP tiers before handling traffic.
	for cursor := 0; ; {
		var ids []int
		events := DB.Model(&GrowthEvent{}).Select("1").Where("growth_events.user_id = users.id")
		err := DB.Model(&User{}).Where("users.id > ? AND (users.vip_level > 0 OR EXISTS (?))", cursor, events).
			Order("users.id").Limit(growthBatchSize).Pluck("users.id", &ids).Error
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := reevaluateUserTier(id, true); err != nil {
				return err
			}
			cursor = id
		}
		if len(ids) < growthBatchSize {
			return completeMembershipMigration(growthBackfillKey)
		}
	}
}

func backfillGrowthEvents[T any](query *gorm.DB, convert func(T) (GrowthEvent, error)) error {
	var records []T
	return query.FindInBatches(&records, growthBatchSize, func(_ *gorm.DB, _ int) error {
		events := make([]GrowthEvent, 0, len(records))
		for _, row := range records {
			event, err := convert(row)
			if err != nil {
				return fmt.Errorf("backfill growth event %s/%s: %w", event.Source, event.RefId, err)
			}
			if event.Quota > 0 {
				events = append(events, event)
			}
		}
		if len(events) == 0 {
			return nil
		}
		return DB.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "source"}, {Name: "ref_id"}},
			DoNothing: true,
		}).CreateInBatches(events, 100).Error
	}).Error
}

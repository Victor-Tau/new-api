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
	growth.Quota = max(0, totals.Quota)
	if totals.First != nil {
		expiry := *totals.First + int64(settings.WindowDays)*86400 + 1
		growth.NextExpiry = &expiry
	}
	return growth, tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"quota", "window_days", "window_start", "config_version", "next_expiry", "tier_pending"}),
	}).Create(&growth).Error
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
		// Acquire the same user lock as a credit. This also starts SQLite as a
		// writer, before any read can establish a stale snapshot.
		if err := tx.Model(&User{}).Where("id = ?", userId).
			UpdateColumn("quota", gorm.Expr("quota")).Error; err != nil {
			return err
		}
		var user User
		err := tx.Select("id", "group", "vip_level").First(&user, userId).Error
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
		if growth.ConfigVersion != settings.version {
			return nil
		}
		eligible := settings.tierForQuota(growth.Quota).Level
		if settings.Enabled && user.VIPLevel != nil && eligible != *user.VIPLevel {
			if !maintenance && eligible < *user.VIPLevel {
				return nil
			}
			_, special, ok := common.SplitUserGroup(user.Group)
			if !ok {
				return nil
			}
			target := common.ComposeUserGroup(eligible, special)
			result := tx.Model(&User{}).Where("id = ? AND "+commonGroupCol+" = ?", userId, user.Group).
				Updates(userGroupColumns(target))
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return nil
			}
			from, to = user.Group, target
		}
		return tx.Model(&UserGrowth{}).Where("user_id = ?", userId).Update("tier_pending", false).Error
	})
	if err != nil {
		return err
	}
	if to != "" {
		if err := RefreshUserGroupCache(userId); err != nil {
			common.SysError(common.LogText("refresh VIP group cache for user %d: %v", userId, err))
		}
		RecordLog(userId, LogTypeSystem, common.NewMessage("Membership changed from {{from}} to {{to}}", map[string]any{"from": from, "to": to}))
	}
	return nil
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

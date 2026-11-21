package model

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestReferralDatabaseMatrix(t *testing.T) {
	engines := []struct {
		name string
		kind common.DatabaseType
		open func(string) gorm.Dialector
	}{
		{"SQLITE", common.DatabaseTypeSQLite, sqlite.Open},
		{"MYSQL", common.DatabaseTypeMySQL, mysql.Open},
		{"POSTGRES", common.DatabaseTypePostgreSQL, postgres.Open},
	}
	for _, engine := range engines {
		for _, mode := range []string{"FRESH", "UPGRADE"} {
			t.Run(engine.name+"/"+mode, func(t *testing.T) {
				prefix := "TEST_REFERRAL_" + engine.name + "_" + mode
				mainDSN, logDSN := os.Getenv(prefix+"_DSN"), os.Getenv(prefix+"_LOG_DSN")
				if engine.kind == common.DatabaseTypeSQLite && mode == "FRESH" {
					dir := t.TempDir()
					mainDSN, logDSN = filepath.Join(dir, "main.sqlite"), filepath.Join(dir, "log.sqlite")
				}
				if mainDSN == "" {
					t.Skip("dedicated database DSN not supplied")
				}
				mainDB, err := gorm.Open(engine.open(mainDSN), &gorm.Config{})
				require.NoError(t, err)
				logDB := mainDB
				if logDSN != "" {
					logDB, err = gorm.Open(engine.open(logDSN), &gorm.Config{})
					require.NoError(t, err)
				}
				databases := []*gorm.DB{mainDB}
				if logDB != mainDB {
					databases = append(databases, logDB)
				}
				for _, db := range databases {
					sqlDB, err := db.DB()
					require.NoError(t, err)
					t.Cleanup(func() { assert.NoError(t, sqlDB.Close()) })
				}

				oldDB, oldLogDB := DB, LOG_DB
				oldMain, oldLog := common.MainDatabaseType(), common.LogDatabaseType()
				oldRedis, oldOptions := common.RedisEnabled, common.OptionMap
				oldCompliance := *operation_setting.GetPaymentSetting()
				oldExchangeRate := operation_setting.USDExchangeRate
				t.Cleanup(func() {
					DB, LOG_DB = oldDB, oldLogDB
					common.SetDatabaseTypes(oldMain, oldLog)
					initCol()
					common.RedisEnabled = oldRedis
					common.OptionMap = oldOptions
					*operation_setting.GetPaymentSetting() = oldCompliance
					operation_setting.USDExchangeRate = oldExchangeRate
				})
				DB, LOG_DB = mainDB, logDB
				common.SetDatabaseTypes(engine.kind, engine.kind)
				initCol()
				common.RedisEnabled = false
				common.OptionMap = map[string]string{
					"USDExchangeRate":           "7.3",
					ReferralExchangeRatesOption: "{}",
				}
				operation_setting.USDExchangeRate = 7.3
				operation_setting.GetPaymentSetting().ComplianceConfirmed = true
				operation_setting.GetPaymentSetting().ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion

				var version string
				versionQuery := "SELECT VERSION()"
				if engine.kind == common.DatabaseTypeSQLite {
					versionQuery = "SELECT sqlite_version()"
				}
				require.NoError(t, DB.Raw(versionQuery).Scan(&version).Error)
				t.Logf("database version: %s", version)

				var legacyTopUp TopUp
				var legacyInvitee User
				if mode == "UPGRADE" {
					require.True(t, DB.Migrator().HasTable(&User{}))
					require.False(t, DB.Migrator().HasColumn(&ReferralRebate{}, "RedemptionId"))
					require.NoError(t, DB.Table("users").Create(map[string]any{
						"username": "referral-legacy", "password": "", "aff_code": "legacy-ref",
						"quota": 12345, "aff_quota": 7500, "aff_history": 9000, "aff_count": 3,
					}).Error)
					require.NoError(t, DB.Table("users").Create(map[string]any{
						"username": "referral-legacy-full", "password": "", "aff_code": "legacy-full",
						"quota": common.MaxWalletQuota, "aff_quota": 8000, "aff_history": 9000, "aff_count": 3,
					}).Error)
					if DB.Migrator().HasTable(&ReferralRebate{}) {
						var legacy User
						require.NoError(t, DB.Select("id").First(&legacy, "username = ?", "referral-legacy").Error)
						now := common.GetTimestamp()
						legacyTopUp = TopUp{UserId: legacy.Id, TradeNo: "legacy-paid", Status: common.TopUpStatusSuccess, CompleteTime: now}
						require.NoError(t, legacyTopUp.Insert())
						require.NoError(t, DB.Table("referral_rebates").Create(map[string]any{
							"top_up_id": legacyTopUp.Id, "inviter_id": legacy.Id, "invitee_username": "legacy-invitee",
							"paid_amount": "12.50", "paid_currency": "USD", "rate": 1000, "quota": 625000,
							"created_at": now, "credited_at": now,
						}).Error)
					}
					if DB.Migrator().HasTable(&InvitationReward{}) {
						require.False(t, DB.Migrator().HasColumn(&InvitationReward{}, "LogPending"))
						var legacy User
						require.NoError(t, DB.Select("id").First(&legacy, "username = ?", "referral-legacy").Error)
						legacyInvitee = User{Username: "legacy-reward-invitee", AffCode: "legacy-invitee", InviterId: legacy.Id}
						require.NoError(t, DB.Create(&legacyInvitee).Error)
						require.NoError(t, DB.Table("invitation_rewards").Create(map[string]any{
							"invitee_id": legacyInvitee.Id, "inviter_id": legacy.Id, "quota": 7500,
							"available_at": legacyInvitee.CreatedAt + 3600,
						}).Error)
					}
				} else {
					require.False(t, DB.Migrator().HasTable(&User{}))
				}
				for range 2 {
					require.NoError(t, migrateDB())
					require.NoError(t, migrateLOGDB())
					require.NoError(t, MigratePendingInvitationRewards())
				}
				if mode == "UPGRADE" {
					var legacy User
					require.NoError(t, DB.Where("username = ?", "referral-legacy").First(&legacy).Error)
					assert.Equal(t, 19845, legacy.Quota)
					assert.Zero(t, legacy.AffQuota)
					assert.Equal(t, 9000, legacy.AffHistoryQuota)
					assert.Equal(t, 3, legacy.AffCount)
					assert.False(t, legacy.InvitationDisabled)
					assert.False(t, legacy.ReferralRebateEnabled)
					assert.Nil(t, legacy.ReferralRebateRate)
					id, err := GetUserIdByAffCode("legacy-ref")
					require.NoError(t, err)
					assert.Equal(t, legacy.Id, id)
					assert.Error(t, DB.Create(&User{Username: legacy.Username, AffCode: "duplicate-name"}).Error)
					if legacyTopUp.Id != 0 {
						var rebate ReferralRebate
						require.NoError(t, DB.First(&rebate, "top_up_id = ?", legacyTopUp.Id).Error)
						assert.Nil(t, rebate.RedemptionId)
						assert.Zero(t, rebate.RedemptionQuota)
						assert.Equal(t, "12.50", rebate.PaidAmount)
						assert.Equal(t, "USD", rebate.PaidCurrency)
						assert.Equal(t, 625000, rebate.Quota)
						assert.Equal(t, legacyTopUp.CompleteTime, rebate.CreditedAt)
						rebate.Id = 0
						assert.Error(t, DB.Create(&rebate).Error)
					}
					if legacyInvitee.Id != 0 {
						records, total, err := ListReferralRebates(t.Context(), legacy.Id, &common.PageInfo{Page: 1, PageSize: 10}, false, true)
						require.NoError(t, err)
						require.Len(t, records, 1)
						assert.EqualValues(t, 1, total)
						assert.Equal(t, "invitation_reward", records[0].RecordType)
						assert.Equal(t, legacyInvitee.Username, records[0].InviteeUsername)
						assert.Equal(t, legacyInvitee.CreatedAt, records[0].CreatedAt)
						assert.Equal(t, legacyInvitee.CreatedAt+3600, records[0].AvailableAt)
						assert.Equal(t, 7500, records[0].Quota)
						assert.Zero(t, records[0].CreditedAt)
						require.Error(t, DB.Create(&InvitationReward{InviteeId: &legacyInvitee.Id, InviterId: legacy.Id, Quota: 7500}).Error)
					}

					var full User
					require.NoError(t, DB.Where("username = ?", "referral-legacy-full").First(&full).Error)
					assert.Equal(t, common.MaxWalletQuota, full.Quota)
					assert.Zero(t, full.AffQuota)
					require.NoError(t, DB.Model(&full).UpdateColumn("quota", common.MaxWalletQuota-8000).Error)
					for range 2 {
						require.NoError(t, ReconcileInvitationRewards(t.Context()))
					}
					require.NoError(t, DB.First(&full, full.Id).Error)
					assert.Equal(t, common.MaxWalletQuota, full.Quota)
					assert.Equal(t, 9000, full.AffHistoryQuota)
					assert.Equal(t, 3, full.AffCount)
				}

				require.NoError(t, UpdateOption(ReferralDefaultRateOption, "1000"))
				require.Error(t, UpdateOption(ReferralDefaultRateOption, "10001"))
				assert.Equal(t, 1000, ReferralDefaultRate())
				exerciseReferralSettlement(t)
				if engine.kind == common.DatabaseTypeSQLite && mode == "FRESH" {
					t.Setenv("LOG_SQL_DSN", "local")
					require.NoError(t, InitLogDB())
				}
				exerciseInvitationRewards(t)
			})
		}
	}
}

func exerciseInvitationRewards(t *testing.T) {
	t.Helper()
	oldReward := common.QuotaForInviter
	t.Cleanup(func() { common.QuotaForInviter = oldReward })
	require.NoError(t, UpdateOptionsBulk(map[string]string{
		"QuotaForInviter": "7500", InvitationRewardDelayOption: "0",
		ReferralRebateDelayOption: "12",
	}))
	inviter := User{Username: "reward-inviter", AffCode: "reward-code", Quota: 1000, ReferralRebateEnabled: true}
	require.NoError(t, DB.Create(&inviter).Error)
	register := func(username string) User {
		t.Helper()
		user := User{Username: username}
		require.NoError(t, user.Insert(inviter.Id))
		require.Equal(t, inviter.Id, user.InviterId)
		return user
	}
	assertWallet := func(quota, total, count int) {
		t.Helper()
		stored, err := GetUserById(inviter.Id, false)
		require.NoError(t, err)
		assert.Equal(t, quota, stored.Quota)
		assert.Equal(t, total, stored.AffHistoryQuota)
		assert.Equal(t, count, stored.AffCount)
		assert.Zero(t, stored.AffQuota)
	}
	immediate := register("reward-immediate")
	assertWallet(8500, 7500, 1)
	duplicate := User{Username: immediate.Username}
	require.Error(t, duplicate.Insert(inviter.Id))
	assertWallet(8500, 7500, 1)

	require.NoError(t, UpdateOption(InvitationRewardDelayOption, "5"))
	delayed := register("reward-delayed")
	var reward InvitationReward
	require.NoError(t, DB.Where("invitee_id = ?", delayed.Id).First(&reward).Error)
	assert.Equal(t, delayed.CreatedAt+5*3600, reward.AvailableAt)
	require.Error(t, UpdateOption(InvitationRewardDelayOption, "-1"))
	require.NoError(t, UpdateOption(InvitationRewardDelayOption, "0"))
	require.NoError(t, ReconcileInvitationRewards(t.Context()))
	assertWallet(8500, 7500, 1)
	logs, _, err := GetUserLogs(inviter.Id, LogFilter{Type: LogTypeTopup}, 0, 10)
	require.NoError(t, err)
	require.Len(t, logs, 1)
	assert.Contains(t, logs[0].Content, "Bonus for inviting a user")

	topUp := TopUp{
		UserId: delayed.Id, Amount: 20, Money: 90, TradeNo: "reward-delayed-rebate",
		PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusPending,
	}
	require.NoError(t, topUp.Insert())
	payment := ParseWalletTopUpPayment("10", "USD")
	_, err = RechargeEpay(topUp.TradeNo, "alipay", "", payment)
	require.NoError(t, err)
	var rebate ReferralRebate
	require.NoError(t, DB.Where("top_up_id = ?", topUp.Id).First(&rebate).Error)
	assert.Equal(t, rebate.CreatedAt+12*3600, rebate.AvailableAt)
	records, total, err := ListReferralRebates(t.Context(), inviter.Id, &common.PageInfo{Page: 1, PageSize: 10}, true, true)
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	require.Len(t, records, 3)
	assert.Contains(t, records, ReferralRebateRecord{
		RecordType: "invitation_reward",
		ReferralRebate: ReferralRebate{
			Id: reward.Id, InviteeUsername: delayed.Username, PaidAmount: "0", PaidCurrency: "USD",
			Quota: 7500, CreatedAt: delayed.CreatedAt, AvailableAt: reward.AvailableAt,
		},
	})
	require.NoError(t, ReconcileReferralRebates(t.Context()))
	assertWallet(8500, 7500, 1)

	now := common.GetTimestamp()
	require.NoError(t, DB.Model(&reward).UpdateColumn("available_at", now).Error)
	require.NoError(t, DB.Model(&rebate).UpdateColumn("available_at", now).Error)
	for range 2 {
		require.NoError(t, ReconcileInvitationRewards(t.Context()))
		require.NoError(t, ReconcileReferralRebates(t.Context()))
	}
	assertWallet(16000+rebate.Quota, 15000, 2)
	logs, _, err = GetUserLogs(inviter.Id, LogFilter{Type: LogTypeSystem}, 0, 10)
	require.NoError(t, err)
	assert.Empty(t, logs)
	logs, _, err = GetUserLogs(inviter.Id, LogFilter{Type: LogTypeTopup}, 0, 10)
	require.NoError(t, err)
	require.Len(t, logs, 3)
	invitationLogs := 0
	for _, log := range logs {
		if strings.Contains(log.Content, "Bonus for inviting a user") {
			invitationLogs++
			assert.Contains(t, log.Other, `"invitation_reward_quota":7500`)
			continue
		}
		assert.Contains(t, log.Content, "Referral rebate / Invited user reward-delayed")
	}
	assert.Equal(t, 2, invitationLogs)
}

func exerciseReferralSettlement(t *testing.T) {
	t.Helper()
	inviter := User{Username: "referrer", AffCode: "referrer-code", ReferralRebateEnabled: true}
	require.NoError(t, DB.Create(&inviter).Error)
	buyer := User{Username: "invited", AffCode: "buyer-code", Email: "alice@example.com", InviterId: inviter.Id}
	require.NoError(t, DB.Create(&buyer).Error)
	other := User{Username: "unrelated", AffCode: "other-code", Email: "other@example.com"}
	require.NoError(t, DB.Create(&other).Error)

	page := &common.PageInfo{Page: 1, PageSize: 10}
	invitees, total, err := ListReferralInvitees(t.Context(), inviter.Id, page)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Equal(t, []ReferralInvitee{{Id: buyer.Id, Username: buyer.Username, Email: "a***e@example.com"}}, invitees)

	require.NoError(t, DB.Model(&inviter).UpdateColumn("invitation_disabled", true).Error)
	_, err = GetUserIdByAffCode(inviter.AffCode)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	late := User{Username: "late-registration"}
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		return late.InsertWithTx(tx, inviter.Id)
	}))
	assert.Zero(t, late.InviterId)

	payment := ParseWalletTopUpPayment("73.00", "CNY")
	complete := func(tradeNo string) *TopUp {
		t.Helper()
		order := &TopUp{
			UserId: buyer.Id, Amount: 20, Money: 90, TradeNo: tradeNo,
			PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusPending,
		}
		require.NoError(t, order.Insert())
		duplicate, err := RechargeEpay(order.TradeNo, "alipay", "", payment)
		require.NoError(t, err)
		assert.False(t, duplicate)
		duplicate, err = RechargeEpay(order.TradeNo, "alipay", "", payment)
		require.NoError(t, err)
		assert.True(t, duplicate)
		return order
	}

	first := complete("rebate-first")
	expected := common.QuotaFromFloat(common.QuotaPerUnit)
	stored, err := GetUserById(inviter.Id, false)
	require.NoError(t, err)
	assert.Equal(t, expected, stored.Quota)
	assert.Zero(t, stored.AffQuota)

	records, total, err := ListReferralRebates(t.Context(), inviter.Id, page, true, true)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, int64(1), total)
	assert.Equal(t, &first.Id, records[0].TopUpId)
	assert.Equal(t, "73", records[0].PaidAmount)
	assert.Equal(t, "CNY", records[0].PaidCurrency)
	assert.Equal(t, 1000, records[0].Rate)
	assert.Equal(t, expected, records[0].Quota)
	assert.Positive(t, records[0].CreditedAt)
	copy := records[0].ReferralRebate
	copy.Id = 0
	assert.Error(t, DB.Create(&copy).Error)
	_, total, err = ListReferralRebates(t.Context(), other.Id, page, true, true)
	require.NoError(t, err)
	assert.Zero(t, total)

	logs, total, err := GetUserLogs(inviter.Id, LogFilter{Type: LogTypeTopup}, 0, 10)
	require.NoError(t, err)
	require.Len(t, logs, 1)
	assert.Equal(t, int64(1), total)
	assert.Contains(t, logs[0].Content, "Referral rebate / Invited user invited")
	assert.Contains(t, logs[0].Other, "Referral rebate / Invited user {{username}} / {{quota}}")
	assert.Contains(t, logs[0].Other, `"referral_rebate_quota":500000`)

	require.NoError(t, DB.Model(&inviter).Updates(map[string]any{
		"referral_rebate_rate": 2000, "quota": common.MaxWalletQuota,
	}).Error)
	waiting := complete("rebate-waiting")
	var pending ReferralRebate
	require.NoError(t, DB.Where("top_up_id = ?", waiting.Id).First(&pending).Error)
	assert.Zero(t, pending.CreditedAt)
	assert.Equal(t, 2*expected, pending.Quota)
	require.NoError(t, DB.Model(&inviter).Updates(map[string]any{
		"referral_rebate_enabled": false, "quota": expected,
	}).Error)
	complete("rebate-disabled")
	require.NoError(t, ReconcileReferralRebates(context.Background()))
	require.NoError(t, ReconcileReferralRebates(context.Background()))
	stored, err = GetUserById(inviter.Id, false)
	require.NoError(t, err)
	assert.Equal(t, 3*expected, stored.Quota)
	_, total, err = GetUserLogs(inviter.Id, LogFilter{Type: LogTypeTopup}, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	_, total, err = ListReferralRebates(t.Context(), inviter.Id, page, true, true)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	require.NoError(t, DB.Model(&inviter).UpdateColumn("referral_rebate_enabled", true).Error)
	payment = ParseWalletTopUpPayment("80", "EUR")
	unconverted := complete("rebate-unconverted")
	var conversion ReferralRebate
	require.NoError(t, DB.First(&conversion, "top_up_id = ?", unconverted.Id).Error)
	assert.True(t, conversion.ExchangeRatePending)
	assert.Zero(t, conversion.CreditedAt)
	require.NoError(t, UpdateOption(ReferralExchangeRatesOption, `{"EUR":0.8}`))
	require.NoError(t, ReconcileReferralRebates(t.Context()))
	stored, err = GetUserById(inviter.Id, false)
	require.NoError(t, err)
	assert.Equal(t, 23*expected, stored.Quota)

	code := Redemption{Id: first.Id, Key: "first-referral-redemption", Quota: 4_000_003}
	require.NoError(t, code.Insert())
	quota, err := Redeem(code.Key, buyer.Id)
	require.NoError(t, err)
	assert.Equal(t, code.Quota, quota)
	_, err = Redeem(code.Key, buyer.Id)
	require.ErrorIs(t, err, ErrRedeemFailed)

	var redeemed ReferralRebate
	require.NoError(t, DB.First(&redeemed, "redemption_id = ?", code.Id).Error)
	assert.Nil(t, redeemed.TopUpId)
	assert.Equal(t, code.Quota, redeemed.RedemptionQuota)
	assert.Empty(t, redeemed.PaidAmount)
	assert.Empty(t, redeemed.PaidCurrency)
	assert.Equal(t, 800_001, redeemed.Quota)
	assert.Positive(t, redeemed.CreditedAt)
	duplicate := redeemed
	duplicate.Id = 0
	assert.Error(t, DB.Create(&duplicate).Error)

	require.NoError(t, UpdateOption(ReferralRebateDelayOption, "2"))
	delayed := Redemption{Id: code.Id + 1, Key: "second-referral-redemption", Quota: 2_500_000}
	require.NoError(t, delayed.Insert())
	_, err = Redeem(delayed.Key, buyer.Id)
	require.NoError(t, err)
	var delayedRebate ReferralRebate
	require.NoError(t, DB.First(&delayedRebate, "redemption_id = ?", delayed.Id).Error)
	assert.Zero(t, delayedRebate.CreditedAt)
	assert.Equal(t, delayedRebate.CreatedAt+2*3600, delayedRebate.AvailableAt)
	require.NoError(t, DB.First(stored, inviter.Id).Error)
	assert.Equal(t, 23*expected+800_001, stored.Quota)
	require.NoError(t, DB.Model(&delayedRebate).UpdateColumn("available_at", common.GetTimestamp()).Error)
	for range 2 {
		require.NoError(t, ReconcileReferralRebates(t.Context()))
	}
	require.NoError(t, DB.First(stored, inviter.Id).Error)
	assert.Equal(t, 23*expected+800_001+500_000, stored.Quota)
	_, total, err = GetUserLogs(inviter.Id, LogFilter{Type: LogTypeTopup}, 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 5, total)
}

package model

import (
	"context"
	"os"
	"path/filepath"
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

				if mode == "UPGRADE" {
					require.True(t, DB.Migrator().HasTable(&User{}))
					require.False(t, DB.Migrator().HasColumn(&User{}, "InvitationDisabled"))
					require.False(t, DB.Migrator().HasTable(&ReferralRebate{}))
					require.NoError(t, DB.Table("users").Create(map[string]any{
						"username": "referral-legacy", "password": "", "aff_code": "legacy-ref",
						"quota": 12345,
					}).Error)
				} else {
					require.False(t, DB.Migrator().HasTable(&User{}))
				}
				for range 2 {
					require.NoError(t, migrateDB())
					require.NoError(t, migrateLOGDB())
				}
				if mode == "UPGRADE" {
					var legacy User
					require.NoError(t, DB.Where("username = ?", "referral-legacy").First(&legacy).Error)
					assert.Equal(t, 12345, legacy.Quota)
					assert.False(t, legacy.InvitationDisabled)
					assert.False(t, legacy.ReferralRebateEnabled)
					assert.Nil(t, legacy.ReferralRebateRate)
					id, err := GetUserIdByAffCode("legacy-ref")
					require.NoError(t, err)
					assert.Equal(t, legacy.Id, id)
					assert.Error(t, DB.Create(&User{Username: legacy.Username, AffCode: "duplicate-name"}).Error)
				}

				require.NoError(t, UpdateOption(ReferralDefaultRateOption, "1000"))
				require.Error(t, UpdateOption(ReferralDefaultRateOption, "10001"))
				assert.Equal(t, 1000, ReferralDefaultRate())
				exerciseReferralSettlement(t)
			})
		}
	}
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

	records, total, err := ListReferralRebates(t.Context(), inviter.Id, page)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, int64(1), total)
	assert.Equal(t, first.Id, records[0].TopUpId)
	assert.Equal(t, "73", records[0].PaidAmount)
	assert.Equal(t, "CNY", records[0].PaidCurrency)
	assert.Equal(t, 1000, records[0].Rate)
	assert.Equal(t, expected, records[0].Quota)
	assert.Positive(t, records[0].CreditedAt)
	copy := records[0]
	copy.Id = 0
	assert.Error(t, DB.Create(&copy).Error)
	_, total, err = ListReferralRebates(t.Context(), other.Id, page)
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
	_, total, err = ListReferralRebates(t.Context(), inviter.Id, page)
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

}

package model

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestVIPDatabaseLifecycle(t *testing.T) {
	for _, engine := range []struct {
		name, dsn, logDSN string
		kind              common.DatabaseType
		open              func(string) gorm.Dialector
	}{
		{"sqlite", "", "", common.DatabaseTypeSQLite, sqlite.Open},
		{"mysql", "TEST_VIP_MYSQL_DSN", "TEST_VIP_MYSQL_LOG_DSN", common.DatabaseTypeMySQL, mysql.Open},
		{"postgres", "TEST_VIP_POSTGRES_DSN", "TEST_VIP_POSTGRES_LOG_DSN", common.DatabaseTypePostgreSQL, postgres.Open},
	} {
		t.Run(engine.name, func(t *testing.T) {
			dsn, logDSN := os.Getenv(engine.dsn), os.Getenv(engine.logDSN)
			if engine.kind == common.DatabaseTypeSQLite {
				dsn = filepath.Join(t.TempDir(), "vip.db")
				logDSN = filepath.Join(t.TempDir(), "vip-logs.db")
			} else if dsn == "" || logDSN == "" {
				t.Skip("configure the dedicated VIP main and log DSNs")
			}
			mainDB, err := gorm.Open(engine.open(dsn), &gorm.Config{})
			require.NoError(t, err)
			logDB, err := gorm.Open(engine.open(logDSN), &gorm.Config{})
			require.NoError(t, err)
			previousDB, previousLog := DB, LOG_DB
			previousMainType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
			previousRedis := common.RedisEnabled
			previousVIP, previousEmail := vipSettings.Load(), emailGroupSettings.Load()
			previousGroupRatio, previousTopupRatio := ratio_setting.GroupRatio2JSONString(), common.TopupGroupRatio2JSONString()
			common.OptionMapRWMutex.Lock()
			previousOptions := common.OptionMap
			common.OptionMap = make(map[string]string)
			common.OptionMapRWMutex.Unlock()
			common.SetDatabaseTypes(engine.kind, engine.kind)
			initCol()
			common.RedisEnabled = false
			DB, LOG_DB = mainDB, logDB
			t.Cleanup(func() {
				DB, LOG_DB = previousDB, previousLog
				common.SetDatabaseTypes(previousMainType, previousLogType)
				initCol()
				common.RedisEnabled = previousRedis
				vipSettings.Store(previousVIP)
				emailGroupSettings.Store(previousEmail)
				require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousGroupRatio))
				require.NoError(t, common.UpdateTopupGroupRatioByJSONString(previousTopupRatio))
				common.OptionMapRWMutex.Lock()
				common.OptionMap = previousOptions
				common.OptionMapRWMutex.Unlock()
			})
			for _, db := range []*gorm.DB{mainDB, logDB} {
				tables, err := db.Migrator().GetTables()
				require.NoError(t, err)
				require.Empty(t, tables, "use dedicated empty test databases")
				sqlDB, err := db.DB()
				require.NoError(t, err)
				t.Cleanup(func() {
					tables, err := db.Migrator().GetTables()
					require.NoError(t, err)
					for _, table := range tables {
						if !strings.HasPrefix(table, "sqlite_") {
							require.NoError(t, db.Migrator().DropTable(table))
						}
					}
					require.NoError(t, sqlDB.Close())
				})
			}
			versionSQL := "SELECT VERSION()"
			if engine.kind == common.DatabaseTypeSQLite {
				versionSQL = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, DB.Raw(versionSQL).Scan(&version).Error)
			t.Logf("database version: %s", version)

			require.NoError(t, migrateDB())
			require.NoError(t, migrateLOGDB())
			require.NoError(t, migrateDB())
			require.NoError(t, migrateLOGDB())
			assert.True(t, DB.Migrator().HasIndex(&GrowthEvent{}, "idx_growth_source_ref"))
			assert.True(t, DB.Migrator().HasIndex(&GrowthEvent{}, "idx_growth_user_time"))
			assert.True(t, DB.Migrator().HasIndex(&User{}, "VIPLevel"))

			// Recreate the pre-VIP membership schema with its existing user
			// data, and seed representative old orders before the upgrade.
			require.NoError(t, DB.Migrator().DropTable(&GrowthEvent{}, &UserGrowth{}))
			require.NoError(t, DB.Migrator().DropIndex(&User{}, "VIPLevel"))
			require.NoError(t, DB.Migrator().DropColumn(&User{}, "VIPLevel"))
			require.NoError(t, DB.Delete(&Option{}, commonKeyCol+" = ?", vipLevelBackfillKey).Error)
			user := User{Username: "student", Group: "vip0-大学生优惠", AffCode: "student"}
			require.NoError(t, DB.Omit("VIPLevel").Create(&user).Error)
			custom := User{Username: "custom", Group: "svip", AffCode: "custom"}
			require.NoError(t, DB.Omit("VIPLevel").Create(&custom).Error)
			now := common.GetTimestamp()
			orders := []TopUp{
				{UserId: user.Id, TradeNo: "historical-credit", Amount: 600, CompleteTime: now, Status: common.TopUpStatusSuccess, PaymentProvider: PaymentProviderEpay},
				{UserId: user.Id, TradeNo: "historical-subscription", Money: 2000, CompleteTime: now, Status: common.TopUpStatusSuccess, PaymentMethod: PaymentMethodStripe},
			}
			require.NoError(t, DB.Create(&orders).Error)
			require.NoError(t, DB.Create(&SubscriptionOrder{UserId: user.Id, TradeNo: "historical-subscription"}).Error)
			require.NoError(t, migrateDB())
			require.NoError(t, migrateDB())
			require.NoError(t, InitializeUserGrowth())
			require.NoError(t, InitializeUserGrowth())
			require.Error(t, DB.Create(&User{Username: user.Username, AffCode: "duplicate"}).Error)

			current, err := GetUserVIPMembership(user.Id)
			require.NoError(t, err)
			require.NotNil(t, current.VIPLevel)
			assert.Equal(t, 1, *current.VIPLevel)
			assert.Equal(t, "VIP1-大学生优惠", current.Group)
			level, err := GetUserVIPLevel(custom.Id)
			require.NoError(t, err)
			assert.Nil(t, level)
			growth, err := GetUserGrowth(user.Id)
			require.NoError(t, err)
			assert.Equal(t, 600.0, GrowthAmount(growth.Quota).InexactFloat64())
			events, total, err := GetUserGrowthEvents(user.Id, growth.WindowStart, 0, 20)
			require.NoError(t, err)
			require.Len(t, events, 1)
			assert.EqualValues(t, 1, total)
			assert.Equal(t, "historical-credit", events[0].RefId)

			order := TopUp{UserId: user.Id, TradeNo: "next", Amount: 1600, Status: common.TopUpStatusPending, PaymentProvider: PaymentProviderEpay}
			require.NoError(t, DB.Create(&order).Error)
			duplicate, err := RechargeEpay(order.TradeNo, "", "")
			require.NoError(t, err)
			assert.False(t, duplicate)
			duplicate, err = RechargeEpay(order.TradeNo, "", "")
			require.NoError(t, err)
			assert.True(t, duplicate)
			level, err = GetUserVIPLevel(user.Id)
			require.NoError(t, err)
			require.NotNil(t, level)
			assert.Equal(t, 2, *level)

			// The unique event constraint must roll back a credit even after the wallet UPDATE.
			require.Error(t, DB.Transaction(func(tx *gorm.DB) error {
				return creditTopUpQuota(tx, user.Id, 1, nil, growthTopup, order.TradeNo, now)
			}))
			require.NoError(t, DB.First(&user, user.Id).Error)
			assert.EqualValues(t, 1600*common.QuotaPerUnit, user.Quota)

			expired := CurrentVIPSettings().windowStart(now) - 1
			require.NoError(t, DB.Model(&GrowthEvent{}).Where("ref_id = ?", order.TradeNo).Update("created_at", expired).Error)
			require.NoError(t, DB.Model(&UserGrowth{}).Where("user_id = ?", user.Id).Update("next_expiry", now).Error)
			require.NoError(t, RefreshUserGrowths(true, true))
			current, err = GetUserVIPMembership(user.Id)
			require.NoError(t, err)
			require.NotNil(t, current.VIPLevel)
			assert.Equal(t, 1, *current.VIPLevel)
			assert.Equal(t, "VIP1-大学生优惠", current.Group)
			var logs int64
			require.NoError(t, LOG_DB.Model(&Log{}).Where("user_id = ? AND type = ?", user.Id, LogTypeSystem).Count(&logs).Error)
			assert.EqualValues(t, 3, logs)

			t.Run("settings_and_email", func(t *testing.T) {
				require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"VIP0":1,"VIP1":1,"VIP1-大学生优惠":1,"VIP2-大学生优惠":1}`))
				require.NoError(t, DB.Create(&Option{Key: topupRatioOption, Value: `{"大学生优惠":0.8,"VIP2-大学生优惠":0.75}`}).Error)
				req := VIPOptionUpdate{
					Tiers: CurrentVIPSettings(),
					EmailGroups: EmailGroupSettings{Enabled: true, ApplyOnBind: true, Rules: []EmailGroupRule{
						{Suffix: "edu.cn", Group: "VIP2-大学生优惠"},
						{Suffix: "staff.edu.cn", Group: "VIP1"},
					}},
					TierRatios: map[string]float64{"VIP0": 1, "VIP1": 0.99, "VIP2": 0.97, "VIP3": 0.95, "VIP4": 0.92, "VIP5": 0.9},
				}
				require.NoError(t, SaveVIPOptions(req))
				assert.Empty(t, GroupForEmail("student@fakeedu.cn"))
				assert.Empty(t, GroupForEmail("teacher@staff.edu.cn"))
				assert.Equal(t, "大学生优惠", GroupForEmail("student@Pku.Edu.Cn"))

				// Binding a matching address grants the discount at the user's current level.
				require.NoError(t, DB.Model(&user).Updates(userGroupColumns("VIP1")).Error)
				require.NoError(t, ModifyUserSetting(user.Id, func(setting *dto.UserSetting) {
					setting.Language = "zh"
					setting.Pebble = &dto.PebbleAvatar{Palette: 4}
				}))
				require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
					_, err := changeEmailGroup(tx, user.Id, "student@pku.edu.cn", true)
					return err
				}))
				require.NoError(t, DB.First(&user, user.Id).Error)
				assert.Equal(t, "VIP1-大学生优惠", user.Group)
				assert.Equal(t, "大学生优惠", user.GetSetting().EmailGroup)

				next := TopUp{UserId: user.Id, TradeNo: "student-upgrade", Amount: 1500, Status: common.TopUpStatusPending, PaymentProvider: PaymentProviderEpay}
				require.NoError(t, DB.Create(&next).Error)
				_, err := RechargeEpay(next.TradeNo, "", "")
				require.NoError(t, err)
				require.NoError(t, DB.First(&user, user.Id).Error)
				require.NotNil(t, user.VIPLevel)
				assert.Equal(t, 2, *user.VIPLevel)
				assert.Equal(t, "VIP2-大学生优惠", user.Group)
				assert.Equal(t, "大学生优惠", user.GetSetting().EmailGroup)
				require.NoError(t, user.ClearBinding("email"))
				assert.Equal(t, "VIP2", user.Group)
				assert.Empty(t, user.GetSetting().EmailGroup)
				assert.Equal(t, "zh", user.GetSetting().Language)
				require.NotNil(t, user.GetSetting().Pebble)
				assert.Equal(t, 4, user.GetSetting().Pebble.Palette)

				assert.Equal(t, 0.75, common.GetTopupGroupRatio("VIP2-大学生优惠"))
				assert.InDelta(t, 0.99*0.8, common.GetTopupGroupRatio("VIP1-大学生优惠"), 1e-10)
				req.Tiers.Tiers = append([]VipTier(nil), req.Tiers.Tiers...)
				req.Tiers.Tiers[2].MinAmount = 1
				req.TierRatios["VIP1"] = 0.5
				require.Error(t, SaveVIPOptions(req))
				assert.Equal(t, 0.99, common.GetTopupGroupRatio("VIP1"))
				assert.Equal(t, 2000.0, CurrentVIPSettings().Tiers[2].MinAmount)
			})

			rollback := errors.New("test rollback")
			order = TopUp{UserId: user.Id, TradeNo: "rolled-back", Amount: 1, Status: common.TopUpStatusPending}
			require.ErrorIs(t, DB.Transaction(func(tx *gorm.DB) error {
				if err := creditTopUpQuota(tx, user.Id, 1, nil, growthTopup, order.TradeNo, now); err != nil {
					return err
				}
				return rollback
			}), rollback)
			require.NoError(t, DB.Model(&GrowthEvent{}).Where("ref_id = ?", order.TradeNo).Count(&total).Error)
			assert.Zero(t, total)
		})
	}
}

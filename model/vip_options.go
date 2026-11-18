package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"gorm.io/gorm"
)

const vipTierOption = "VipTierSetting"
const emailGroupOption = "EmailGroupRules"
const topupRatioOption = "TopupGroupRatio"

type VipTier struct {
	Level     int     `json:"level"`
	Key       string  `json:"key"`
	Name      string  `json:"name"`
	MinAmount float64 `json:"min_amount"`
}

type VIPSettings struct {
	Enabled    bool      `json:"enabled"`
	WindowDays int       `json:"window_days"`
	Tiers      []VipTier `json:"tiers"`
	version    string
}

var defaultVIPSettings = VIPSettings{
	Enabled:    true,
	WindowDays: 365,
	Tiers: []VipTier{
		{0, "VIP0", "入门", 0},
		{1, "VIP1", "青铜", 500},
		{2, "VIP2", "白银", 2000},
		{3, "VIP3", "黄金", 5000},
		{4, "VIP4", "铂金", 20000},
		{5, "VIP5", "钻石", 50000},
	},
}

type EmailGroupRule struct {
	Suffix string `json:"suffix"`
	Group  string `json:"group"`
}

type EmailGroupSettings struct {
	Enabled     bool             `json:"enabled"`
	ApplyOnBind bool             `json:"apply_on_bind"`
	Rules       []EmailGroupRule `json:"rules"`
}

var defaultEmailGroupSettings = EmailGroupSettings{Enabled: true, ApplyOnBind: true, Rules: []EmailGroupRule{}}
var vipSettings atomic.Pointer[VIPSettings]
var emailGroupSettings atomic.Pointer[EmailGroupSettings]

func init() {
	defaultVIPSettings.version = vipSettingsVersion(defaultVIPSettings)
	vipSettings.Store(&defaultVIPSettings)
	emailGroupSettings.Store(&defaultEmailGroupSettings)
}

func CurrentVIPSettings() VIPSettings {
	return *vipSettings.Load()
}

func vipSettingsVersion(settings VIPSettings) string {
	data, _ := common.Marshal(settings)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func parseVIPSettings(raw string) (VIPSettings, error) {
	var settings VIPSettings
	if err := common.UnmarshalJsonStr(raw, &settings); err != nil {
		return settings, err
	}
	if settings.WindowDays <= 0 || int64(settings.WindowDays) > math.MaxInt32 ||
		len(settings.Tiers) != common.MaxVIPLevel+1 {
		return settings, common.NewMessage("VIP settings require a positive window and six tiers")
	}
	for level, tier := range settings.Tiers {
		if tier.Level != level || tier.Key != common.ComposeUserGroup(level, "") ||
			strings.TrimSpace(tier.Name) == "" || math.IsNaN(tier.MinAmount) || math.IsInf(tier.MinAmount, 0) ||
			(level == 0 && tier.MinAmount != 0) || (level > 0 && tier.MinAmount <= settings.Tiers[level-1].MinAmount) {
			return settings, common.NewMessage("VIP tiers must have valid names and strictly increasing thresholds starting at zero")
		}
	}
	settings.version = vipSettingsVersion(settings)
	return settings, nil
}

func parseEmailGroupSettings(raw string, requireGroups bool) (EmailGroupSettings, error) {
	var settings EmailGroupSettings
	if err := common.UnmarshalJsonStr(raw, &settings); err != nil {
		return settings, err
	}
	if strings.TrimSpace(raw) == "null" {
		return settings, common.NewMessage("Email group rules must be an object")
	}
	seen := make(map[string]bool)
	for i, rule := range settings.Rules {
		suffix := strings.ToLower(strings.TrimSpace(rule.Suffix))
		group := strings.TrimSpace(rule.Group)
		_, _, vip := common.SplitUserGroup(group)
		if suffix == "" || strings.HasPrefix(suffix, ".") || strings.ContainsAny(suffix, "@ /\\\t\r\n") ||
			seen[suffix] || group == "" || vip || group == "default" || group == "vip" || group == "svip" ||
			len([]rune(common.ComposeUserGroup(0, group))) > 64 ||
			(requireGroups && !ratio_setting.ContainsGroupRatio(group)) {
			return settings, common.NewMessage("Invalid email domain or discount group")
		}
		seen[suffix] = true
		settings.Rules[i] = EmailGroupRule{Suffix: suffix, Group: group}
	}
	return settings, nil
}

type VIPOptionUpdate struct {
	Tiers       VIPSettings        `json:"vip_tier_setting"`
	EmailGroups EmailGroupSettings `json:"email_group_rules"`
	TierRatios  map[string]float64 `json:"topup_ratios"`
}

func SaveVIPOptions(request VIPOptionUpdate) error {
	tierJSON, err := common.Marshal(request.Tiers)
	if err != nil {
		return err
	}
	if _, err = parseVIPSettings(string(tierJSON)); err != nil {
		return err
	}
	emailJSON, err := common.Marshal(request.EmailGroups)
	if err != nil {
		return err
	}
	if _, err = parseEmailGroupSettings(string(emailJSON), true); err != nil {
		return err
	}
	for key, ratio := range request.TierRatios {
		level, special, ok := common.SplitUserGroup(key)
		if !ok || special != "" || key != common.ComposeUserGroup(level, "") ||
			ratio <= 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
			return common.NewMessage("Invalid VIP top-up ratio")
		}
	}
	if len(request.TierRatios) != common.MaxVIPLevel+1 {
		return common.NewMessage("A top-up ratio is required for each VIP tier")
	}

	// Serialize with option reloads so a local reload cannot publish an older
	// snapshot after this save, and merge against the locked database row.
	requestPolicyOptionMutex.Lock()
	defer requestPolicyOptionMutex.Unlock()
	values := map[string]string{vipTierOption: string(tierJSON), emailGroupOption: string(emailJSON)}
	err = DB.Transaction(func(tx *gorm.DB) error {
		// Start with a write to acquire SQLite's writer lock before reading.
		if err := tx.Model(&Option{}).Where(commonKeyCol+" = ?", topupRatioOption).
			UpdateColumn("value", gorm.Expr("value")).Error; err != nil {
			return err
		}
		var option Option
		err := lockForUpdate(tx).Where(commonKeyCol+" = ?", topupRatioOption).Take(&option).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			option.Value = common.TopupGroupRatio2JSONString()
		} else if err != nil {
			return err
		}
		ratios, err := common.ParseTopupGroupRatios(option.Value)
		if err != nil {
			return err
		}
		for key, ratio := range request.TierRatios {
			ratios[key] = ratio
		}
		data, err := common.Marshal(ratios)
		if err != nil {
			return err
		}
		values[topupRatioOption] = string(data)
		for _, key := range []string{topupRatioOption, vipTierOption, emailGroupOption} {
			if err := tx.Save(&Option{Key: key, Value: values[key]}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, key := range []string{topupRatioOption, vipTierOption, emailGroupOption} {
		if err := updateOptionMap(key, values[key]); err != nil {
			return err
		}
	}
	BumpSyncTopic(SyncTopicOptions)
	return nil
}

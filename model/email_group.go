package model

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"gorm.io/gorm"
)

type emailGroupChange struct {
	from, to, domain string
}

func emailDomain(email string) string {
	_, domain, _ := strings.Cut(NormalizeEmail(email), "@")
	return domain
}

// A configured VIP group supplies the discount suffix. The account's level
// comes from its growth, including when a rule targets a plain VIP group.
func emailDiscountGroup(group string) string {
	if _, special, vip := common.SplitUserGroup(group); vip {
		return special
	}
	return group
}

func groupForDomain(settings *EmailGroupSettings, domain string) string {
	group := ""
	length := 0
	for _, rule := range settings.Rules {
		if len(rule.Suffix) > length &&
			(domain == rule.Suffix || strings.HasSuffix(domain, "."+rule.Suffix)) &&
			ratio_setting.ContainsGroupRatio(rule.Group) {
			group = emailDiscountGroup(rule.Group)
			length = len(rule.Suffix)
		}
	}
	return group
}

func GroupForEmail(email string) string {
	settings := emailGroupSettings.Load()
	if !settings.Enabled {
		return ""
	}
	return groupForDomain(settings, emailDomain(email))
}

func RecordEmailGroupJoin(userId int, group, email string) {
	recordEmailGroupChange(userId, emailGroupChange{to: group, domain: emailDomain(email)})
}

func changeEmailGroup(tx *gorm.DB, userId int, email string, allowApply bool) (emailGroupChange, error) {
	var user User
	if err := lockForUpdate(tx).Select("id", "group", "vip_level", "setting").First(&user, userId).Error; err != nil {
		return emailGroupChange{}, err
	}
	level, special, managed := common.SplitUserGroup(user.Group)
	settings := emailGroupSettings.Load()
	change := emailGroupChange{domain: emailDomain(email)}
	matched := groupForDomain(settings, change.domain)
	setting := user.GetSetting()
	marker := setting.EmailGroup
	if marker != "" && (!managed || marker != special) {
		// A later manual move superseded the email-granted group.
		marker = ""
	}
	if marker != "" && matched != marker {
		change.from = marker
		special, marker = "", ""
	}
	if managed && special == "" && matched != "" && settings.Enabled && settings.ApplyOnBind && allowApply {
		change.to = matched
		special, marker = matched, matched
	}
	if change.from == "" && change.to == "" && marker == setting.EmailGroup {
		return change, nil
	}
	setting.EmailGroup = marker
	settingJSON, err := mergeUserSetting(user.Setting, setting, user.GetSetting())
	if err != nil {
		return emailGroupChange{}, err
	}
	updates := map[string]any{"setting": settingJSON}
	if change.from != "" || change.to != "" {
		updates = userGroupColumns(common.ComposeUserGroup(level, special))
		updates["setting"] = settingJSON
	}
	result := tx.Model(&User{}).Where("id = ? AND "+commonGroupCol+" = ?", userId, user.Group).Updates(updates)
	if result.Error != nil {
		return emailGroupChange{}, result.Error
	}
	if result.RowsAffected == 0 {
		return emailGroupChange{}, nil
	}
	return change, nil
}

func recordEmailGroupChange(userId int, change emailGroupChange) {
	if change.from != "" {
		RecordLog(userId, LogTypeSystem, common.NewMessage("Left group {{group}} after email change", map[string]any{"group": change.from}))
	}
	if change.to != "" {
		RecordLog(userId, LogTypeSystem, common.NewMessage("Joined group {{group}} by email domain {{domain}}", map[string]any{"group": change.to, "domain": change.domain}))
	}
}

func publishEmailGroupChange(userId int, change emailGroupChange) {
	if change.from == "" && change.to == "" {
		return
	}
	if err := RefreshUserGroupCache(userId); err != nil {
		common.SysError(common.LogText("refresh email group cache for user %d: %v", userId, err))
	}
	recordEmailGroupChange(userId, change)
}

// A manual move to a different discount ends the previous email grant.
func manualUserGroupColumns(user User, group string) (map[string]any, error) {
	updates := userGroupColumns(group)
	before := user.GetSetting()
	_, special, managed := common.SplitUserGroup(group)
	if before.EmailGroup == "" || (managed && before.EmailGroup == special) {
		return updates, nil
	}
	after := before
	after.EmailGroup = ""
	setting, err := mergeUserSetting(user.Setting, after, before)
	if err != nil {
		return nil, err
	}
	updates["setting"] = setting
	return updates, nil
}

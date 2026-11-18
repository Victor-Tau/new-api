package model

import (
	"encoding/json"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"gorm.io/gorm"
)

func ModifyUserSetting(userId int, modify func(*dto.UserSetting)) error {
	err := DB.Transaction(func(tx *gorm.DB) error {
		// Acquire the writer lock before establishing a read snapshot on SQLite.
		if err := tx.Model(&User{}).Where("id = ?", userId).
			UpdateColumn("setting", gorm.Expr("setting")).Error; err != nil {
			return err
		}
		var user User
		if err := tx.Select("id", "setting").First(&user, userId).Error; err != nil {
			return err
		}
		before := user.GetSetting()
		after := before
		modify(&after)
		if before == after {
			return nil
		}
		raw, err := mergeUserSetting(user.Setting, after, before)
		if err != nil {
			return err
		}
		return tx.Model(&user).Update("setting", raw).Error
	})
	if err != nil {
		return err
	}
	return PublishUserAuthCache(userId)
}

// Merge just the changed keys so extensions in stored JSON survive a settings
// update even when they are absent from the current DTO.
func mergeUserSetting(raw string, after, before dto.UserSetting) (string, error) {
	stored := make(map[string]json.RawMessage)
	if raw != "" {
		if err := common.UnmarshalJsonStr(raw, &stored); err != nil {
			return "", err
		}
	}
	if stored == nil {
		stored = make(map[string]json.RawMessage)
	}
	prev, err := userSettingFields(before)
	if err != nil {
		return "", err
	}
	next, err := userSettingFields(after)
	if err != nil {
		return "", err
	}
	for key := range prev {
		if _, ok := next[key]; !ok {
			delete(stored, key)
		}
	}
	for key, value := range next {
		if string(prev[key]) != string(value) {
			stored[key] = value
		}
	}
	data, err := common.Marshal(stored)
	return string(data), err
}

func userSettingFields(setting dto.UserSetting) (map[string]json.RawMessage, error) {
	raw, err := common.Marshal(setting)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	err = common.Unmarshal(raw, &fields)
	return fields, err
}

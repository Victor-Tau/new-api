package common

import (
	"strings"
	"unicode/utf8"
)

const BaseVIPGroup = "VIP0"
const MaxVIPLevel = 5

// SplitUserGroup recognizes the stored membership formats. Other groups are
// owned by the administrator and do not participate in automatic tier changes.
func SplitUserGroup(group string) (level int, special string, ok bool) {
	if len(group) < 4 || !strings.EqualFold(group[:3], "VIP") || group[3] < '0' || group[3] > '0'+MaxVIPLevel {
		return 0, "", false
	}
	level = int(group[3] - '0')
	if len(group) == 4 {
		return level, "", true
	}
	r, size := utf8.DecodeRuneInString(group[4:])
	if !strings.ContainsRune("-－—_·", r) || len(group) == 4+size {
		return 0, "", false
	}
	return level, group[4+size:], true
}

func ComposeUserGroup(level int, special string) string {
	group := "VIP" + string(rune('0'+level))
	if special != "" {
		group += "-" + special
	}
	return group
}

func VIPLevelForGroup(group string) *int {
	level, _, ok := SplitUserGroup(group)
	if !ok {
		return nil
	}
	return &level
}

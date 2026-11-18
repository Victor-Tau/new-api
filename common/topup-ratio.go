package common

import (
	"errors"
	"maps"
	"math"
	"sync"
)

var defaultTopupGroupRatio = map[string]float64{
	BaseVIPGroup: 1,
	"VIP1":       0.99,
	"VIP2":       0.97,
	"VIP3":       0.95,
	"VIP4":       0.92,
	"VIP5":       0.90,
	"vip":        1,
	"svip":       1,
}

var topupGroupRatio = maps.Clone(defaultTopupGroupRatio)
var topupGroupRatioMutex sync.RWMutex

func TopupGroupRatio2JSONString() string {
	topupGroupRatioMutex.RLock()
	defer topupGroupRatioMutex.RUnlock()
	jsonBytes, err := Marshal(topupGroupRatio)
	if err != nil {
		SysError(LogText("error marshalling topup group ratio: %s", err.Error()))
	}
	return string(jsonBytes)
}

func ParseTopupGroupRatios(raw string) (map[string]float64, error) {
	var ratios map[string]float64
	if err := UnmarshalJsonStr(raw, &ratios); err != nil {
		return nil, err
	}
	if ratios == nil {
		return nil, errors.New("top-up ratios must be an object")
	}
	for _, ratio := range ratios {
		if ratio < 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
			return nil, errors.New("top-up ratios must be finite and non-negative")
		}
	}
	// Canonicalize tier-only keys from existing option values.
	for key, ratio := range ratios {
		if level, special, ok := SplitUserGroup(key); ok && special == "" {
			canonical := ComposeUserGroup(level, "")
			if _, exists := ratios[canonical]; !exists {
				ratios[canonical] = ratio
			}
			if canonical != key {
				delete(ratios, key)
			}
		}
	}
	for level := 0; level <= MaxVIPLevel; level++ {
		key := ComposeUserGroup(level, "")
		if _, ok := ratios[key]; !ok {
			ratios[key] = defaultTopupGroupRatio[key]
		}
	}
	return ratios, nil
}

func UpdateTopupGroupRatioByJSONString(raw string) error {
	ratios, err := ParseTopupGroupRatios(raw)
	if err != nil {
		return err
	}
	topupGroupRatioMutex.Lock()
	topupGroupRatio = ratios
	topupGroupRatioMutex.Unlock()
	return nil
}

func GetTopupGroupRatio(name string) float64 {
	return GetUserTopupGroupRatio(name, VIPLevelForGroup(name))
}

// GetUserTopupGroupRatio uses the persisted tier for user quotes.
func GetUserTopupGroupRatio(group string, level *int) float64 {
	topupGroupRatioMutex.RLock()
	defer topupGroupRatioMutex.RUnlock()
	if ratio, ok := topupGroupRatio[group]; ok {
		if ratio == 0 {
			return 1
		}
		return ratio
	}
	if level == nil {
		return 1
	}
	ratio := topupGroupRatio[ComposeUserGroup(*level, "")]
	if ratio == 0 {
		ratio = 1
	}
	_, special, _ := SplitUserGroup(group)
	if specialRatio, ok := topupGroupRatio[special]; special != "" && ok {
		combined := ratio * specialRatio
		if combined > 0 && !math.IsInf(combined, 0) {
			ratio = combined
		}
	}
	return ratio
}

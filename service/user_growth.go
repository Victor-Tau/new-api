package service

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

func StartUserGrowthMaintenance() {
	if !common.IsMasterNode {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		nextExpirySweep := time.Time{}
		for now := range ticker.C {
			includeExpiry := !now.Before(nextExpirySweep)
			if err := model.RefreshUserGrowths(includeExpiry, false); err != nil {
				common.SysError(common.LogText("maintain membership growth: %v", err))
			} else if includeExpiry {
				nextExpirySweep = now.Add(24 * time.Hour)
			}
		}
	}()
}

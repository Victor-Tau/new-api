package controller

import (
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func GetSelfGrowth(c *gin.Context) {
	userId := c.GetInt("id")
	user, err := model.GetUserVIPMembership(userId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	growth, err := model.GetUserGrowth(userId)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	page := common.GetPageQuery(c)
	if page.Page < 1 || page.PageSize < 1 || page.Page > math.MaxInt/page.PageSize {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	events, total, err := model.GetUserGrowthEvents(userId, growth.WindowStart, page.GetStartIdx(), page.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	items := make([]gin.H, 0, len(events))
	for _, event := range events {
		items = append(items, gin.H{
			"id": event.Id, "quota": event.Quota, "amount": model.GrowthAmount(event.Quota).InexactFloat64(),
			"source": event.Source, "created_at": event.CreatedAt,
		})
	}

	var next gin.H
	settings := model.CurrentVIPSettings()
	if user.VIPLevel != nil && *user.VIPLevel >= 0 && *user.VIPLevel+1 < len(settings.Tiers) {
		tier := settings.Tiers[*user.VIPLevel+1]
		next = gin.H{"level": tier.Level, "min_amount": tier.MinAmount}
	}
	common.ApiSuccess(c, gin.H{
		"window_days": growth.WindowDays,
		"amount":      model.GrowthAmount(growth.Quota).InexactFloat64(),
		"level":       user.VIPLevel,
		"next":        next,
		"records":     gin.H{"items": items, "total": total},
	})
}

func UpdateVIPOptions(c *gin.Context) {
	var request model.VIPOptionUpdate
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	if err := model.SaveVIPOptions(request); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccessT(c, "Settings updated", nil)
}

func vipStatus() (int, []gin.H) {
	settings := model.CurrentVIPSettings()
	tiers := make([]gin.H, 0, len(settings.Tiers))
	for _, tier := range settings.Tiers {
		tiers = append(tiers, gin.H{
			"level": tier.Level, "key": tier.Key, "name": tier.Name, "min_amount": tier.MinAmount,
			"topup_ratio": common.GetTopupGroupRatio(tier.Key),
		})
	}
	return settings.WindowDays, tiers
}

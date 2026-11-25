package controller

import (
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type adminUserGrowthDetails struct {
	*model.User
	GrowthAmount float64 `json:"growth_amount"`
}

func adminUsersWithGrowth(users []*model.User) ([]adminUserGrowthDetails, error) {
	ids := make([]int, 0, len(users))
	for _, user := range users {
		ids = append(ids, user.Id)
	}
	amounts, err := model.GetUserGrowthAmounts(ids)
	if err != nil {
		return nil, err
	}
	items := make([]adminUserGrowthDetails, 0, len(users))
	for _, user := range users {
		items = append(items, adminUserGrowthDetails{User: user, GrowthAmount: amounts[user.Id]})
	}
	return items, nil
}

func AdjustAdminUserGrowth(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("id"))
	var request struct {
		Amount *float64 `json:"amount"`
	}
	if err != nil || userID <= 0 || common.DecodeJson(c.Request.Body, &request) != nil || request.Amount == nil {
		writeAccountStatusError(c, http.StatusBadRequest, "Invalid parameters")
		return
	}
	adjustment, err := model.AdjustUserGrowth(userID, c.GetInt("role"), decimal.NewFromFloat(*request.Amount))
	switch {
	case errors.Is(err, model.ErrInvalidGrowthAmount):
		writeAccountStatusError(c, http.StatusBadRequest, "Invalid growth points")
	case errors.Is(err, model.ErrUserGrowthPermission):
		writeAccountStatusError(c, http.StatusForbidden, "No permission to update users of same or higher permission level")
	case errors.Is(err, gorm.ErrRecordNotFound):
		writeAccountStatusError(c, http.StatusNotFound, "User does not exist")
	case err != nil:
		common.ApiError(c, err)
	default:
		recordManageAuditFor(c, userID, "user.growth_adjust", map[string]any{
			"id": userID, "username": adjustment.Username, "from": adjustment.Before, "to": adjustment.Amount,
		})
		common.ApiSuccess(c, gin.H{"growth_amount": adjustment.Amount, "group": adjustment.Group})
	}
}

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

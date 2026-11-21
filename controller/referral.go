package controller

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type referralPermissions struct {
	InvitationEnabled bool `json:"invitation_enabled"`
	RebateEnabled     bool `json:"rebate_enabled"`
	RateBPS           *int `json:"rate_bps,omitempty"`
}

func GetSelfReferralPermissions(c *gin.Context) {
	user, err := model.GetUserById(c.GetInt("id"), false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	permissions := referralPermissions{
		InvitationEnabled: !user.InvitationDisabled,
		RebateEnabled:     user.ReferralRebateEnabled,
	}
	if permissions.RebateEnabled {
		rate := user.EffectiveReferralRebateRate()
		permissions.RateBPS = &rate
	}
	common.ApiSuccess(c, permissions)
}

func GetReferralInvitees(c *gin.Context) {
	user, err := model.GetUserById(c.GetInt("id"), false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if user.InvitationDisabled {
		writeAccountStatusError(c, http.StatusForbidden, "Invitation access is disabled")
		return
	}
	page := referralPage(c)
	items, total, err := model.ListReferralInvitees(c.Request.Context(), user.Id, page)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	page.SetTotal(int(total))
	page.SetItems(items)
	common.ApiSuccess(c, page)
}

func GetReferralRebates(c *gin.Context) {
	user, err := model.GetUserById(c.GetInt("id"), false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !user.ReferralRebateEnabled {
		writeAccountStatusError(c, http.StatusForbidden, "Rebate access is disabled")
		return
	}
	page := referralPage(c)
	items, total, err := model.ListReferralRebates(c.Request.Context(), user.Id, page)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	page.SetTotal(int(total))
	page.SetItems(items)
	common.ApiSuccess(c, page)
}

func referralPage(c *gin.Context) *common.PageInfo {
	page := common.GetPageQuery(c)
	page.Page = max(1, min(page.Page, 1_000_000))
	page.PageSize = max(1, page.PageSize)
	return page
}

func referralUserForAdmin(c *gin.Context) *model.User {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		writeAccountStatusError(c, http.StatusBadRequest, "Invalid parameters")
		return nil
	}
	user, err := model.GetUserById(id, false)
	if err != nil {
		common.ApiError(c, err)
		return nil
	}
	if !canManageTargetRole(c.GetInt("role"), user.Role) {
		writeAccountStatusError(c, http.StatusForbidden, "No permission to access users of same or higher level")
		return nil
	}
	return user
}

func GetAdminReferralPermissions(c *gin.Context) {
	user := referralUserForAdmin(c)
	if user == nil {
		return
	}
	common.ApiSuccess(c, referralPermissions{
		InvitationEnabled: !user.InvitationDisabled,
		RebateEnabled:     user.ReferralRebateEnabled,
		RateBPS:           user.ReferralRebateRate,
	})
}

func UpdateAdminReferralPermissions(c *gin.Context) {
	user := referralUserForAdmin(c)
	if user == nil {
		return
	}
	var req struct {
		InvitationEnabled *bool `json:"invitation_enabled"`
		RebateEnabled     *bool `json:"rebate_enabled"`
		RateBPS           *int  `json:"rate_bps"`
	}
	if err := common.DecodeJson(c.Request.Body, &req); err != nil || req.InvitationEnabled == nil || req.RebateEnabled == nil {
		writeAccountStatusError(c, http.StatusBadRequest, "Invalid parameters")
		return
	}
	if req.RateBPS != nil && (*req.RateBPS < 0 || *req.RateBPS > 10_000) {
		common.ApiErrorStatus(c, http.StatusBadRequest, model.ErrInvalidReferralRate)
		return
	}
	if *req.RebateEnabled && !user.ReferralRebateEnabled && !requirePaymentCompliance(c) {
		return
	}
	proof := requireAdminUserProof(c, service.VerificationScopeAdminUserReferral, service.AdminUserContext{UserID: user.Id})
	if proof == nil {
		return
	}

	// Include the checked target role in the write to reject a concurrent
	// role change. Map updates preserve off and clear-to-default values.
	result := model.DB.WithContext(c.Request.Context()).Model(&model.User{}).
		Where("id = ? AND role = ?", user.Id, user.Role).Updates(map[string]any{
		"invitation_disabled":     !*req.InvitationEnabled,
		"referral_rebate_enabled": *req.RebateEnabled,
		"referral_rebate_rate":    req.RateBPS,
	})
	if result.Error != nil {
		common.ApiError(c, result.Error)
		return
	}
	if result.RowsAffected == 0 {
		current, err := model.GetUserById(user.Id, false)
		if err != nil || current.Role != user.Role {
			writeAccountStatusError(c, http.StatusForbidden, "No permission to access users of same or higher level")
			return
		}
	}
	recordManageAuditFor(c, user.Id, "user.referral_update", map[string]any{
		"id": user.Id, "username": user.Username,
		"invitation_enabled": *req.InvitationEnabled, "rebate_enabled": *req.RebateEnabled,
		"rate_bps": req.RateBPS, "verification_method": proof.Method,
	})
	common.ApiSuccessT(c, "Update successful", nil)
}

func UpdateReferralSettings(c *gin.Context) {
	var req struct {
		RateBPS       *int   `json:"rate_bps"`
		ExchangeRates string `json:"exchange_rates"`
	}
	if err := common.DecodeJson(c.Request.Body, &req); err != nil || req.RateBPS == nil {
		writeAccountStatusError(c, http.StatusBadRequest, "Invalid parameters")
		return
	}
	if *req.RateBPS > 0 && !requirePaymentCompliance(c) {
		return
	}
	if err := model.UpdateOptionsBulk(map[string]string{
		model.ReferralDefaultRateOption:   strconv.Itoa(*req.RateBPS),
		model.ReferralExchangeRatesOption: req.ExchangeRates,
	}); err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "option.update", map[string]any{"key": model.ReferralDefaultRateOption, "rate_bps": *req.RateBPS})
	common.ApiSuccessT(c, "Setting updated successfully", nil)
}

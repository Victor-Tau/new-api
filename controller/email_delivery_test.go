package controller

import (
	"errors"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type emailHTTPTransport func(*http.Request) (*http.Response, error)

func (transport emailHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func configureEmailDelivery(t *testing.T, options map[string]string) {
	t.Helper()
	common.OptionMapRWMutex.Lock()
	original := common.OptionMap
	common.OptionMap = options
	common.OptionMapRWMutex.Unlock()
	originalTransport := http.DefaultTransport
	originalFrom, originalAccount, originalServer := common.SMTPFrom, common.SMTPAccount, common.SMTPServer
	common.SMTPFrom, common.SMTPAccount, common.SMTPServer = "", "", ""
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = original
		common.OptionMapRWMutex.Unlock()
		http.DefaultTransport = originalTransport
		common.SMTPFrom, common.SMTPAccount, common.SMTPServer = originalFrom, originalAccount, originalServer
	})
}

func TestResendDeliversThroughSelectedProvider(t *testing.T) {
	configureEmailDelivery(t, map[string]string{"EmailProvider": "resend", "ResendApiKey": "test-key", "ResendFrom": "New API <sender@example.com>"})
	calls := 0
	http.DefaultTransport = emailHTTPTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		assert.Equal(t, "https://api.resend.com/emails", request.URL.String())
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "Bearer test-key", request.Header.Get("Authorization"))
		assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
		_, hasDeadline := request.Context().Deadline()
		assert.True(t, hasDeadline, "email delivery must have a bounded wait")
		var body struct {
			From    string   `json:"from"`
			To      []string `json:"to"`
			Subject string   `json:"subject"`
			HTML    string   `json:"html"`
		}
		require.NoError(t, common.DecodeJson(request.Body, &body))
		assert.Equal(t, `"New API" <sender@example.com>`, body.From)
		assert.Equal(t, []string{"one@example.com", "two@example.com"}, body.To)
		assert.Equal(t, "验证码", body.Subject)
		assert.Equal(t, "<p>123456</p>", body.HTML)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"email-test-id"}`)), Header: make(http.Header)}, nil
	})
	require.NoError(t, common.SendEmail("验证码", "one@example.com; two@example.com", "<p>123456</p>"))
	assert.Equal(t, 1, calls, "no SMTP configuration is needed and no duplicate request is made")
}

func TestResendFailuresDoNotRetryFallbackOrExposeSensitiveContent(t *testing.T) {
	for _, status := range []int{302, 400, 401, 403, 429, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			configureEmailDelivery(t, map[string]string{"EmailProvider": "resend", "ResendApiKey": "test-key", "ResendFrom": "sender@example.com"})
			calls := 0
			http.DefaultTransport = emailHTTPTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{"https://untrusted.example/emails"}}, Body: io.NopCloser(strings.NewReader(`{"message":"test-key 123456 reset-token"}`))}, nil
			})
			err := common.SendEmail("verification", "one@example.com", "123456 reset-token")
			require.EqualError(t, err, "Resend email delivery failed (HTTP "+strconv.Itoa(status)+")")
			assert.Equal(t, 1, calls, "do not retry or follow redirects")
		})
	}
}

func TestResendRejectsIncompleteOrInvalidConfigurationBeforeSending(t *testing.T) {
	tests := []struct{ name, key, from, receiver string }{
		{"missing key", "", "sender@example.com", "one@example.com"},
		{"header injection", "key\r\nInjected: value", "sender@example.com", "one@example.com"},
		{"missing sender", "test-key", "", "one@example.com"},
		{"invalid sender", "test-key", "bad-address", "one@example.com"},
		{"invalid recipient", "test-key", "sender@example.com", "bad-address"},
		{"empty recipient", "test-key", "sender@example.com", ""},
		{"too many recipients", "test-key", "sender@example.com", strings.Repeat("one@example.com;", 50) + "two@example.com"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configureEmailDelivery(t, map[string]string{"EmailProvider": "resend", "ResendApiKey": test.key, "ResendFrom": test.from})
			http.DefaultTransport = emailHTTPTransport(func(*http.Request) (*http.Response, error) {
				t.Fatal("invalid settings must not make a request")
				return nil, errors.New("unexpected request")
			})
			err := common.SendEmail("verification", test.receiver, "123456")
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "123456")
			assert.NotContains(t, err.Error(), "test-key")
		})
	}
}

func TestResendRejectsMalformedSuccessAndSanitizesTransportErrors(t *testing.T) {
	for _, body := range []string{`{}`, `not json`, `{"id":false}`, `{"id":""}`} {
		t.Run(body, func(t *testing.T) {
			configureEmailDelivery(t, map[string]string{"EmailProvider": "resend", "ResendApiKey": "test-key", "ResendFrom": "sender@example.com"})
			http.DefaultTransport = emailHTTPTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			require.EqualError(t, common.SendEmail("verification", "one@example.com", "123456"), "Invalid Resend delivery response")
		})
	}
	configureEmailDelivery(t, map[string]string{"EmailProvider": "resend", "ResendApiKey": "test-key", "ResendFrom": "sender@example.com"})
	http.DefaultTransport = emailHTTPTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("test-key 123456 reset-token") })
	require.EqualError(t, common.SendEmail("verification", "one@example.com", "123456"), "Resend request failed")
}

func TestSMTPAndLegacyConfigurationNeverUseResend(t *testing.T) {
	for _, provider := range []string{"", "smtp", "invalid"} {
		t.Run("provider="+provider, func(t *testing.T) {
			configureEmailDelivery(t, map[string]string{"EmailProvider": provider, "ResendApiKey": "test-key", "ResendFrom": "sender@example.com"})
			http.DefaultTransport = emailHTTPTransport(func(*http.Request) (*http.Response, error) {
				t.Fatal("SMTP and unknown providers must not invoke Resend")
				return nil, errors.New("unexpected request")
			})
			require.Error(t, common.SendEmail("verification", "one@example.com", "123456"))
		})
	}
}

func TestEmailOptionsNeverReturnResendCredentials(t *testing.T) {
	configureEmailDelivery(t, map[string]string{"EmailProvider": "resend", "ResendApiKey": "private-email-credential", "ResendFrom": "sender@example.com"})
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/option/", nil)
	GetOptions(context)
	require.Equal(t, http.StatusOK, response.Code)
	assert.NotContains(t, response.Body.String(), "private-email-credential")
	assert.NotContains(t, response.Body.String(), "ResendApiKey")
	assert.Contains(t, response.Body.String(), "ResendFrom")
	assert.Contains(t, response.Body.String(), `"key":"ResendKeyConfigured","value":"true"`)
}

func TestEmailProviderOptionRejectsInvalidValues(t *testing.T) {
	for _, provider := range []string{"", "both", "SMTP", "smtp,resend"} {
		t.Run(provider, func(t *testing.T) {
			configureEmailDelivery(t, map[string]string{"EmailProvider": "smtp"})
			body, err := common.Marshal(OptionUpdateRequest{Key: "EmailProvider", Value: provider})
			require.NoError(t, err)
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Request = httptest.NewRequest(http.MethodPut, "/api/option/", strings.NewReader(string(body)))
			UpdateOption(context)
			assert.Contains(t, response.Body.String(), `"success":false`)
			assert.Contains(t, response.Body.String(), `"message_key":"Email sending method must be SMTP or Resend"`)
			assert.Equal(t, "smtp", common.OptionMap["EmailProvider"])
		})
	}
}

func TestEmailProviderCannotActivateIncompleteResend(t *testing.T) {
	for name, settings := range map[string]map[string]string{
		"missing key":    {"ResendFrom": "sender@example.com"},
		"missing sender": {"ResendApiKey": "test-key"},
		"invalid sender": {"ResendApiKey": "test-key", "ResendFrom": "invalid"},
		"invalid key":    {"ResendApiKey": "key\r\nInjected: value", "ResendFrom": "sender@example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			settings["EmailProvider"] = "smtp"
			configureEmailDelivery(t, settings)
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Request = httptest.NewRequest(http.MethodPut, "/api/option/", strings.NewReader(`{"key":"EmailProvider","value":"resend"}`))
			require.NotPanics(t, func() { UpdateOption(context) }, "invalid activation must fail before accessing storage")
			assert.Contains(t, response.Body.String(), `"success":false`)
			assert.Contains(t, response.Body.String(), `"message_key":"Resend `)
			assert.Equal(t, "smtp", common.OptionMap["EmailProvider"])
			assert.NotContains(t, response.Body.String(), "test-key")
		})
	}
}

func TestResendSettingsChangesKeepActiveDeliveryWorking(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	previousDB, previousRedis := model.DB, common.RedisEnabled
	model.DB, common.RedisEnabled = db, false
	t.Cleanup(func() { model.DB, common.RedisEnabled = previousDB, previousRedis })

	tests := []struct {
		name, provider, key, value string
		saved                      bool
	}{
		{"active sender the parser rejects", "resend", "ResendFrom", "notify..team@example.com", false},
		{"active key cleared", "resend", "ResendApiKey", " ", false},
		{"active sender renamed", "resend", "ResendFrom", `"New API" <notify@example.com>`, true},
		{"inactive sender saved before its key", "smtp", "ResendFrom", "notify@example.com", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := map[string]string{"EmailProvider": test.provider, "ResendApiKey": "test-key", "ResendFrom": "sender@example.com"}
			if test.provider == "smtp" {
				settings["ResendApiKey"] = ""
			}
			configureEmailDelivery(t, settings)
			before := settings[test.key]
			body, err := common.Marshal(OptionUpdateRequest{Key: test.key, Value: test.value})
			require.NoError(t, err)
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Request = httptest.NewRequest(http.MethodPut, "/api/option/", strings.NewReader(string(body)))
			UpdateOption(context)
			if test.saved {
				assert.Contains(t, response.Body.String(), `"success":true`)
				assert.Equal(t, test.value, common.OptionMap[test.key])
				return
			}
			assert.Contains(t, response.Body.String(), `"message_key":"Resend `)
			assert.Equal(t, before, common.OptionMap[test.key])
		})
	}
}

func TestEmailKeyStatusIsDerivedAndReadOnly(t *testing.T) {
	configureEmailDelivery(t, map[string]string{"EmailProvider": "smtp", "ResendKeyConfigured": "true"})
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/option/", nil)
	GetOptions(context)
	assert.Contains(t, response.Body.String(), `"key":"ResendKeyConfigured","value":"false"`)
	assert.NotContains(t, response.Body.String(), `"value":"true"`)

	response = httptest.NewRecorder()
	context, _ = gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodPut, "/api/option/", strings.NewReader(`{"key":"ResendKeyConfigured","value":true}`))
	UpdateOption(context)
	assert.Contains(t, response.Body.String(), `"success":false`)
	assert.Contains(t, response.Body.String(), "read-only")
}

func TestRegistrationEmailDomainWhitelist(t *testing.T) {
	setupManageUserTestDB(t)
	configureEmailDelivery(t, map[string]string{
		"EmailProvider": "resend", "ResendApiKey": "test-key", "ResendFrom": "sender@example.com",
	})
	previousWhitelist := common.EmailDomainWhitelist
	previousDomainRestriction, previousAliasRestriction := common.EmailDomainRestrictionEnabled, common.EmailAliasRestrictionEnabled
	previousVerification, previousRegister, previousPasswordRegister := common.EmailVerificationEnabled, common.RegisterEnabled, common.PasswordRegisterEnabled
	t.Cleanup(func() {
		common.EmailDomainWhitelist = previousWhitelist
		common.EmailDomainRestrictionEnabled, common.EmailAliasRestrictionEnabled = previousDomainRestriction, previousAliasRestriction
		common.EmailVerificationEnabled, common.RegisterEnabled, common.PasswordRegisterEnabled = previousVerification, previousRegister, previousPasswordRegister
	})
	common.EmailVerificationEnabled, common.RegisterEnabled, common.PasswordRegisterEnabled = true, true, true
	var sent int
	http.DefaultTransport = emailHTTPTransport(func(*http.Request) (*http.Response, error) {
		sent++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"test"}`)), Header: make(http.Header)}, nil
	})
	router := gin.New()
	router.GET("/api/verification", SendEmailVerification)
	router.POST("/api/user/register", Register)

	for _, tc := range []struct {
		name, rule, email                  string
		allow, blockAlias, disableRestrict bool
	}{
		{name: "plain rule matches its own domain", rule: "edu.cn", email: "user@edu.cn", allow: true},
		{name: "plain rule keeps exact matching", rule: "edu.cn", email: "user@thu.edu.cn"},
		{name: "wildcard matches a subdomain", rule: "@*.edu.cn", email: "USER@THU.EDU.CN", allow: true},
		{name: "wildcard matches nested subdomains and ignores rule casing and spaces", rule: " *.Edu.Cn ", email: "user@mail.thu.edu.cn", allow: true},
		{name: "wildcard excludes the root domain", rule: "@*.edu.cn", email: "user@edu.cn"},
		{name: "wildcard requires a dot boundary", rule: "@*.edu.cn", email: "user@notedu.cn"},
		{name: "wildcard rejects suffix extension", rule: "@*.edu.cn", email: "user@thu.edu.cn.example"},
		{name: "bare star cannot allow arbitrary domains", rule: "@*", email: "user@example.com"},
		{name: "alias restrictions still apply", rule: "@*.edu.cn", email: "user+alias@thu.edu.cn", blockAlias: true},
		{name: "disabled restriction ignores the rules", rule: "@*.edu.cn", email: "user@example.com", allow: true, disableRestrict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			common.EmailDomainWhitelist = []string{"example.org", tc.rule}
			common.EmailDomainRestrictionEnabled = !tc.disableRestrict
			common.EmailAliasRestrictionEnabled = tc.blockAlias
			defer common.DeleteKey(model.NormalizeEmail(tc.email), common.EmailVerificationPurpose)
			before := sent
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/verification?email="+url.QueryEscape(tc.email), nil))
			require.Equal(t, http.StatusOK, response.Code)
			if tc.allow {
				assert.Contains(t, response.Body.String(), `"success":true`)
				assert.Equal(t, before+1, sent)
				return
			}
			assert.Contains(t, response.Body.String(), `"code":"EMAIL_ADDRESS_REJECTED"`)
			assert.Equal(t, before, sent)
		})
	}

	const email, code = "student@thu.edu.cn", "012345"
	common.RegisterVerificationCodeWithKey(email, code, common.EmailVerificationPurpose)
	t.Cleanup(func() { common.DeleteKey(email, common.EmailVerificationPurpose) })
	register := func(code string) string {
		body, err := common.Marshal(model.User{
			Username: "new-student", Email: email, Password: "test-password", VerificationCode: code,
		})
		require.NoError(t, err)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/user/register", strings.NewReader(string(body))))
		require.Equal(t, http.StatusOK, response.Code)
		return response.Body.String()
	}
	common.EmailDomainRestrictionEnabled = true
	common.EmailDomainWhitelist = []string{"edu.cn"}
	assert.Contains(t, register(code), `"code":"EMAIL_ADDRESS_REJECTED"`, "registration must enforce the current policy")
	common.EmailDomainWhitelist = []string{"@*.edu.cn"}
	assert.Contains(t, register("wrong"), "Verification code is incorrect or has expired")
	assert.Contains(t, register(code), `"success":true`)
}

func TestAuthEmailTemplatesThroughDelivery(t *testing.T) {
	db := setupManageUserTestDB(t)
	configureEmailDelivery(t, map[string]string{
		"EmailProvider": "resend", "ResendApiKey": "test-key", "ResendFrom": "sender@example.com",
	})
	previousAddress, previousName, previousMinutes := system_setting.ServerAddress, common.SystemName, common.VerificationValidMinutes
	system_setting.ServerAddress, common.SystemName, common.VerificationValidMinutes = "https://dashboard.example.com/", "Site & <Brand>", 7
	t.Cleanup(func() {
		system_setting.ServerAddress, common.SystemName, common.VerificationValidMinutes = previousAddress, previousName, previousMinutes
	})

	type sentEmail struct {
		To      []string `json:"to"`
		Subject string   `json:"subject"`
		HTML    string   `json:"html"`
	}
	var sent []sentEmail
	http.DefaultTransport = emailHTTPTransport(func(request *http.Request) (*http.Response, error) {
		var message sentEmail
		require.NoError(t, common.DecodeJson(request.Body, &message))
		sent = append(sent, message)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"test"}`))}, nil
	})
	router := gin.New()
	router.GET("/api/verification", SendEmailVerification)
	router.GET("/api/reset_password", SendPasswordResetEmail)
	router.POST("/api/user/reset", ResetPassword)
	requestEmail := func(path, email string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "https://untrusted.example"+path+"?email="+url.QueryEscape(email), nil)
		request.Header.Set("Accept-Language", "zh-CN")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assert.Equal(t, http.StatusOK, response.Code)
		assert.Contains(t, response.Body.String(), `"success":true`)
		return response
	}

	const registrationEmail = "register@example.com"
	t.Cleanup(func() { common.DeleteKey(registrationEmail, common.EmailVerificationPurpose) })
	requestEmail("/api/verification", registrationEmail)
	require.Len(t, sent, 1)
	assert.Equal(t, []string{registrationEmail}, sent[0].To)
	assert.Equal(t, "验证你的邮箱 · Site & <Brand>", sent[0].Subject)
	assert.Contains(t, sent[0].HTML, `<html lang="zh-CN">`)
	assert.Contains(t, sent[0].HTML, "欢迎加入 Site &amp; &lt;Brand&gt;")
	assert.Contains(t, sent[0].HTML, "7 分钟内有效")
	assert.Contains(t, sent[0].HTML, `src="https://dashboard.example.com/email-assets/email-register.png"`)
	assert.NotContains(t, sent[0].HTML, "<svg")
	cells := regexp.MustCompile(`>([0-9a-f])</td>`).FindAllStringSubmatch(sent[0].HTML, -1)
	require.Len(t, cells, 6)
	var code strings.Builder
	for _, cell := range cells {
		code.WriteString(cell[1])
	}
	assert.True(t, common.VerifyCodeWithKey(registrationEmail, code.String(), common.EmailVerificationPurpose))
	assert.False(t, common.VerifyCodeWithKey(registrationEmail, code.String(), common.PasswordResetPurpose))

	const resetEmail = "reset+tag@example.com"
	t.Cleanup(func() { common.DeleteKey(resetEmail, common.PasswordResetPurpose) })
	user := model.User{Username: "reset-user", Email: resetEmail}
	user.SetSetting(dto.UserSetting{Language: "zhTW"})
	require.NoError(t, db.Create(&user).Error)
	known := requestEmail("/api/reset_password", resetEmail)
	unknown := requestEmail("/api/reset_password", "unknown@example.com")
	assert.Equal(t, known.Body.String(), unknown.Body.String())
	require.Len(t, sent, 2)
	assert.Equal(t, []string{resetEmail}, sent[1].To)
	assert.Equal(t, "重設你的密碼 · Site & <Brand>", sent[1].Subject)
	assert.Contains(t, sent[1].HTML, `<html lang="zh-TW">`)
	assert.Contains(t, sent[1].HTML, "7 分鐘內有效")
	assert.Contains(t, html.UnescapeString(sent[1].HTML), `>reset+tag@example.com</strong>`)
	assert.Contains(t, sent[1].HTML, `src="https://dashboard.example.com/email-assets/email-reset-password.png"`)
	assert.NotContains(t, sent[1].HTML, "untrusted.example")
	assert.NotContains(t, sent[1].HTML, "<svg")
	href := regexp.MustCompile(`href="([^"]+)"`).FindStringSubmatch(sent[1].HTML)
	require.Len(t, href, 2)
	assert.Equal(t, 2, strings.Count(sent[1].HTML, href[1]), "button and backup URL must match")
	link, err := url.Parse(html.UnescapeString(href[1]))
	require.NoError(t, err)
	assert.Equal(t, "https", link.Scheme)
	assert.Equal(t, "dashboard.example.com", link.Host)
	assert.Equal(t, "/user/reset", link.Path)
	assert.Equal(t, resetEmail, link.Query().Get("email"))
	token := link.Query().Get("token")
	require.NotEmpty(t, token)
	assert.NotContains(t, sent[1].Subject, token)
	body, err := common.Marshal(PasswordResetRequest{Email: resetEmail, Token: token})
	require.NoError(t, err)
	reset := func() string {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/user/reset", strings.NewReader(string(body))))
		return response.Body.String()
	}
	common.VerificationValidMinutes = 0
	assert.Contains(t, reset(), `"success":false`, "expired reset links must fail")
	common.VerificationValidMinutes = 7
	assert.Contains(t, reset(), `"success":true`)
	assert.Contains(t, reset(), `"success":false`, "used reset links must fail")

	system_setting.ServerAddress = "javascript:invalid"
	failed := requestEmail("/api/reset_password", resetEmail)
	assert.Equal(t, unknown.Body.String(), failed.Body.String())
	assert.Len(t, sent, 2, "invalid site URL must prevent sending a reset link")
}

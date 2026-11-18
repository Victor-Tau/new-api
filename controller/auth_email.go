package controller

import (
	"embed"
	"errors"
	"html/template"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/setting/system_setting"
	webassets "github.com/QuantumNous/new-api/web"
)

//go:embed email_templates/*.html
var authEmailFS embed.FS

type authEmailData struct {
	Language string
	Subject  string
	Params   map[string]any
	Digits   []string
	Link     string
	ImageURL template.URL
	Year     int
}

var authEmailTemplates = template.Must(template.New("auth-email").Option("missingkey=error").Funcs(template.FuncMap{
	"t": func(data authEmailData, key string) string {
		return i18n.Translate(data.Language, key, data.Params)
	},
}).ParseFS(authEmailFS, "email_templates/*.html"))

type authEmail struct {
	subject string
	html    string
	image   common.EmailInlinePNG
}

func renderAuthEmail(name, subjectKey, lang string, data authEmailData) (authEmail, error) {
	if lang == "" {
		lang = common.DefaultLanguage
	}
	data.Language = i18n.ParseAcceptLanguage(lang)
	data.Params["SystemName"] = common.SystemName
	data.Params["Minutes"] = common.VerificationValidMinutes
	data.Subject = i18n.Translate(data.Language, subjectKey, data.Params)
	data.Year = time.Now().Year()
	filename := strings.TrimSuffix(name, ".html") + ".png"
	image, err := webassets.EmailIllustration(filename)
	if err != nil {
		return authEmail{}, errors.New("failed to load authentication email illustration")
	}
	contentID := common.GetUUID() + "@new-api"
	data.ImageURL = template.URL("cid:" + contentID) // The ID is generated locally, without user input.

	var body strings.Builder
	if err := authEmailTemplates.ExecuteTemplate(&body, name, data); err != nil {
		return authEmail{}, errors.New("failed to render authentication email")
	}
	return authEmail{subject: data.Subject, html: body.String(), image: common.EmailInlinePNG{
		Filename: filename, ContentID: contentID, Data: image,
	}}, nil
}

func renderPasswordResetEmail(lang, email, token string) (authEmail, error) {
	base, err := url.Parse(system_setting.ServerAddress)
	if err != nil || base.Host == "" || base.User != nil || (base.Scheme != "https" && base.Scheme != "http") {
		return authEmail{}, errors.New("invalid server address for password reset email")
	}
	link := base.JoinPath("user", "reset")
	link.RawQuery = (url.Values{"email": {email}, "token": {token}}).Encode()
	link.Fragment = ""
	return renderAuthEmail("email-reset-password.html", i18n.MsgEmailPasswordResetSubject, lang, authEmailData{
		Params: map[string]any{"Email": email},
		Link:   link.String(),
	})
}

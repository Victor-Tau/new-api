package service

import (
	_ "embed"
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

//go:embed email_templates/email.html
var emailTemplateHTML string

var emailTemplate = template.Must(template.New("email").Parse(emailTemplateHTML))

const registrationEmailIllustration = "email-register.png"

type emailContent struct {
	Language       string
	SystemName     string
	Subject        string
	Preview        string
	Heading        string
	BodyLines      []string
	Address        string
	BodySuffix     string
	CodeCharacters []string
	ActionURL      string
	ActionLabel    string
	Validity       string
	FallbackLabel  string
	FooterNote     string
	Footer         string
	ImageURL       template.URL
	Year           int
}

type EmailMessage struct {
	subject string
	html    string
	image   common.EmailInlinePNG
}

func (message EmailMessage) Send(recipient string) error {
	return common.SendEmailWithInlinePNG(message.subject, recipient, message.html, message.image)
}

func renderEmail(lang, illustration string, content emailContent) (EmailMessage, error) {
	if lang == "" {
		lang = common.DefaultLanguage
	}
	content.Language = i18n.ParseAcceptLanguage(lang)
	if content.Language == "" {
		content.Language = i18n.DefaultLang
	}
	content.SystemName = common.SystemName
	content.Footer = i18n.Translate(lang, i18n.MsgEmailAutomatedFooter)
	content.Year = time.Now().Year()
	image, err := webassets.EmailIllustration(illustration)
	if err != nil {
		return EmailMessage{}, errors.New("failed to load email illustration")
	}
	contentID := common.GetUUID() + "@new-api"
	content.ImageURL = template.URL("cid:" + contentID) // The ID is generated locally, without user input.

	var body strings.Builder
	if err := emailTemplate.Execute(&body, content); err != nil {
		return EmailMessage{}, errors.New("failed to render email")
	}
	return EmailMessage{subject: content.Subject, html: body.String(), image: common.EmailInlinePNG{
		Filename: illustration, ContentID: contentID, Data: image,
	}}, nil
}

func RenderRegistrationEmail(lang, code string) (EmailMessage, error) {
	params := map[string]any{"SystemName": common.SystemName, "Minutes": common.VerificationValidMinutes, "Code": code}
	return renderEmail(lang, registrationEmailIllustration, emailContent{
		Subject:        i18n.Translate(lang, i18n.MsgEmailVerificationSubject, params),
		Preview:        i18n.Translate(lang, i18n.MsgEmailVerificationPreview, params),
		Heading:        i18n.Translate(lang, i18n.MsgEmailVerificationGreeting, params),
		BodyLines:      []string{i18n.Translate(lang, i18n.MsgEmailVerificationIntro), i18n.Translate(lang, i18n.MsgEmailVerificationCode)},
		CodeCharacters: strings.Split(code, ""),
		Validity:       i18n.Translate(lang, i18n.MsgEmailVerificationValidity, params),
		FooterNote:     i18n.Translate(lang, i18n.MsgEmailVerificationIgnore, params),
	})
}

func RenderPasswordResetEmail(lang, email, token string) (EmailMessage, error) {
	base, err := url.Parse(system_setting.ServerAddress)
	if err != nil || base.Host == "" || base.User != nil || (base.Scheme != "https" && base.Scheme != "http") {
		return EmailMessage{}, errors.New("invalid server address for password reset email")
	}
	link := base.JoinPath("user", "reset")
	link.RawQuery = (url.Values{"email": {email}, "token": {token}}).Encode()
	link.Fragment = ""
	params := map[string]any{"SystemName": common.SystemName, "Minutes": common.VerificationValidMinutes}
	return renderEmail(lang, "email-reset-password.png", emailContent{
		Subject: i18n.Translate(lang, i18n.MsgEmailPasswordResetSubject, params),
		Preview: i18n.Translate(lang, i18n.MsgEmailPasswordResetPreview, params),
		Heading: i18n.Translate(lang, i18n.MsgEmailPasswordResetHeading),
		BodyLines: []string{
			i18n.Translate(lang, i18n.MsgEmailPasswordResetGreeting),
			i18n.Translate(lang, i18n.MsgEmailPasswordResetRequestPrefix, params),
		},
		Address:       email,
		BodySuffix:    i18n.Translate(lang, i18n.MsgEmailPasswordResetRequestSuffix, params),
		ActionURL:     link.String(),
		ActionLabel:   i18n.Translate(lang, i18n.MsgEmailPasswordResetLink),
		Validity:      i18n.Translate(lang, i18n.MsgEmailPasswordResetValidity, params),
		FallbackLabel: i18n.Translate(lang, i18n.MsgEmailPasswordResetLinkFallback),
		FooterNote: strings.Join([]string{
			i18n.Translate(lang, i18n.MsgEmailPasswordResetIgnoreHeading),
			i18n.Translate(lang, i18n.MsgEmailPasswordResetIgnore),
			i18n.Translate(lang, i18n.MsgEmailPasswordResetNotice, params),
		}, " "),
	})
}

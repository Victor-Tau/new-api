package webassets

import "embed"

//go:embed public/email-assets/*.png
var emailAssets embed.FS

func EmailIllustration(filename string) ([]byte, error) {
	return emailAssets.ReadFile("public/email-assets/" + filename)
}

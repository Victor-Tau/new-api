package common

import (
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"strings"
)

func writeSMTPRelatedBody(message *strings.Builder, html string, image EmailInlinePNG) error {
	related := multipart.NewWriter(message)
	contentType := mime.FormatMediaType("multipart/related", map[string]string{
		"boundary": related.Boundary(), "type": "text/html",
	})
	fmt.Fprintf(message, "MIME-Version: 1.0\r\nContent-Type: %s\r\n\r\n", contentType)
	htmlPart, err := related.CreatePart(textproto.MIMEHeader{
		"Content-Type": {"text/html; charset=UTF-8"}, "Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return err
	}
	encodedHTML := quotedprintable.NewWriter(htmlPart)
	if _, err := io.WriteString(encodedHTML, html); err != nil {
		return err
	}
	if err := encodedHTML.Close(); err != nil {
		return err
	}
	imagePart, err := related.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"image/png"},
		"Content-Disposition":       {mime.FormatMediaType("inline", map[string]string{"filename": image.Filename})},
		"Content-Id":                {"<" + image.ContentID + ">"},
		"Content-Transfer-Encoding": {"base64"},
	})
	if err != nil {
		return err
	}
	encodedImage := base64.StdEncoding.EncodeToString(image.Data)
	for len(encodedImage) > 0 {
		length := min(len(encodedImage), 76)
		if _, err := io.WriteString(imagePart, encodedImage[:length]+"\r\n"); err != nil {
			return err
		}
		encodedImage = encodedImage[length:]
	}
	return related.Close()
}

package common

import (
	"crypto/subtle"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
)

type verificationValue struct {
	code string
	time time.Time
}

const (
	EmailVerificationPurpose = "v"
	PasswordResetPurpose     = "r"
)

var verificationMutex sync.Mutex
var verificationMap map[string]verificationValue
var verificationMapMaxSize = 10
var VerificationValidMinutes = 10

func GenerateVerificationCode(length int) string {
	code := uuid.New().String()
	code = strings.Replace(code, "-", "", -1)
	if length == 0 {
		return code
	}
	return code[:length]
}

func GenerateEmailVerificationCode() (string, error) {
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	const digits = "0123456789"
	for {
		code, err := generateRandomChars(6, letters+digits)
		if err != nil {
			return "", err
		}
		if strings.ContainsAny(code, letters) && strings.ContainsAny(code, digits) {
			return code, nil
		}
	}
}

func NormalizeEmailVerificationCode(input string) (string, error) {
	var code [6]byte
	length := 0
	for _, character := range input {
		if unicode.IsSpace(character) || character == '·' || character == '\u200b' || character == '\ufeff' {
			continue
		}
		if character >= 'a' && character <= 'z' {
			character -= 'a' - 'A'
		}
		if length == len(code) || !(character >= 'A' && character <= 'Z' || character >= '0' && character <= '9') {
			return "", errors.New("invalid email verification code")
		}
		code[length] = byte(character)
		length++
	}
	if length != len(code) {
		return "", errors.New("invalid email verification code")
	}
	return string(code[:]), nil
}

func RegisterVerificationCodeWithKey(key string, code string, purpose string) {
	verificationMutex.Lock()
	defer verificationMutex.Unlock()
	verificationMap[purpose+key] = verificationValue{
		code: code,
		time: time.Now(),
	}
	if len(verificationMap) > verificationMapMaxSize {
		removeExpiredPairs()
	}
}

func VerifyCodeWithKey(key string, code string, purpose string) bool {
	if purpose == EmailVerificationPurpose {
		normalized, err := NormalizeEmailVerificationCode(code)
		if err != nil {
			return false
		}
		code = normalized
	}
	verificationMutex.Lock()
	defer verificationMutex.Unlock()
	value, okay := verificationMap[purpose+key]
	now := time.Now()
	if !okay || int(now.Sub(value.time).Seconds()) >= VerificationValidMinutes*60 {
		return false
	}
	if purpose == EmailVerificationPurpose {
		return subtle.ConstantTimeCompare([]byte(code), []byte(strings.ToUpper(value.code))) == 1
	}
	return code == value.code
}

func DeleteKey(key string, purpose string) {
	verificationMutex.Lock()
	defer verificationMutex.Unlock()
	delete(verificationMap, purpose+key)
}

// no lock inside, so the caller must lock the verificationMap before calling!
func removeExpiredPairs() {
	now := time.Now()
	for key := range verificationMap {
		if int(now.Sub(verificationMap[key].time).Seconds()) >= VerificationValidMinutes*60 {
			delete(verificationMap, key)
		}
	}
}

func init() {
	verificationMutex.Lock()
	defer verificationMutex.Unlock()
	verificationMap = make(map[string]verificationValue)
}

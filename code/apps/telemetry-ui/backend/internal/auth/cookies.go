package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
	"time"
)

func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (o *OAuth) cookieName(kind string) string {
	if strings.HasPrefix(o.cfg.Origin, "https://") {
		return "__Host-telemetry_" + kind
	}
	return "telemetry_" + kind
}

func (o *OAuth) cookie(c *gin.Context, kind, value string, age int) {
	cookie := &http.Cookie{Name: o.cookieName(kind), Value: value, Path: "/", MaxAge: age, HttpOnly: true, Secure: strings.HasPrefix(o.cfg.Origin, "https://"), SameSite: http.SameSiteLaxMode}
	if age < 0 {
		cookie.Expires = time.Unix(1, 0)
	}
	http.SetCookie(c.Writer, cookie)
}

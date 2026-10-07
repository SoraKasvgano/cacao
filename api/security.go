package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lanthora/cacao/argp"
	"github.com/lanthora/cacao/model"
	"golang.org/x/crypto/bcrypt"
)

const sessionLifetime = 24 * time.Hour

// Hash a fixed-size digest with bcrypt so existing long passwords remain usable.
func hashUserPassword(username, password string) string {
	digest := sha256.Sum256([]byte(username + ":" + password))
	hash, err := bcrypt.GenerateFromPassword([]byte(hex.EncodeToString(digest[:])), bcrypt.DefaultCost)
	if err != nil {
		panic("password hashing failed")
	}
	return "bcrypt-sha256:" + string(hash)
}

func verifyUserPassword(user *model.User, password string) bool {
	digest := sha256.Sum256([]byte(user.Name + ":" + password))
	encoded := hex.EncodeToString(digest[:])
	if strings.HasPrefix(user.Password, "bcrypt-sha256:") {
		return bcrypt.CompareHashAndPassword([]byte(strings.TrimPrefix(user.Password, "bcrypt-sha256:")), []byte(encoded)) == nil
	}
	return subtle.ConstantTimeCompare([]byte(user.Password), []byte(encoded)) == 1
}

func hashSessionToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validSessionToken(stored, supplied string) bool {
	if strings.HasPrefix(stored, "sha256:") {
		supplied = hashSessionToken(supplied)
	}
	return stored != "" && subtle.ConstantTimeCompare([]byte(stored), []byte(supplied)) == 1
}

func newSession(user *model.User) string {
	token := uuid.NewString()
	expires := time.Now().Add(sessionLifetime)
	user.Token = hashSessionToken(token)
	user.TokenExpiresAt = &expires
	return token
}

func setSessionCookies(c *gin.Context, userID uint, token string, maxAge int) {
	// Behind TLS termination, explicitly set --secure-cookies=true. Never trust
	// an arbitrary X-Forwarded-Proto header to make this decision.
	secure := c.Request.TLS != nil || argp.Get("secure-cookies", "false") == "true"
	c.SetSameSite(http.SameSiteLaxMode)
	id := ""
	if maxAge > 0 {
		id = strconv.FormatUint(uint64(userID), 10)
	}
	c.SetCookie("id", id, maxAge, "/", "", secure, true)
	c.SetCookie("token", token, maxAge, "/", "", secure, true)
}

func randomString(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic("secure random generation failed")
	}
	return base64.RawURLEncoding.EncodeToString(buf)[:n]
}

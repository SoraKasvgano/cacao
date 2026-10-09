package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lanthora/cacao/argp"
	"github.com/lanthora/cacao/model"
	"golang.org/x/crypto/bcrypt"
)

const sessionLifetime = 24 * time.Hour

// A concurrent burst must not schedule unbounded bcrypt work before failed
// attempts have had a chance to reach the per-IP/account limiter.
var passwordWork = make(chan struct{}, 8)

func acquirePasswordWork(c *gin.Context) bool {
	select {
	case passwordWork <- struct{}{}:
		return true
	default:
		c.Header("Retry-After", "1")
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"status": TooManyRequests, "msg": "authentication busy; retry shortly", "data": nil})
		return false
	}
}

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

// The environment variable wins over config.toml, and there is deliberately
// no command-line flag: arguments are visible in the process list.
func expectedSetupToken() string {
	expected := os.Getenv("CACAO_SETUP_TOKEN")
	if expected == "" {
		expected = argp.Config("setup-token")
	}
	return expected
}

func validSetupToken(supplied string) bool {
	expected := expectedSetupToken()
	if len(expected) < 32 || supplied == "" {
		return false
	}
	a, b := sha256.Sum256([]byte(expected)), sha256.Sum256([]byte(supplied))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func randomString(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic("secure random generation failed")
	}
	return base64.RawURLEncoding.EncodeToString(buf)[:n]
}

type attemptWindow struct {
	count int
	until time.Time
}

type attemptLimiter struct {
	mu      sync.Mutex
	entries map[string]attemptWindow
}

var authAttempts = &attemptLimiter{entries: make(map[string]attemptWindow)}

// Bound failed guesses and memory consumption. Successful authentication,
// ordinary API calls and established tunnel traffic do not consume the budget.
func (l *attemptLimiter) allow(key string, limit int, period time.Duration, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, exists := l.entries[key]
	if !exists || !now.Before(w.until) {
		if len(l.entries) >= 4096 {
			for k, entry := range l.entries {
				if !now.Before(entry.until) {
					delete(l.entries, k)
				}
			}
			if len(l.entries) >= 4096 && !exists {
				return false
			}
		}
		w = attemptWindow{until: now.Add(period)}
	}
	if w.count >= limit {
		return false
	}
	w.count++
	l.entries[key] = w
	return true
}

func allowAuthentication(c *gin.Context, username string) bool {
	now := time.Now()
	allowed := true
	authAttempts.mu.Lock()
	for _, key := range authenticationKeys(c, username) {
		entry := authAttempts.entries[key]
		if now.Before(entry.until) && entry.count >= 30 {
			allowed = false
		}
	}
	// Fail closed if attackers have filled the bounded table with active keys.
	if len(authAttempts.entries) >= 4096 {
		for key, entry := range authAttempts.entries {
			if !now.Before(entry.until) {
				delete(authAttempts.entries, key)
			}
		}
		if len(authAttempts.entries) >= 4096 {
			allowed = false
		}
	}
	authAttempts.mu.Unlock()
	if !allowed {
		c.Header("Retry-After", "60")
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"status": TooManyRequests, "msg": "too many authentication attempts", "data": nil})
	}
	return allowed
}

func authenticationKeys(c *gin.Context, username string) []string {
	keys := []string{"ip:" + c.ClientIP()}
	if username != "" {
		// Digest untrusted names so an oversized name cannot grow the limiter map.
		digest := sha256.Sum256([]byte(username))
		keys = append(keys, "user:"+hex.EncodeToString(digest[:]))
	}
	return keys
}

func recordAuthenticationFailure(c *gin.Context, username string) {
	for _, key := range authenticationKeys(c, username) {
		authAttempts.allow(key, 30, time.Minute, time.Now())
	}
}

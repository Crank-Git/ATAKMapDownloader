package commands

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// tokenCookie is the name of the cookie that holds the token (FR-team-server-20).
const tokenCookie = "atakdl_token" //nolint:gosec // This is the cookie name, not a credential.

// maxFailures is the count of failed token attempts that one IP address can make in failureWindow (FR-team-server-24).
const maxFailures = 10

// failureWindow is the time that a failed token attempt counts toward maxFailures.
const failureWindow = time.Minute

// publicPaths need no token, so that a client can reach the health route and the sign-in page.
// The static paths are the files that signin.html loads.
var publicPaths = map[string]bool{
	"/healthz":              true,
	"/signin":               true,
	"/api/session":          true,
	"/static/css/style.css": true,
	"/static/js/theme.js":   true,
	"/static/js/signin.js":  true,
}

// tokenAuth holds the token of a server that is not on loopback.
// A nil *tokenAuth is the auth of a server on loopback, and it checks nothing (FR-team-server-16).
type tokenAuth struct {
	// sum is the SHA-256 sum of the token, so that valid compares two values of equal length.
	sum      [32]byte
	failures *failureLimiter
}

// newTokenAuth returns the auth that accepts token.
func newTokenAuth(token string) *tokenAuth {
	return &tokenAuth{
		sum:      sha256.Sum256([]byte(token)),
		failures: &failureLimiter{now: time.Now, byIP: map[string][]time.Time{}},
	}
}

// newToken returns a random token of 32 hexadecimal characters (FR-team-server-19).
// It returns an error when the system has no random source.
func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// valid reports whether candidate is the token.
// The two SHA-256 sums have equal length, so the constant-time compare does not leak the token length (FR-team-server-22).
func (a *tokenAuth) valid(candidate string) bool {
	sum := sha256.Sum256([]byte(candidate))
	return subtle.ConstantTimeCompare(sum[:], a.sum[:]) == 1
}

// clientIP returns the host part of r.RemoteAddr.
// The server does not read X-Forwarded-For, because any client can set that header.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// requestToken returns the token of r and true, or "" and false when r carries no token.
// It reads only the Authorization header and the cookie, because a token in a URL goes into logs.
// fromCookie is true when the token came from the cookie.
func requestToken(r *http.Request) (token string, fromCookie, ok bool) {
	if scheme, value, found := strings.Cut(r.Header.Get("Authorization"), " "); found && strings.EqualFold(scheme, "Bearer") {
		return value, false, true
	}
	if c, err := r.Cookie(tokenCookie); err == nil {
		return c.Value, true, true
	}
	return "", false, false
}

// setTokenCookie sets the token cookie to value with the attributes of FR-team-server-21.
// A negative maxAge tells the browser to delete the cookie.
func setTokenCookie(w http.ResponseWriter, value string, maxAge int) {
	// The server serves plain HTTP on a LAN, so the browser would never send back a Secure cookie.
	http.SetCookie(w, &http.Cookie{Name: tokenCookie, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, SameSite: http.SameSiteStrictMode}) //nolint:gosec // See the comment above.
}

// clearTokenCookie tells the browser to delete the token cookie.
func clearTokenCookie(w http.ResponseWriter) {
	setTokenCookie(w, "", -1)
}

// handler returns a handler that passes a request to next only when the request carries the token.
// It writes one of these responses for a request without a valid token (FR-team-server-23, FR-team-server-24):
//   - 429 too_many_attempts when the IP address has maxFailures failed attempts in failureWindow, even with the token.
//   - 302 to /signin for a GET or HEAD request of a path outside /api/.
//   - 401 unauthorized for every other request.
//
// A request for a path in publicPaths passes with no token.
func (a *tokenAuth) handler(next http.Handler) http.Handler {
	if a == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		ip := clientIP(r)
		if a.failures.blocked(ip) {
			writeTooManyAttempts(w)
			return
		}
		token, fromCookie, ok := requestToken(r)
		if ok && a.valid(token) {
			next.ServeHTTP(w, r)
			return
		}
		if ok {
			a.failures.record(ip)
		}
		// A cookie from before a restart is not valid, so the server deletes it, and it counts as one failure only.
		if fromCookie {
			clearTokenCookie(w)
		}
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && !strings.HasPrefix(r.URL.Path, "/api/") {
			http.Redirect(w, r, "/signin", http.StatusFound)
			return
		}
		writeError(w, http.StatusUnauthorized, "unauthorized", "A valid token is required")
	})
}

// writeTooManyAttempts writes the 429 response of FR-team-server-24.
func writeTooManyAttempts(w http.ResponseWriter) {
	writeError(w, http.StatusTooManyRequests, "too_many_attempts", "Too many failed token attempts. Wait one minute")
}

// apiSession serves POST /api/session, which sets the token cookie, and DELETE /api/session, which deletes it.
// POST writes 401 unauthorized for a wrong token, and 429 too_many_attempts when the IP address is above the limit.
// On loopback, POST writes 204 and sets no cookie, because the server needs no token.
func (h *WebHandler) apiSession(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
	case http.MethodDelete:
		clearTokenCookie(w)
		w.WriteHeader(http.StatusNoContent)
		return
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	ip := clientIP(r)
	if h.auth != nil && h.auth.failures.blocked(ip) {
		writeTooManyAttempts(w)
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if h.auth == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !h.auth.valid(req.Token) {
		h.auth.failures.record(ip)
		writeError(w, http.StatusUnauthorized, "unauthorized", "Wrong token")
		return
	}
	setTokenCookie(w, req.Token, 0)
	w.WriteHeader(http.StatusNoContent)
}

// failureLimiter counts the failed token attempts of each IP address in the last failureWindow.
type failureLimiter struct {
	mu sync.Mutex
	// now is the clock. A test replaces it.
	now  func() time.Time
	byIP map[string][]time.Time
}

// blocked reports whether ip has maxFailures failed attempts in the last failureWindow.
func (l *failureLimiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(ip)) >= maxFailures
}

// record adds one failed attempt of ip.
// It deletes each IP address whose last attempt is older than failureWindow, so the map does not grow with old addresses.
func (l *failureLimiter) record(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := l.now().Add(-failureWindow)
	for other, times := range l.byIP {
		if !times[len(times)-1].After(cutoff) {
			delete(l.byIP, other)
		}
	}
	l.byIP[ip] = append(l.recent(ip), l.now())
}

// recent drops the attempts of ip that are older than failureWindow, and returns the rest.
// It deletes an IP address with no recent attempt. The caller holds l.mu.
func (l *failureLimiter) recent(ip string) []time.Time {
	cutoff := l.now().Add(-failureWindow)
	times := l.byIP[ip]
	i := 0
	for i < len(times) && !times[i].After(cutoff) {
		i++
	}
	times = times[i:]
	if len(times) == 0 {
		delete(l.byIP, ip)
	} else {
		l.byIP[ip] = times
	}
	return times
}

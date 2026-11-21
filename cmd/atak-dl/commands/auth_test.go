package commands

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// testToken has the length of a generated token.
const testToken = "0123456789abcdef0123456789abcdef"

// newAuthServer returns the server handler of a server that is not on loopback, with testToken and the clock now.
func newAuthServer(t *testing.T, now func() time.Time) http.Handler {
	t.Helper()
	h := newTestHandler(t, map[string]string{"test.xml": testSourceXML})
	h.auth = newTokenAuth(testToken)
	if now != nil {
		h.auth.failures.now = now
	}
	return newServerHandler(h)
}

// authRequest returns a request from the IP address ip, with the bearer token and the cookie when they are not empty.
func authRequest(method, path, body, ip, bearer, cookie string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = ip + ":40000"
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if cookie != "" {
		req.Header.Set("Cookie", "atakdl_token="+cookie)
	}
	return req
}

func TestAuthHandler(t *testing.T) {
	tests := []struct {
		name     string
		loopback bool
		method   string
		path     string
		header   string
		cookie   string
		status   int
		code     string
		location string
	}{
		{name: "passes a request with no token on loopback", loopback: true, method: http.MethodGet, path: "/api/sources", status: http.StatusOK},
		{name: "rejects an API request with no token", method: http.MethodGet, path: "/api/sources", status: http.StatusUnauthorized, code: "unauthorized"},
		{name: "rejects a POST with no token", method: http.MethodPost, path: "/api/jobs", status: http.StatusUnauthorized, code: "unauthorized"},
		{name: "passes a valid bearer token", method: http.MethodGet, path: "/api/sources", header: "Bearer " + testToken, status: http.StatusOK},
		{name: "reads the Bearer scheme in any letter case", method: http.MethodGet, path: "/api/sources", header: "bearer " + testToken, status: http.StatusOK},
		{name: "passes a valid cookie", method: http.MethodGet, path: "/api/sources", cookie: testToken, status: http.StatusOK},
		{name: "rejects a wrong bearer token", method: http.MethodGet, path: "/api/sources", header: "Bearer wrong", status: http.StatusUnauthorized, code: "unauthorized"},
		{name: "rejects a Basic authorization header", method: http.MethodGet, path: "/api/sources", header: "Basic " + testToken, status: http.StatusUnauthorized, code: "unauthorized"},
		{name: "rejects a wrong cookie", method: http.MethodGet, path: "/api/sources", cookie: "wrong", status: http.StatusUnauthorized, code: "unauthorized"},
		{name: "ignores a token in the query parameter token", method: http.MethodGet, path: "/api/sources?token=" + testToken, status: http.StatusUnauthorized, code: "unauthorized"},
		{name: "ignores a token in the query parameter atakdl_token", method: http.MethodGet, path: "/api/sources?atakdl_token=" + testToken, status: http.StatusUnauthorized, code: "unauthorized"},
		{name: "redirects a page request with no token to the sign-in page", method: http.MethodGet, path: "/", status: http.StatusFound, location: "/signin"},
		{name: "redirects a HEAD page request with no token", method: http.MethodHead, path: "/", status: http.StatusFound, location: "/signin"},
		{name: "redirects a page request with a query token", method: http.MethodGet, path: "/?token=" + testToken, status: http.StatusFound, location: "/signin"},
		{name: "redirects a static file request that the sign-in page does not load", method: http.MethodGet, path: "/static/js/app.js", status: http.StatusFound, location: "/signin"},
		{name: "passes a page request with a valid cookie", method: http.MethodGet, path: "/", cookie: testToken, status: http.StatusOK},
		{name: "passes the style sheet of the sign-in page with no token", method: http.MethodGet, path: "/static/css/style.css", status: http.StatusOK},
		// The health route does not exist yet, so the request reaches the 404 of ServeHTTP.
		{name: "passes GET /healthz with no token", method: http.MethodGet, path: "/healthz", status: http.StatusNotFound, code: "not_found"},
		{name: "passes GET /signin with no token", method: http.MethodGet, path: "/signin", status: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(t, map[string]string{"test.xml": testSourceXML})
			if !tt.loopback {
				h.auth = newTokenAuth(testToken)
			}
			req := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			if tt.cookie != "" {
				req.Header.Set("Cookie", "atakdl_token="+tt.cookie)
			}
			rec := httptest.NewRecorder()
			newServerHandler(h).ServeHTTP(rec, req)
			if tt.code != "" {
				checkError(t, rec.Result(), tt.status, tt.code)
				return
			}
			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d; body %s", rec.Code, tt.status, rec.Body.String())
			}
			if got := rec.Header().Get("Location"); got != tt.location {
				t.Errorf("Location = %q, want %q", got, tt.location)
			}
		})
	}
}

func TestAuthHandlerClearsAWrongCookie(t *testing.T) {
	rec := httptest.NewRecorder()
	newAuthServer(t, nil).ServeHTTP(rec, authRequest(http.MethodGet, "/", "", "192.0.2.1", "", "stale"))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "atakdl_token" || cookies[0].MaxAge >= 0 {
		t.Errorf("cookies = %v, want one atakdl_token cookie with a negative MaxAge", cookies)
	}
}

func TestSessionSetsTheCookie(t *testing.T) {
	h := newAuthServer(t, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authRequest(http.MethodPost, "/api/session", `{"token":"`+testToken+`"}`, "192.0.2.1", "", ""))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %v, want one cookie", cookies)
	}
	c := cookies[0]
	if c.Name != "atakdl_token" || c.Value != testToken {
		t.Errorf("cookie = %s=%s, want atakdl_token=<token>", c.Name, c.Value)
	}
	if !c.HttpOnly {
		t.Error("cookie has no HttpOnly attribute")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", c.SameSite)
	}
	if c.Path != "/" {
		t.Errorf("Path = %q, want /", c.Path)
	}

	// The cookie of the response signs in the next request.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authRequest(http.MethodGet, "/api/sources", "", "192.0.2.1", "", c.Value))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/sources with the session cookie returned %d, want 200", rec.Code)
	}
}

func TestSessionRoute(t *testing.T) {
	tests := []struct {
		name   string
		method string
		body   string
		status int
		code   string
	}{
		{"rejects a wrong token", http.MethodPost, `{"token":"wrong"}`, http.StatusUnauthorized, "unauthorized"},
		{"rejects an empty token", http.MethodPost, `{"token":""}`, http.StatusUnauthorized, "unauthorized"},
		{"rejects a body that is not JSON", http.MethodPost, `{`, http.StatusBadRequest, "invalid_json"},
		{"rejects a GET", http.MethodGet, "", http.StatusMethodNotAllowed, "method_not_allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			newAuthServer(t, nil).ServeHTTP(rec, authRequest(tt.method, "/api/session", tt.body, "192.0.2.1", "", ""))
			if cookies := rec.Result().Cookies(); len(cookies) != 0 {
				t.Errorf("cookies = %v, want none", cookies)
			}
			checkError(t, rec.Result(), tt.status, tt.code)
		})
	}
}

func TestSessionDeleteClearsTheCookie(t *testing.T) {
	rec := httptest.NewRecorder()
	newAuthServer(t, nil).ServeHTTP(rec, authRequest(http.MethodDelete, "/api/session", "", "192.0.2.1", "", ""))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "atakdl_token" || cookies[0].MaxAge >= 0 || cookies[0].Path != "/" {
		t.Errorf("cookies = %v, want one atakdl_token cookie with Path=/ and a negative MaxAge", cookies)
	}
}

func TestSessionOnLoopbackSetsNoCookie(t *testing.T) {
	rec := httptest.NewRecorder()
	h := newServerHandler(newTestHandler(t, nil))
	h.ServeHTTP(rec, authRequest(http.MethodPost, "/api/session", `{"token":"any"}`, "127.0.0.1", "", ""))
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if cookies := rec.Result().Cookies(); len(cookies) != 0 {
		t.Errorf("cookies = %v, want none", cookies)
	}
}

// fakeClock is a clock that a test moves forward.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func TestFailureLimiterReturns429OnThe11thAttempt(t *testing.T) {
	// An attempt sends one failed token attempt from ip and returns the status.
	type attempt func(h http.Handler, ip string) int
	bearer := func(h http.Handler, ip string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, authRequest(http.MethodGet, "/api/sources", "", ip, "wrong", ""))
		return rec.Code
	}
	cookie := func(h http.Handler, ip string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, authRequest(http.MethodGet, "/api/sources", "", ip, "", "wrong"))
		return rec.Code
	}
	session := func(h http.Handler, ip string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, authRequest(http.MethodPost, "/api/session", `{"token":"wrong"}`, ip, "", ""))
		return rec.Code
	}
	tests := []struct {
		name string
		fail attempt
	}{
		{"counts a wrong bearer token", bearer},
		{"counts a wrong cookie", cookie},
		{"counts a wrong sign-in", session},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := &fakeClock{t: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)}
			h := newAuthServer(t, clock.now)
			for i := 1; i <= 10; i++ {
				if got := tt.fail(h, "192.0.2.1"); got != http.StatusUnauthorized {
					t.Fatalf("attempt %d returned %d, want 401", i, got)
				}
				clock.t = clock.t.Add(time.Second)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, authRequest(http.MethodGet, "/api/sources", "", "192.0.2.1", "wrong", ""))
			checkError(t, rec.Result(), http.StatusTooManyRequests, "too_many_attempts")

			// An IP address above the limit gets 429 with the correct token too.
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, authRequest(http.MethodGet, "/api/sources", "", "192.0.2.1", testToken, ""))
			checkError(t, rec.Result(), http.StatusTooManyRequests, "too_many_attempts")
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, authRequest(http.MethodPost, "/api/session", `{"token":"`+testToken+`"}`, "192.0.2.1", "", ""))
			checkError(t, rec.Result(), http.StatusTooManyRequests, "too_many_attempts")

			// The limit applies to one IP address only.
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, authRequest(http.MethodGet, "/api/sources", "", "198.51.100.7", testToken, ""))
			if rec.Code != http.StatusOK {
				t.Errorf("a second IP address got %d, want 200", rec.Code)
			}

			// 61 s after the first failure, one failure is older than the window, so a slot is free.
			clock.t = clock.t.Add(51 * time.Second)
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, authRequest(http.MethodGet, "/api/sources", "", "192.0.2.1", testToken, ""))
			if rec.Code != http.StatusOK {
				t.Errorf("after the window, the correct token got %d, want 200", rec.Code)
			}
		})
	}
}

func TestFailureLimiterDoesNotCountARequestWithNoToken(t *testing.T) {
	h := newAuthServer(t, nil)
	for i := 1; i <= 20; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, authRequest(http.MethodGet, "/api/sources", "", "192.0.2.1", "", ""))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("request %d returned %d, want 401", i, rec.Code)
		}
	}
}

func TestFailureLimiterIgnoresXForwardedFor(t *testing.T) {
	h := newAuthServer(t, nil)
	for i := 1; i <= 10; i++ {
		req := authRequest(http.MethodGet, "/api/sources", "", "192.0.2.1", "wrong", "")
		req.Header.Set("X-Forwarded-For", "203.0.113.5")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	req := authRequest(http.MethodGet, "/api/sources", "", "192.0.2.1", testToken, "")
	req.Header.Set("X-Forwarded-For", "203.0.113.99")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	checkError(t, rec.Result(), http.StatusTooManyRequests, "too_many_attempts")
}

func TestTokenAuthAcceptsOnlyTheExactToken(t *testing.T) {
	a := newTokenAuth(testToken)
	tests := []struct {
		candidate string
		want      bool
	}{
		{testToken, true},
		{"", false},
		{testToken[:31], false},
		{testToken + "0", false},
		{strings.ToUpper(testToken), false},
		{" " + testToken, false},
	}
	for _, tt := range tests {
		if got := a.valid(tt.candidate); got != tt.want {
			t.Errorf("valid(%q) = %v, want %v", tt.candidate, got, tt.want)
		}
	}
}

func TestNewTokenHas32Characters(t *testing.T) {
	hex32 := regexp.MustCompile(`^[0-9a-f]{32}$`)
	first, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	if !hex32.MatchString(first) || !hex32.MatchString(second) {
		t.Errorf("tokens %q and %q, want 32 hexadecimal characters", first, second)
	}
	if first == second {
		t.Errorf("two tokens are both %q, want two random tokens", first)
	}
}

// captureLog sends the slog output to a buffer until the test ends.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

func TestNoLogLineHoldsTheToken(t *testing.T) {
	logs := captureLog(t)
	clock := &fakeClock{t: time.Now()}
	h := newAuthServer(t, clock.now)
	requests := []*http.Request{
		authRequest(http.MethodGet, "/api/sources", "", "192.0.2.1", testToken, ""),
		authRequest(http.MethodGet, "/", "", "192.0.2.1", "", testToken),
		authRequest(http.MethodGet, "/api/sources?token="+testToken, "", "192.0.2.1", "", ""),
		authRequest(http.MethodPost, "/api/session", `{"token":"`+testToken+`"}`, "192.0.2.1", "", ""),
		authRequest(http.MethodPost, "/api/session", `{"token":"`+testToken+`x"}`, "192.0.2.1", "", ""),
		authRequest(http.MethodGet, "/api/sources", "", "192.0.2.1", testToken+"x", ""),
		authRequest(http.MethodPost, "/api/validate-source", `{"source_name":"`+testToken+`"}`, "192.0.2.1", testToken, ""),
	}
	for _, req := range requests {
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	if strings.Contains(logs.String(), testToken) {
		t.Errorf("the log holds the token:\n%s", logs.String())
	}
}

package web

import (
	"regexp"
	"strings"
	"testing"
)

// signinScenario is the input of testdata/signin_page.js.
type signinScenario struct {
	Token    string    `json:"token"`
	Response *response `json:"response,omitempty"`
}

// signinPage is the state of the sign-in page that signin_page.js prints.
type signinPage struct {
	Error    string `json:"error"`
	Location string `json:"location"`
	Requests []struct {
		Key  string         `json:"key"`
		Body map[string]any `json:"body"`
	} `json:"requests"`
	ButtonDisabled bool `json:"buttonDisabled"`
}

func runSigninPage(t *testing.T, s signinScenario) signinPage {
	t.Helper()
	var got signinPage
	runNodeHarness(t, "signin_page.js", "signin.html", "signin.js", s, &got)
	return got
}

func TestSigninShowsWrongTokenOn401(t *testing.T) {
	apiErr := func(code, message string) map[string]any {
		return map[string]any{"error": map[string]any{"code": code, "message": message}}
	}
	tests := []struct {
		name     string
		response *response
		error    string
		location string
	}{
		{"goes to the Jobs page on 204", &response{Status: 204}, "", "/jobs"},
		{"shows Wrong token on 401", &response{Status: 401, Body: apiErr("unauthorized", "Wrong token")}, "Wrong token", ""},
		{"shows the attempts message on 429", &response{Status: 429, Body: apiErr("too_many_attempts", "Too many failed token attempts. Wait one minute")}, "Too many attempts. Wait one minute, then try again.", ""},
		{"shows the status of another error", &response{Status: 500}, "Sign in failed: the server returned status 500", ""},
		{"shows Server unreachable when the request fails", &response{Reject: true}, "Server unreachable", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runSigninPage(t, signinScenario{Token: "abc", Response: tt.response})
			if got.Error != tt.error {
				t.Errorf("error = %q, want %q", got.Error, tt.error)
			}
			if got.Location != tt.location {
				t.Errorf("location = %q, want %q", got.Location, tt.location)
			}
			if got.ButtonDisabled {
				t.Error("Sign in stays disabled after the response")
			}
		})
	}
}

func TestSigninSendsTheTokenInTheBody(t *testing.T) {
	// A pasted token often carries a space at an end, and a token never does.
	got := runSigninPage(t, signinScenario{Token: "  abc123 ", Response: &response{Status: 204}})
	if len(got.Requests) != 1 || got.Requests[0].Key != "POST /api/session" || got.Requests[0].Body["token"] != "abc123" {
		t.Errorf("requests = %+v, want one POST /api/session with the token abc123", got.Requests)
	}
}

func TestSigninAsksForATokenBeforeItSendsARequest(t *testing.T) {
	got := runSigninPage(t, signinScenario{Token: " "})
	if len(got.Requests) != 0 {
		t.Errorf("requests = %+v, want none for an empty token", got.Requests)
	}
	if got.Error != "Enter the token" {
		t.Errorf("error = %q, want \"Enter the token\"", got.Error)
	}
}

func TestSigninPageHoldsTheTokenFieldAndTheHelpText(t *testing.T) {
	page := string(SigninHTML())
	for _, want := range []*regexp.Regexp{
		regexp.MustCompile(`<input id="token"[^>]*\btype="password"`),
		regexp.MustCompile(`<input id="token"[^>]*\bautocomplete="current-password"`),
		regexp.MustCompile(`<button id="signinButton"[^>]*\btype="submit"[^>]*>Sign in</button>`),
	} {
		if !want.MatchString(page) {
			t.Errorf("signin.html does not match %s", want)
		}
	}
	text := strings.Join(strings.Fields(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(page, "")), " ")
	if !strings.Contains(text, "Find the token in the server log or in ATAKDL_TOKEN") {
		t.Error("signin.html does not hold the help text of the sign-in page")
	}
}

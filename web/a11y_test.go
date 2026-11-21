package web

import (
	"regexp"
	"strings"
	"testing"
)

func appJS(t *testing.T) string {
	t.Helper()
	b, err := staticFS.ReadFile("static/js/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	return string(b)
}

func TestEveryFormControlInIndexHasAVisibleLabel(t *testing.T) {
	index := string(IndexHTML())
	controls := regexp.MustCompile(`<(?:select|input|textarea)\b[^>]*\bid="([^"]+)"`).FindAllStringSubmatch(index, -1)
	if len(controls) == 0 {
		t.Fatal("index.html has no form control, so the pattern is stale")
	}
	for _, m := range controls {
		label := regexp.MustCompile(`<label\b[^>]*\bfor="` + regexp.QuoteMeta(m[1]) + `"[^>]*>\s*\S`)
		if !label.MatchString(index) {
			t.Errorf("the control %q has no <label for> with text (FR-offline-web-ui-25)", m[1])
		}
	}
}

func TestEveryIconOnlyButtonInIndexHasAnAriaLabel(t *testing.T) {
	index := string(IndexHTML())
	buttons := regexp.MustCompile(`(?s)<button\b([^>]*)>(.*?)</button>`).FindAllStringSubmatch(index, -1)
	if len(buttons) == 0 {
		t.Fatal("index.html has no button, so the pattern is stale")
	}
	svg := regexp.MustCompile(`(?s)<svg\b.*?</svg>`)
	tag := regexp.MustCompile(`<[^>]+>`)
	label := regexp.MustCompile(`\baria-label="[^"]+"`)
	for _, m := range buttons {
		text := strings.TrimSpace(tag.ReplaceAllString(svg.ReplaceAllString(m[2], ""), ""))
		if text == "" && !label.MatchString(m[1]) {
			t.Errorf("the icon-only button <button%s> has no aria-label (FR-offline-web-ui-26)", m[1])
		}
	}
}

func TestIndexMarksTheLiveRegionsAndTheProgressBar(t *testing.T) {
	index := string(IndexHTML())
	tests := []struct {
		name string
		want *regexp.Regexp
	}{
		{"job status region (FR-offline-web-ui-27)", regexp.MustCompile(`<p id="jobSummary"[^>]*\baria-live="polite"`)},
		{"status log (FR-offline-web-ui-27)", regexp.MustCompile(`<div id="statusLog"[^>]*\baria-live="polite"`)},
		// The status log scrolls, so a keyboard user needs a focus stop to scroll it.
		{"status log takes keyboard focus", regexp.MustCompile(`<div id="statusLog"[^>]*\btabindex="0"`)},
		{"connection indicator", regexp.MustCompile(`<span id="connStatus"[^>]*\brole="status"`)},
		{"error banner", regexp.MustCompile(`<div id="errorBanner"[^>]*\brole="alert"`)},
		{"progress bar (FR-offline-web-ui-28)", regexp.MustCompile(`id="jobBar"[^>]*role="progressbar" aria-valuenow="0" aria-valuemin="0" aria-valuemax="100"`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.want.MatchString(index) {
				t.Errorf("index.html does not match %s", tt.want)
			}
		})
	}
	if !strings.Contains(methodBody(t, appJS(t), "updateDownloadProgress"), "bar.setAttribute('aria-valuenow'") {
		t.Error("updateDownloadProgress does not set aria-valuenow on the progress bar (FR-offline-web-ui-28)")
	}
}

func TestAppJSNamesTheGeomanToolbarButtons(t *testing.T) {
	// Geoman puts the title on the button container and none on the link, so the link has no accessible name.
	geoman, err := staticFS.ReadFile("static/vendor/leaflet-geoman/leaflet-geoman.min.js")
	if err != nil {
		t.Fatalf("read leaflet-geoman.min.js: %v", err)
	}
	for _, want := range []string{`e.title&&r.setAttribute("title",e.title)`, `"leaflet-buttons-control-button",r`} {
		if !strings.Contains(string(geoman), want) {
			t.Errorf("leaflet-geoman.min.js does not contain %q, so the selector in initDrawControls is stale", want)
		}
	}
	draw := methodBody(t, appJS(t), "initDrawControls")
	for _, want := range []string{
		"querySelectorAll('.button-container[title] > .leaflet-buttons-control-button')",
		"setAttribute('aria-label', button.parentElement.title)",
	} {
		if !strings.Contains(draw, want) {
			t.Errorf("initDrawControls does not contain %q (FR-offline-web-ui-26)", want)
		}
	}
	// The toolbar exists only after addControls, so the names come after it.
	if strings.Index(draw, "setAttribute('aria-label'") < strings.Index(draw, "this.map.pm.addControls(") {
		t.Error("initDrawControls names the toolbar buttons before Geoman creates them")
	}
}

func TestAppJSFollowsTheSystemThemeUntilTheUserSelectsOne(t *testing.T) {
	app := appJS(t)
	initTheme := methodBody(t, app, "initTheme")
	toggleTheme := methodBody(t, app, "toggleTheme")
	for _, want := range []string{
		"matchMedia('(prefers-color-scheme: dark)')",
		"addEventListener('change'",
		"localStorage.getItem('theme')",
	} {
		if !strings.Contains(initTheme, want) {
			t.Errorf("initTheme does not contain %q (FR-offline-web-ui-29)", want)
		}
	}
	if !strings.Contains(toggleTheme, "localStorage.setItem('theme'") {
		t.Error("toggleTheme does not store the theme that the user selects")
	}
	// A browser can block storage, and the page must still load, so each storage call is in a try block.
	for name, body := range map[string]string{"initTheme": initTheme, "toggleTheme": toggleTheme} {
		if !strings.Contains(body, "try {") {
			t.Errorf("%s calls localStorage outside a try block", name)
		}
	}
	if n, inTheme := strings.Count(app, "localStorage."), strings.Count(initTheme+toggleTheme, "localStorage."); n != inTheme {
		t.Errorf("app.js calls localStorage %d times, and only %d calls are in initTheme and toggleTheme", n, inTheme)
	}
}

func TestDarkThemeFiltersNoMapTileAndNoPreviewTile(t *testing.T) {
	// A filter in the dark theme changes the tile pixels, and the user then sees colors the output file does not hold.
	filter := regexp.MustCompile(`(?i)(?:^|[\s;{])(?:-webkit-)?filter\s*:|--[\w-]*filter`)
	for name, body := range ownFiles(t) {
		if strings.HasSuffix(name, ".css") {
			if m := filter.Find(body); m != nil {
				t.Errorf("%s sets a CSS filter %q (FR-offline-web-ui-30)", name, m)
			}
		}
	}
	if strings.Contains(appJS(t), ".style.filter") {
		t.Error("app.js sets a CSS filter (FR-offline-web-ui-30)")
	}
}

// staticHTML is the HTML that app.js may insert: string literals with no interpolation and the icon constants.
var staticHTML = regexp.MustCompile("^(?:'[^'\\\\]*'|`[^`$\\\\]*`|ICON_DOWNLOAD|ICON_CANCEL|SPINNER)(?:\\s*\\+\\s*(?:'[^'\\\\]*'|`[^`$\\\\]*`|ICON_DOWNLOAD|ICON_CANCEL|SPINNER))*$")

func TestStaticHTMLAcceptsOnlyLiteralsAndIconConstants(t *testing.T) {
	tests := []struct {
		expr string
		want bool
	}{
		{`''`, true},
		{`'<option value="">Select a map source...</option>'`, true},
		{`SPINNER`, true},
		{`ICON_CANCEL + ' Cancel'`, true},
		{"`<strong>Selected Area:</strong>`", true},
		{"`North: ${bounds.getNorth()}`", false},
		{`'<strong>' + first.display_name + '</strong>'`, false},
		{`previewData.source`, false},
		{`ICON_CANCEL + label`, false},
		{`'a' + 'b'.concat(name)`, false},
	}
	for _, tt := range tests {
		if got := staticHTML.MatchString(tt.expr); got != tt.want {
			t.Errorf("staticHTML.MatchString(%s) = %v, want %v", tt.expr, got, tt.want)
		}
	}
}

func TestAppJSInsertsNoServerOrUserStringAsHTML(t *testing.T) {
	app := appJS(t)
	// The statement ends at a semicolon at the end of a line, so a template literal over many lines is one value.
	sinks := regexp.MustCompile(`(?s)\.(?:innerHTML|outerHTML)\s*=\s*(.*?);\s*\n|\.insertAdjacentHTML\(\s*'[a-z]+',\s*(.*?)\);\s*\n`)
	found := sinks.FindAllStringSubmatch(app, -1)
	if len(found) == 0 {
		t.Fatal("app.js has no HTML sink, so the pattern is stale")
	}
	for _, m := range found {
		expr := strings.TrimSpace(m[1] + m[2])
		if !staticHTML.MatchString(expr) {
			t.Errorf("app.js inserts %q as HTML, so a server or user string can become markup (FR-offline-web-ui-31)", expr)
		}
	}
	// An append with += or a sink that the pattern does not parse would escape the check, so the counts must agree.
	if n := len(regexp.MustCompile(`\.(?:innerHTML|outerHTML|insertAdjacentHTML)\b`).FindAllString(app, -1)); n != len(found) {
		t.Errorf("app.js has %d HTML sinks, and the check parsed %d of them", n, len(found))
	}
	// Leaflet sets a string popup as innerHTML and appends an element, so a popup takes a DOM node only.
	popups := strings.Count(app, "bindPopup(")
	if nodes := len(regexp.MustCompile(`bindPopup\(\s*[A-Za-z_$][\w$]*\s*\)`).FindAllString(app, -1)); nodes != popups {
		t.Errorf("app.js calls bindPopup %d times, and only %d calls pass a DOM node (FR-offline-web-ui-31)", popups, nodes)
	}
}

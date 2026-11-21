package web

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// response is one fake server response of a page harness. Reject makes the request fail as if the server is unreachable.
// Pending makes the request never settle.
type response struct {
	Status  int  `json:"status,omitempty"`
	Body    any  `json:"body,omitempty"`
	Reject  bool `json:"reject,omitempty"`
	Pending bool `json:"pending,omitempty"`
}

// jobsStep is one step of the user in testdata/jobs_page.js.
type jobsStep struct {
	Do       string    `json:"do"`
	Row      string    `json:"row,omitempty"`
	Text     string    `json:"text,omitempty"`
	Element  string    `json:"element,omitempty"`
	Name     string    `json:"name,omitempty"`
	Data     any       `json:"data,omitempty"`
	Key      string    `json:"key,omitempty"`
	Response *response `json:"response,omitempty"`
}

// jobsScenario is the input of testdata/jobs_page.js.
type jobsScenario struct {
	Responses map[string]response `json:"responses"`
	Steps     []jobsStep          `json:"steps,omitempty"`
}

// jobAction is one action control of a job row.
type jobAction struct {
	Text string `json:"text"`
	Href string `json:"href"`
}

// jobRow is one table row of the Jobs page.
type jobRow struct {
	ID      string      `json:"id"`
	Cells   []string    `json:"cells"`
	Actions []jobAction `json:"actions"`
}

// jobsPage is the state of the Jobs page that jobs_page.js prints.
type jobsPage struct {
	Queue    []jobRow `json:"queue"`
	History  []jobRow `json:"history"`
	Loading  bool     `json:"loading"`
	Empty    bool     `json:"empty"`
	Tables   bool     `json:"tables"`
	Error    string   `json:"error"`
	Requests []string `json:"requests"`
	Location string   `json:"location"`
	Timers   []int    `json:"timers"`
	Streams  []struct {
		URL    string `json:"url"`
		Closed bool   `json:"closed"`
	} `json:"streams"`
	Drawer struct {
		Open   bool     `json:"open"`
		Title  string   `json:"title"`
		Fields []string `json:"fields"`
		Log    []string `json:"log"`
	} `json:"drawer"`
	Footer string `json:"footer"`
	Status string `json:"status"`
}

// runNodeHarness runs the harness script in Node with the page, the page script and the scenario s, and decodes its output into out.
func runNodeHarness(t *testing.T, harness, page, script string, s, out any) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed, so the page script cannot run")
	}
	in, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	// #nosec G204 -- the arguments are test fixture paths and a JSON scenario of the test.
	cmd := exec.CommandContext(t.Context(), node, filepath.Join("testdata", harness), page, filepath.Join("static", "js", script), string(in))
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("node %s: %v\n%s", harness, err, stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), out); err != nil {
		t.Fatalf("decode %q: %v", stdout.String(), err)
	}
}

// runJobsPage runs jobs.js against the scenario s and returns the page.
func runJobsPage(t *testing.T, s jobsScenario) jobsPage {
	t.Helper()
	var got jobsPage
	runNodeHarness(t, "jobs_page.js", "jobs.html", "jobs.js", s, &got)
	return got
}

// record returns a job record in state with the id, and the fields of extra on top.
func record(id, state string, extra map[string]any) map[string]any {
	r := map[string]any{
		"id": id, "source": "OpenTopoMap", "bounds": []float64{-122, 45, -121.5, 45.5},
		"min_zoom": 8, "max_zoom": 15, "state": state, "output_file": id + ".mbtiles",
		"total": 100, "downloaded": 50, "skipped": 12, "failed": 0,
		"created_at": "2026-11-21T14:02:00Z", "started_at": nil, "finished_at": nil, "error": "",
	}
	for k, v := range extra {
		r[k] = v
	}
	return r
}

// listed returns the responses of a server whose job list is jobs and whose output files are files.
func listed(jobs []map[string]any, files ...string) map[string]response {
	items := []map[string]any{}
	for _, f := range files {
		items = append(items, map[string]any{"name": f, "size": 1})
	}
	return map[string]response{
		"GET /api/jobs?page=1": {Status: 200, Body: map[string]any{"jobs": jobs, "page": 1, "pages": 1}},
		"GET /api/files":       {Status: 200, Body: items},
		"GET /api/config":      {Status: 200, Body: map[string]any{"geocode": true, "max_tiles": 1000, "addr": "0.0.0.0:5000", "data_dir": "/data", "workers": 2}},
	}
}

func TestJobsPageSplitsTheListIntoTheQueueAndTheHistory(t *testing.T) {
	got := runJobsPage(t, jobsScenario{Responses: listed([]map[string]any{
		record("aaaaaa111", "queued", map[string]any{"position": 1}),
		record("bbbbbb222", "running", nil),
		record("cccccc333", "complete", nil),
		record("dddddd444", "failed", nil),
		record("eeeeee555", "cancelled", nil),
	})})
	ids := func(rows []jobRow) []string {
		out := []string{}
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}
	if want := []string{"aaaaaa111", "bbbbbb222"}; !slices.Equal(ids(got.Queue), want) {
		t.Errorf("queue = %q, want %q", ids(got.Queue), want)
	}
	if want := []string{"cccccc333", "dddddd444", "eeeeee555"}; !slices.Equal(ids(got.History), want) {
		t.Errorf("history = %q, want %q", ids(got.History), want)
	}
	if !got.Tables || got.Empty || got.Loading || got.Error != "" {
		t.Errorf("tables %v, empty %v, loading %v, error %q, want only the tables", got.Tables, got.Empty, got.Loading, got.Error)
	}
}

func TestJobsPageRowShowsTheFieldsOfFRTeamServer26(t *testing.T) {
	got := runJobsPage(t, jobsScenario{Responses: listed([]map[string]any{
		record("a3f91c7d", "running", map[string]any{"label": "Team 2", "total": 200, "downloaded": 100, "skipped": 24}),
	})})
	if len(got.Queue) != 1 {
		t.Fatalf("queue has %d rows, want 1", len(got.Queue))
	}
	cells := got.Queue[0].Cells
	// The cells are: short id, map source, area name or label, zoom range, state, progress, created time, actions.
	want := []string{"a3f91c", "OpenTopoMap", "Team 2", "8–15", "running", "62%"}
	if len(cells) != 8 {
		t.Fatalf("the row has %d cells, want 8: %q", len(cells), cells)
	}
	for i, w := range want {
		if cells[i] != w {
			t.Errorf("cell %d = %q, want %q", i, cells[i], w)
		}
	}
	if strings.TrimSpace(cells[6]) == "" {
		t.Error("the created time cell is empty")
	}
}

func TestJobsPageRowNamesTheJob(t *testing.T) {
	tests := []struct {
		name  string
		extra map[string]any
		want  string
	}{
		{"shows the label", map[string]any{"label": "Base layer v3"}, "Base layer v3"},
		{"shows a dash with no label", nil, "—"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runJobsPage(t, jobsScenario{Responses: listed([]map[string]any{record("abcdef12", "complete", tt.extra)})})
			if len(got.History) != 1 || got.History[0].Cells[2] != tt.want {
				t.Errorf("history = %+v, want the name cell %q", got.History, tt.want)
			}
		})
	}
}

func TestJobsPageShowsTheQueuePositionOfAQueuedJob(t *testing.T) {
	got := runJobsPage(t, jobsScenario{Responses: listed([]map[string]any{record("abcdef12", "queued", map[string]any{"position": 2})})})
	if len(got.Queue) != 1 || got.Queue[0].Cells[5] != "Position 2" {
		t.Errorf("queue = %+v, want the progress cell \"Position 2\"", got.Queue)
	}
}

func TestJobsPageActionsByState(t *testing.T) {
	download := jobAction{Text: "Download", Href: "/api/files/abcdef12.mbtiles"}
	tests := []struct {
		state string
		files []string
		want  []jobAction
	}{
		{"queued", nil, []jobAction{{Text: "Cancel"}}},
		{"running", []string{"abcdef12.mbtiles"}, []jobAction{{Text: "Cancel"}}},
		{"complete", []string{"abcdef12.mbtiles"}, []jobAction{download}},
		{"failed", []string{"abcdef12.mbtiles"}, []jobAction{{Text: "Resume"}, download}},
		{"failed", nil, []jobAction{{Text: "Resume"}}},
		{"cancelled", []string{"abcdef12.mbtiles"}, []jobAction{{Text: "Resume"}, download}},
		{"cancelled", []string{"other.mbtiles"}, []jobAction{{Text: "Resume"}}},
	}
	for _, tt := range tests {
		t.Run(tt.state+" with files "+strings.Join(tt.files, ","), func(t *testing.T) {
			got := runJobsPage(t, jobsScenario{Responses: listed([]map[string]any{record("abcdef12", tt.state, nil)}, tt.files...)})
			rows := append(got.Queue, got.History...)
			if len(rows) != 1 {
				t.Fatalf("the page has %d rows, want 1", len(rows))
			}
			if !slices.Equal(rows[0].Actions, tt.want) {
				t.Errorf("actions = %+v, want %+v", rows[0].Actions, tt.want)
			}
		})
	}
}

func TestJobsPageActionsSendTheirRequestAndReloadTheList(t *testing.T) {
	tests := []struct {
		state   string
		action  string
		request string
	}{
		{"running", "Cancel", "DELETE /api/jobs/abcdef12"},
		{"queued", "Cancel", "DELETE /api/jobs/abcdef12"},
		{"failed", "Resume", "POST /api/jobs/abcdef12/resume"},
	}
	for _, tt := range tests {
		t.Run(tt.action+" a "+tt.state+" job", func(t *testing.T) {
			responses := listed([]map[string]any{record("abcdef12", tt.state, nil)})
			responses[tt.request] = response{Status: 202, Body: record("abcdef12", "queued", nil)}
			got := runJobsPage(t, jobsScenario{Responses: responses, Steps: []jobsStep{{Do: "click", Row: "abcdef12", Text: tt.action}}})
			i := slices.Index(got.Requests, tt.request)
			if i < 0 {
				t.Fatalf("requests = %q, want %q", got.Requests, tt.request)
			}
			if !slices.Contains(got.Requests[i+1:], "GET /api/jobs?page=1") {
				t.Errorf("requests = %q, want the list again after %q", got.Requests, tt.request)
			}
		})
	}
}

func TestJobsPageShowsTheErrorOfAFailedAction(t *testing.T) {
	responses := listed([]map[string]any{record("abcdef12", "complete", nil)})
	responses["POST /api/jobs/abcdef12/resume"] = response{Status: 409, Body: map[string]any{"error": map[string]any{"code": "invalid_state", "message": "Job is complete"}}}
	responses["GET /api/jobs?page=1"] = response{Status: 200, Body: map[string]any{"jobs": []any{record("abcdef12", "failed", nil)}, "page": 1, "pages": 1}}
	got := runJobsPage(t, jobsScenario{Responses: responses, Steps: []jobsStep{{Do: "click", Row: "abcdef12", Text: "Resume"}}})
	if !strings.Contains(got.Error, "Job is complete") {
		t.Errorf("error = %q, want the message of the server", got.Error)
	}
}

func TestJobsPageStates(t *testing.T) {
	failed := map[string]response{"GET /api/jobs?page=1": {Status: 500, Body: map[string]any{"error": map[string]any{"code": "internal", "message": "Store failed"}}}}
	unreachable := map[string]response{"GET /api/jobs?page=1": {Reject: true}}
	tests := []struct {
		name      string
		responses map[string]response
		loading   bool
		empty     bool
		tables    bool
		error     string
	}{
		{"loading shows the skeleton rows", map[string]response{"GET /api/jobs?page=1": {Pending: true}}, true, false, false, ""},
		{"empty shows No jobs yet", listed([]map[string]any{}), false, true, false, ""},
		{"populated shows the tables", listed([]map[string]any{record("abcdef12", "complete", nil)}), false, false, true, ""},
		{"error shows the banner", failed, false, false, false, "Store failed"},
		{"unreachable shows the banner", unreachable, false, false, false, "Server unreachable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runJobsPage(t, jobsScenario{Responses: tt.responses})
			if got.Loading != tt.loading || got.Empty != tt.empty || got.Tables != tt.tables {
				t.Errorf("loading %v, empty %v, tables %v, want %v, %v, %v", got.Loading, got.Empty, got.Tables, tt.loading, tt.empty, tt.tables)
			}
			if !strings.Contains(got.Error, tt.error) || (tt.error == "") != (got.Error == "") {
				t.Errorf("error = %q, want %q", got.Error, tt.error)
			}
		})
	}
}

func TestJobsPageRetryLoadsTheListAgain(t *testing.T) {
	responses := listed(nil)
	responses["GET /api/jobs?page=1"] = response{Reject: true}
	got := runJobsPage(t, jobsScenario{
		Responses: responses,
		Steps: []jobsStep{
			{Do: "respond", Key: "GET /api/jobs?page=1", Response: &response{Status: 200, Body: map[string]any{"jobs": []any{record("abcdef12", "complete", nil)}, "page": 1, "pages": 1}}},
			{Do: "click", Element: "errorRetry"},
		},
	})
	if got.Error != "" || len(got.History) != 1 {
		t.Errorf("error %q, history %+v, want no error and one row after Retry", got.Error, got.History)
	}
}

func TestJobsPageEmptyStateLinksToTheWorkspace(t *testing.T) {
	page := string(JobsHTML())
	if !regexp.MustCompile(`(?s)<div id="jobsEmpty"[^>]*>.*?No jobs yet.*?<a href="/"`).MatchString(page) {
		t.Error("jobs.html has no empty state with \"No jobs yet\" and a link to /")
	}
}

func TestJobsPageGoesToSigninOn401(t *testing.T) {
	unauthorized := response{Status: 401, Body: map[string]any{"error": map[string]any{"code": "unauthorized", "message": "A valid token is required"}}}
	got := runJobsPage(t, jobsScenario{Responses: map[string]response{"GET /api/jobs?page=1": unauthorized, "GET /api/config": unauthorized}})
	if got.Location != "/signin" {
		t.Errorf("location = %q, want /signin", got.Location)
	}
	if got.Error != "" {
		t.Errorf("error = %q, want no banner before the redirect", got.Error)
	}
}

func TestJobsPageRefreshesEvery5SecondsWhileAJobIsActive(t *testing.T) {
	tests := []struct {
		name   string
		states []string
		want   []int
	}{
		{"queued", []string{"queued", "complete"}, []int{5000}},
		{"running", []string{"running"}, []int{5000}},
		{"no active job", []string{"complete", "failed", "cancelled"}, []int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobs := []map[string]any{}
			for i, s := range tt.states {
				jobs = append(jobs, record(strings.Repeat(string(rune('a'+i)), 8), s, nil))
			}
			got := runJobsPage(t, jobsScenario{Responses: listed(jobs)})
			if !slices.Equal(got.Timers, tt.want) {
				t.Errorf("timers = %v, want %v", got.Timers, tt.want)
			}
		})
	}
	// A tick reloads the list, and the reload sets the next timer.
	got := runJobsPage(t, jobsScenario{Responses: listed([]map[string]any{record("abcdef12", "running", nil)}), Steps: []jobsStep{{Do: "tick"}}})
	if n := strings.Count(strings.Join(got.Requests, "\n"), "GET /api/jobs?page=1"); n != 2 {
		t.Errorf("the page sent GET /api/jobs %d times, want 2: %q", n, got.Requests)
	}
	if !slices.Equal(got.Timers, []int{5000}) {
		t.Errorf("timers after the tick = %v, want [5000]", got.Timers)
	}
}

func TestJobsPageSignOutDeletesTheSession(t *testing.T) {
	responses := listed([]map[string]any{})
	responses["DELETE /api/session"] = response{Status: 204}
	got := runJobsPage(t, jobsScenario{Responses: responses, Steps: []jobsStep{{Do: "click", Element: "signOut"}}})
	if !slices.Contains(got.Requests, "DELETE /api/session") {
		t.Errorf("requests = %q, want DELETE /api/session", got.Requests)
	}
	if got.Location != "/signin" {
		t.Errorf("location = %q, want /signin", got.Location)
	}
}

func TestJobsDrawerShowsTheRecordAndTheEventLog(t *testing.T) {
	got := runJobsPage(t, jobsScenario{
		Responses: listed([]map[string]any{record("abcdef12", "running", map[string]any{"label": "Team 2"})}),
		Steps: []jobsStep{
			{Do: "open", Row: "abcdef12"},
			{Do: "event", Name: "state", Data: record("abcdef12", "running", map[string]any{"label": "Team 2"})},
			{Do: "event", Name: "log", Data: map[string]any{"level": "warning", "message": "429 from the tile server"}},
			{Do: "event", Name: "log", Data: map[string]any{"level": "info", "message": "<b>not markup</b>"}},
		},
	})
	if !got.Drawer.Open || got.Drawer.Title != "Job abcdef" {
		t.Errorf("drawer open %v, title %q, want open with \"Job abcdef\"", got.Drawer.Open, got.Drawer.Title)
	}
	if len(got.Streams) != 1 || got.Streams[0].URL != "/api/jobs/abcdef12/events" || got.Streams[0].Closed {
		t.Errorf("streams = %+v, want one open stream of /api/jobs/abcdef12/events", got.Streams)
	}
	fields := strings.Join(got.Drawer.Fields, "\n")
	for _, want := range []string{"abcdef12", "OpenTopoMap", "Team 2", "8–15", "running", "abcdef12.mbtiles", "50"} {
		if !strings.Contains(fields, want) {
			t.Errorf("drawer fields %q do not contain %q", got.Drawer.Fields, want)
		}
	}
	if len(got.Drawer.Log) != 2 || !strings.Contains(got.Drawer.Log[0], "429 from the tile server") || !strings.Contains(got.Drawer.Log[1], "<b>not markup</b>") {
		t.Errorf("drawer log = %q, want the two log events as text", got.Drawer.Log)
	}
}

func TestJobsDrawerClosesEventSource(t *testing.T) {
	jobs := []map[string]any{record("abcdef12", "running", nil)}
	tests := []struct {
		name  string
		steps []jobsStep
		open  bool
	}{
		{"when the drawer closes", []jobsStep{{Do: "open", Row: "abcdef12"}, {Do: "click", Element: "drawerClose"}}, false},
		{"when the state is final", []jobsStep{{Do: "open", Row: "abcdef12"}, {Do: "event", Name: "state", Data: record("abcdef12", "complete", nil)}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runJobsPage(t, jobsScenario{Responses: listed(jobs), Steps: tt.steps})
			if len(got.Streams) != 1 || !got.Streams[0].Closed {
				t.Errorf("streams = %+v, want one closed stream", got.Streams)
			}
			if got.Drawer.Open != tt.open {
				t.Errorf("drawer open = %v, want %v", got.Drawer.Open, tt.open)
			}
		})
	}
	// A second job in the drawer replaces the stream of the first.
	two := []map[string]any{record("abcdef12", "running", nil), record("bcdefa23", "queued", nil)}
	got := runJobsPage(t, jobsScenario{Responses: listed(two), Steps: []jobsStep{{Do: "open", Row: "abcdef12"}, {Do: "open", Row: "bcdefa23"}}})
	if len(got.Streams) != 2 || !got.Streams[0].Closed || got.Streams[1].Closed {
		t.Errorf("streams = %+v, want the first closed and the second open", got.Streams)
	}
}

func TestJobsFooterShowsTheServerConfig(t *testing.T) {
	responses := listed([]map[string]any{})
	responses["GET /api/config"] = response{Status: 200, Body: map[string]any{"version": "2.0.0", "addr": "0.0.0.0:5000", "data_dir": "/data", "workers": 2}}
	got := runJobsPage(t, jobsScenario{Responses: responses})
	for _, want := range []string{"2.0.0", "0.0.0.0:5000", "/data", "2 workers"} {
		if !strings.Contains(got.Footer, want) {
			t.Errorf("footer = %q, want %q", got.Footer, want)
		}
	}
}

func pageScript(t *testing.T, name string) string {
	t.Helper()
	b, err := staticFS.ReadFile("static/js/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func TestJobsPageUsesTextContent(t *testing.T) {
	// The page scripts build every element, so no HTML sink is needed and none is allowed (FR-offline-web-ui-31).
	sink := regexp.MustCompile(`\.(?:innerHTML|outerHTML|insertAdjacentHTML)\b|document\.write`)
	for _, name := range []string{"jobs.js", "signin.js", "theme.js"} {
		if m := sink.FindString(pageScript(t, name)); m != "" {
			t.Errorf("%s uses the HTML sink %q", name, m)
		}
	}
}

func TestEveryElementIDThatAPageScriptReadsIsInItsPage(t *testing.T) {
	for script, page := range map[string]string{"jobs.js": string(JobsHTML()), "signin.js": string(SigninHTML())} {
		reads := regexp.MustCompile(`getElementById\('([^']+)'\)`).FindAllStringSubmatch(pageScript(t, script), -1)
		if len(reads) == 0 {
			t.Fatalf("%s reads no element id, so the pattern is stale", script)
		}
		for _, m := range reads {
			if !strings.Contains(page, `id="`+m[1]+`"`) {
				t.Errorf("%s reads the element id %q, which its page does not have", script, m[1])
			}
		}
	}
}

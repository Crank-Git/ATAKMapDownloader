package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// jobSettings are the ids of the controls that a running job makes read-only (feature 08, downloading state).
// The map source picker is not in the list, because an empty source list keeps it disabled too.
var jobSettings = []string{
	"minZoom", "maxZoom", "customName", "maxConcurrent",
	"failureTolerance", "ignoreErrors", "verifyTLS", "ifExists",
	// A running job keeps its area list, so the area tools are job settings too (FR-area-selection-19).
	"drawRectangle", "drawPolygon", "drawRoute", "importFile", "routeBuffer", "clearSelection",
}

func TestAppJSKeepsARunningJobReadOnly(t *testing.T) {
	app := appJS(t)
	lock := methodBody(t, app, "setJobLocked")
	if !strings.Contains(lock, "getElementById('estimateBtn').disabled = locked") {
		t.Error("setJobLocked does not disable Estimate")
	}
	for _, id := range jobSettings {
		if !regexp.MustCompile(`JOB_SETTINGS = \[[^\]]*'` + id + `'`).MatchString(app) {
			t.Errorf("JOB_SETTINGS does not name the setting %q", id)
		}
	}
	for _, want := range []string{
		"JOB_SETTINGS.forEach",
		"getElementById('mapSourceSelect').disabled = locked || this.mapSources.length === 0",
	} {
		if !strings.Contains(lock, want) {
			t.Errorf("setJobLocked does not contain %q", want)
		}
	}
	tests := []struct {
		method string
		want   string
	}{
		{"setStartEnabled", "|| this.isDownloading"},
		{"startDownload", "if (this.isDownloading)"},
		{"startDownload", "this.setJobLocked(true)"},
		{"estimateDownload", "if (this.isDownloading) return;"},
		{"estimateDownload", "button.disabled = this.isDownloading"},
		{"enableEstimation", "getElementById('estimateBtn').disabled = this.isDownloading"},
		{"loadMapSources", "select.disabled = this.mapSources.length === 0 || this.isDownloading"},
		// A rejected start resets nothing of a job that the panel shows.
		{"resetDownloadUI", "if (jobId !== this.jobId) return;"},
		{"resetDownloadUI", "this.setJobLocked(false)"},
	}
	for _, tt := range tests {
		if !strings.Contains(methodBody(t, app, tt.method), tt.want) {
			t.Errorf("%s does not contain %q", tt.method, tt.want)
		}
	}
	if strings.Contains(app, "this.resetDownloadUI()") {
		t.Error("app.js calls resetDownloadUI with no job id, so the call can hide the panel of a running job")
	}
	// A POST before the job id arrives must lock the settings, so the lock comes before the request.
	start := methodBody(t, app, "startDownload")
	if strings.Index(start, "this.setJobLocked(true)") > strings.Index(start, "this.postJSON('/api/jobs'") {
		t.Error("startDownload sends POST /api/jobs before it locks the settings")
	}
}

func TestResetDownloadUIEnablesStartOnlyWithAnAreaAndAMapSource(t *testing.T) {
	reset := methodBody(t, appJS(t), "resetDownloadUI")
	if !strings.Contains(reset, "this.setStartEnabled(Boolean(this.areas.length > 0 && document.getElementById('mapSourceSelect').value))") {
		t.Error("resetDownloadUI does not check the area and the map source before it enables Start")
	}
	if strings.Contains(reset, "this.setStartEnabled(true)") {
		t.Error("resetDownloadUI enables Start with no area or no map source")
	}
}

func TestAppJSLoadsTheOutputFilesThroughTheSharedRequestPath(t *testing.T) {
	app := appJS(t)
	load := methodBody(t, app, "loadOutputFiles")
	for _, want := range []string{
		"this.requestJSON('/api/files')",
		"this.hideError('files')",
		"this.showError('files'",
	} {
		if !strings.Contains(load, want) {
			t.Errorf("loadOutputFiles does not contain %q", want)
		}
	}
	if strings.Contains(load, "fetch(") {
		t.Error("loadOutputFiles calls fetch, so the connection indicator does not see the request")
	}
	// The list marks the file of the running job in_progress only after a refresh at the start of the job.
	running := regexp.MustCompile(`(?s)case 'running':.*?break;`).FindString(methodBody(t, app, "onJobState"))
	if !strings.Contains(running, "this.loadOutputFiles()") {
		t.Error("onJobState does not refresh the output file list when a job runs")
	}
}

func TestAppJSOnJobCompleteTakesNoParameter(t *testing.T) {
	if !strings.HasPrefix(methodBody(t, appJS(t), "onJobComplete"), "    onJobComplete() {") {
		t.Error("onJobComplete declares a parameter that it does not use")
	}
}

func TestAppJSSetsThePolicyLinkOnlyToAnHTTPURL(t *testing.T) {
	start := methodBody(t, appJS(t), "startDownload")
	for _, want := range []string{"new URL(source.policy_reference_url)", "protocol === 'http:'", "protocol === 'https:'"} {
		if !strings.Contains(start, want) {
			t.Errorf("startDownload does not contain %q", want)
		}
	}
	// A javascript: URL in a source file would run as script on a click.
	if strings.Contains(start, "link.href = source.policy_reference_url") {
		t.Error("startDownload sets the policy link to the reference URL with no check of its scheme")
	}
}

// vendorBudget is the size budget of the vendored assets, 520 KB (feature 08, behaviour rules; PM ruling on #31).
const vendorBudget = 520 * 1024

func TestVendoredAssetsStayWithinTheSizeBudget(t *testing.T) {
	var total int64
	err := fs.WalkDir(staticFS, "static/vendor", func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		t.Fatalf("walk static/vendor: %v", err)
	}
	if total == 0 {
		t.Fatal("static/vendor holds no file, so the walk is stale")
	}
	if total > vendorBudget {
		t.Errorf("the vendored assets total %d bytes, the budget is %d", total, vendorBudget)
	}
}

package commands

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"

	"github.com/crank-git/atak-map-downloader/internal/areafile"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/spf13/cobra"
)

// errAreaAndBBox is the error for a command with both --area and --bbox (FR-area-selection-16).
var errAreaAndBBox = errors.New("--area and --bbox cannot be used together: use one of them")

// areaOrBBox returns the --area and --bbox flags of cmd.
// It returns errAreaAndBBox when both are set (FR-area-selection-16).
func areaOrBBox(cmd *cobra.Command) (areaFile, bbox string, err error) {
	if areaFile, err = cmd.Flags().GetString("area"); err != nil {
		return "", "", fmt.Errorf("read --area: %w", err)
	}
	if bbox, err = cmd.Flags().GetString("bbox"); err != nil {
		return "", "", fmt.Errorf("read --bbox: %w", err)
	}
	if areaFile != "" && bbox != "" {
		return "", "", errAreaAndBBox
	}
	return areaFile, bbox, nil
}

// readAreaFile returns the areas of the --area file at path, and prints a warning for each skipped Point feature (FR-area-selection-18).
// A feature with no zoom property gets minZoom and maxZoom, and a line with no buffer_m property gets --buffer.
// It returns the error of areafile.Read. It does not validate the areas.
func readAreaFile(cmd *cobra.Command, path string, minZoom, maxZoom int) ([]tilecalc.JobArea, error) {
	buffer, err := cmd.Flags().GetFloat64("buffer")
	if err != nil {
		return nil, fmt.Errorf("read --buffer: %w", err)
	}
	// The flags are valid in form, so the usage text does not help with a file error.
	cmd.SilenceUsage = true
	res, err := areafile.Read(path, areafile.Defaults{MinZoom: minZoom, MaxZoom: maxZoom, BufferM: buffer})
	if err != nil {
		return nil, fmt.Errorf("read --area %s: %w", path, err)
	}
	for _, w := range res.Warnings() {
		if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+w); err != nil {
			return nil, fmt.Errorf("print the area file warning: %w", err)
		}
	}
	return res.Areas, nil
}

// parseRouteBodyBytes is the largest body of POST /api/areas/parse.
// A multipart form adds headers and boundaries to the file, so the limit is 64 KiB above the file limit.
const parseRouteBodyBytes = areafile.MaxBytes + 64<<10

// areasParseResult is the POST /api/areas/parse response.
// Areas uses the area shape of POST /api/estimate and POST /api/jobs (FR-area-selection-25).
type areasParseResult struct {
	Areas    []areaRequest `json:"areas"`
	Warnings []string      `json:"warnings"`
}

// apiAreasParse returns the areas of a GeoJSON, KML or KMZ file, so the web UI has no second parser.
// The body is a multipart form with the field file, or the file itself.
// For a multipart form, the type comes from the file name extension, and for a file body, from its first bytes.
// The query parameters min_zoom, max_zoom and buffer_m set the values that the file does not set, and an absent one gives 0.
// It writes these errors:
//   - 400 invalid_query for a query parameter that is not a number.
//   - 400 invalid_area_file when the file does not decode or holds no polygon and no line.
//   - 413 body_too_large when the file is larger than 5 MiB.
//
// It does not validate the areas, because POST /api/estimate does.
func (h *WebHandler) apiAreasParse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	d, err := parseDefaults(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}
	format, data, err := areaFileBody(r)
	var res areafile.Result
	if err == nil {
		res, err = areafile.Parse(format, data, d)
	}
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		writeBodyTooLarge(w, tooLarge.Limit)
		return
	case errors.Is(err, areafile.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", err.Error())
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, "invalid_area_file", err.Error())
		return
	}
	out := areasParseResult{Areas: make([]areaRequest, 0, len(res.Areas)), Warnings: res.Warnings()}
	for _, a := range res.Areas {
		geometry, err := tilecalc.EncodeGeometry(a.Geometry)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		out.Areas = append(out.Areas, areaRequest{
			Kind: a.Kind, Geometry: geometry, MinZoom: a.MinZoom, MaxZoom: a.MaxZoom, BufferM: a.BufferM, Name: a.Name,
		})
	}
	writeJSON(w, out)
}

// parseDefaults returns the area defaults of the min_zoom, max_zoom and buffer_m query parameters.
// It returns an error for a parameter that is not a number.
func parseDefaults(q url.Values) (areafile.Defaults, error) {
	var d areafile.Defaults
	for _, p := range []struct {
		name string
		set  func(string) error
	}{
		{"min_zoom", func(v string) (err error) { d.MinZoom, err = strconv.Atoi(v); return err }},
		{"max_zoom", func(v string) (err error) { d.MaxZoom, err = strconv.Atoi(v); return err }},
		{"buffer_m", func(v string) (err error) { d.BufferM, err = strconv.ParseFloat(v, 64); return err }},
	} {
		if v := q.Get(p.name); v != "" {
			if err := p.set(v); err != nil {
				return d, fmt.Errorf("query parameter %s %q is not a number", p.name, v)
			}
		}
	}
	return d, nil
}

// areaFileBody returns the area file of a POST /api/areas/parse request and its format.
// It returns at most areafile.MaxBytes+1 bytes of the file, so that areafile.Parse can report ErrTooLarge.
func areaFileBody(r *http.Request) (areafile.Format, []byte, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		// A body with no multipart Content-Type is the file itself, so curl can send it with --data-binary.
		data, err := io.ReadAll(io.LimitReader(r.Body, areafile.MaxBytes+1))
		if err != nil {
			return 0, nil, fmt.Errorf("read the body: %w", err)
		}
		format, err := areafile.Sniff(data)
		return format, data, err
	}
	mr, err := r.MultipartReader()
	if err != nil {
		return 0, nil, fmt.Errorf("read the multipart form: %w", err)
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return 0, nil, errors.New("the multipart form has no file field")
		}
		if err != nil {
			return 0, nil, fmt.Errorf("read the multipart form: %w", err)
		}
		if part.FormName() != "file" {
			continue
		}
		format, err := areafile.FormatOf(part.FileName())
		if err != nil {
			return 0, nil, err
		}
		data, err := io.ReadAll(io.LimitReader(part, areafile.MaxBytes+1))
		if err != nil {
			return 0, nil, fmt.Errorf("read the file field: %w", err)
		}
		return format, data, nil
	}
}

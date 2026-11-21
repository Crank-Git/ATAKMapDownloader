package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
)

// defaultGeocoderURL is the base URL of the public Nominatim (FR-polite-downloading-25).
const defaultGeocoderURL = "https://nominatim.openstreetmap.org"

const (
	// geocodeCacheTTL is the time that a geocode result stays in the cache (FR-polite-downloading-23).
	geocodeCacheTTL = 24 * time.Hour
	// geocodeCacheSize keeps the memory of the cache bounded for a server that runs for weeks.
	geocodeCacheSize = 1000
	// geocodeWait is the longest time that a geocode request waits for the rate limit (FR-polite-downloading-24).
	geocodeWait = 5 * time.Second
)

// geocodeLocation is one result in the POST /api/geocode response.
type geocodeLocation struct {
	DisplayName string   `json:"display_name"`
	Lat         float64  `json:"lat"`
	Lon         float64  `json:"lon"`
	Boundingbox []string `json:"boundingbox"`
	Type        string   `json:"type"`
	Class       string   `json:"class"`
}

// geocodeResponse is the POST /api/geocode response.
type geocodeResponse struct {
	Query   string            `json:"query"`
	Results []geocodeLocation `json:"results"`
	Count   int               `json:"count"`
}

// configResponse is the GET /api/config response. The web UI reads it to hide the search box.
type configResponse struct {
	Geocode bool `json:"geocode"`
}

// geocodeCache holds geocode results by normalized query. It is safe for concurrent use.
type geocodeCache struct {
	mu      sync.Mutex
	entries map[string]geocodeCacheEntry
	// now is the clock of the cache. A test moves it past the TTL.
	now func() time.Time
}

type geocodeCacheEntry struct {
	locations []geocodeLocation
	stored    time.Time
}

func newGeocodeCache() *geocodeCache {
	return &geocodeCache{entries: map[string]geocodeCacheEntry{}, now: time.Now}
}

// normalizeQuery returns the cache key of query: trimmed, in lower case, with each run of spaces made one space.
func normalizeQuery(query string) string {
	return strings.Join(strings.Fields(strings.ToLower(query)), " ")
}

// get returns the cached locations of key. It reports false when the cache has no entry or the entry is older than the TTL.
func (c *geocodeCache) get(key string) ([]geocodeLocation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || c.now().Sub(e.stored) > geocodeCacheTTL {
		return nil, false
	}
	return e.locations, true
}

// put stores locations under key.
// When the cache is full, it removes the expired entries, and then the oldest entry if no entry expired.
func (c *geocodeCache) put(key string, locations []geocodeLocation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if _, ok := c.entries[key]; !ok && len(c.entries) >= geocodeCacheSize {
		var oldestKey string
		var oldest time.Time
		for k, e := range c.entries {
			if now.Sub(e.stored) > geocodeCacheTTL {
				delete(c.entries, k)
				continue
			}
			if oldestKey == "" || e.stored.Before(oldest) {
				oldestKey, oldest = k, e.stored
			}
		}
		if len(c.entries) >= geocodeCacheSize {
			delete(c.entries, oldestKey)
		}
	}
	c.entries[key] = geocodeCacheEntry{locations: locations, stored: now}
}

func (c *geocodeCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// applyGeocodeFlags sets the geocode flags of the serve command cmd on h.
// It returns an error when --geocoder-url is not an absolute http or https URL.
func applyGeocodeFlags(cmd *cobra.Command, h *WebHandler) error {
	base, err := cmd.Flags().GetString("geocoder-url")
	if err != nil {
		return fmt.Errorf("read --geocoder-url: %w", err)
	}
	off, err := cmd.Flags().GetBool("no-geocode")
	if err != nil {
		return fmt.Errorf("read --no-geocode: %w", err)
	}
	u, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("read --geocoder-url %q: %w", base, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("read --geocoder-url %q: the URL must be an absolute http or https URL", base)
	}
	h.geocodeURL = strings.TrimRight(base, "/") + "/search"
	h.geocodeOff = off
	return nil
}

func (h *WebHandler) apiConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	writeJSON(w, configResponse{Geocode: !h.geocodeOff})
}

// apiGeocode answers a place search from the cache, or else from Nominatim within its usage policy.
// It returns 429 geocode_busy when the rate limit has no slot within geocodeWait (FR-polite-downloading-24).
// It returns 502 geocode_failed when Nominatim is unreachable or sends a bad response.
func (h *WebHandler) apiGeocode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	var req struct {
		Query string `json:"query"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	key := normalizeQuery(req.Query)
	if key == "" {
		writeError(w, http.StatusBadRequest, "invalid_query", "Query is required")
		return
	}
	locations, ok := h.geocodeCache.get(key)
	if !ok {
		waitCtx, cancel := context.WithTimeout(r.Context(), h.geocodeWait)
		err := h.geocodeLimiter.Wait(waitCtx)
		cancel()
		if err != nil {
			writeError(w, http.StatusTooManyRequests, "geocode_busy", "Place search is busy. Try again in a few seconds")
			return
		}
		locations, err = h.searchNominatim(r.Context(), req.Query)
		if err != nil {
			writeError(w, http.StatusBadGateway, "geocode_failed", "Geocoding failed: "+err.Error())
			return
		}
		h.geocodeCache.put(key, locations)
	}
	writeJSON(w, geocodeResponse{Query: req.Query, Results: locations, Count: len(locations)})
}

// searchNominatim returns the Nominatim results for query.
// It returns an error for a failed request, a status other than 200, or a body that does not decode.
func (h *WebHandler) searchNominatim(ctx context.Context, query string) ([]geocodeLocation, error) {
	apiURL := h.geocodeURL + "?q=" + url.QueryEscape(query) + "&format=json&limit=5&addressdetails=1"
	hr, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("make the request: %w", err)
	}
	hr.Header.Set("User-Agent", h.userAgent)
	resp, err := h.geocodeClient.Do(hr)
	if err != nil {
		return nil, fmt.Errorf("send the request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() // The body is read only, so a close error loses nothing.
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the geocoder returned status %d", resp.StatusCode)
	}
	var results []struct {
		DisplayName string   `json:"display_name"`
		Lat         string   `json:"lat"`
		Lon         string   `json:"lon"`
		Boundingbox []string `json:"boundingbox"`
		Type        string   `json:"type"`
		Class       string   `json:"class"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("decode the response: %w", err)
	}
	locations := []geocodeLocation{}
	for _, res := range results {
		// Nominatim sends lat and lon as JSON strings.
		lat, latErr := strconv.ParseFloat(res.Lat, 64)
		lon, lonErr := strconv.ParseFloat(res.Lon, 64)
		if latErr != nil || lonErr != nil {
			return nil, fmt.Errorf("the result has a coordinate that is not a number")
		}
		locations = append(locations, geocodeLocation{
			DisplayName: res.DisplayName,
			Lat:         lat,
			Lon:         lon,
			Boundingbox: res.Boundingbox,
			Type:        res.Type,
			Class:       res.Class,
		})
	}
	return locations, nil
}

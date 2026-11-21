// Package policy matches a map source to an entry in the embedded source policy table.
package policy

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
)

// Class is the terms-of-use class of a map source.
type Class string

// The three policy classes (FR-polite-downloading-3).
const (
	Open        Class = "open"
	Conditional Class = "conditional"
	Restricted  Class = "restricted"
)

// defaultRequestsPerSecond is the rate for a host with no entry (FR-polite-downloading-5).
const defaultRequestsPerSecond = 4

// Entry is one row of the source policy table.
type Entry struct {
	// HostPattern is "*.<domain>" for every subdomain of <domain>, or one exact host.
	HostPattern       string  `json:"host_pattern"`
	PolicyClass       Class   `json:"policy_class"`
	RequestsPerSecond float64 `json:"requests_per_second"`
	Reason            string  `json:"reason"`
	ReferenceURL      string  `json:"reference_url"`
	// Retrieved is the date, as YYYY-MM-DD, on which a person last read ReferenceURL.
	Retrieved string `json:"retrieved"`
}

//go:embed policy.json
var tableJSON []byte

var table []Entry

// A bad entry is a build defect, because the table ships in the binary, so init panics.
func init() {
	var err error
	if table, err = parse(tableJSON); err != nil {
		panic(err)
	}
}

// parse returns the entries of a policy table in JSON.
// It returns an error for an entry with no host pattern, an unknown class or a rate that is not positive.
func parse(data []byte) ([]Entry, error) {
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse policy table: %w", err)
	}
	for i, e := range entries {
		switch {
		case e.HostPattern == "":
			return nil, fmt.Errorf("policy entry %d has no host_pattern", i)
		case e.PolicyClass != Open && e.PolicyClass != Conditional && e.PolicyClass != Restricted:
			return nil, fmt.Errorf("policy entry %q has policy_class %q, want open, conditional or restricted", e.HostPattern, e.PolicyClass)
		case e.RequestsPerSecond <= 0:
			return nil, fmt.Errorf("policy entry %q has requests_per_second %v, want a positive number", e.HostPattern, e.RequestsPerSecond)
		}
	}
	return entries, nil
}

// Lookup returns the first table entry whose host pattern matches host (FR-polite-downloading-4).
// When no entry matches, it returns a conditional entry at 4 requests per second whose HostPattern is host.
func Lookup(host string) Entry {
	return lookup(table, host)
}

func lookup(entries []Entry, host string) Entry {
	host = strings.ToLower(host)
	for _, e := range entries {
		if matches(e.HostPattern, host) {
			return e
		}
	}
	return Entry{
		HostPattern:       host,
		PolicyClass:       Conditional,
		RequestsPerSecond: defaultRequestsPerSecond,
		Reason:            "No entry in the policy table matches this host, so the default rate applies.",
	}
}

func matches(pattern, host string) bool {
	if suffix, ok := strings.CutPrefix(pattern, "*."); ok {
		return strings.HasSuffix(host, "."+suffix)
	}
	return host == pattern
}

// ForSource returns the strictest entry over the hosts of src.
// A restricted entry is stricter than a conditional one, and a conditional one is stricter than an open one.
// Between two entries of one class, the entry with the lower rate is stricter.
func ForSource(src *xmlparser.MapSource) Entry {
	hosts := src.Hosts()
	if len(hosts) == 0 {
		return Lookup("")
	}
	strictest := Lookup(hosts[0])
	for _, h := range hosts[1:] {
		if e := Lookup(h); stricter(e, strictest) {
			strictest = e
		}
	}
	return strictest
}

var classRank = map[Class]int{Open: 0, Conditional: 1, Restricted: 2}

func stricter(a, b Entry) bool {
	if classRank[a.PolicyClass] != classRank[b.PolicyClass] {
		return classRank[a.PolicyClass] > classRank[b.PolicyClass]
	}
	return a.RequestsPerSecond < b.RequestsPerSecond
}

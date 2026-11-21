package commands

import (
	"errors"

	"github.com/crank-git/atak-map-downloader/internal/policy"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
)

// exitTermsNotAccepted is the exit code for a restricted map source without --accept-terms (feature 07 exit codes).
const exitTermsNotAccepted = 2

// ExitError is an error that sets the exit code of atak-dl.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }

func (e *ExitError) Unwrap() error { return e.Err }

// ExitCode returns the exit code of atak-dl for err.
// It returns 0 for a nil err and the Code of the first ExitError in the chain of err.
// It returns 1 for every other error.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}
	return 1
}

// sourcePolicy returns the policy entry of a map source.
// The terms check, the source badges and the engine configuration all call it, so they cannot disagree.
// Tests replace it, because a test map source must point at a local server and not at a restricted host.
var sourcePolicy = policy.ForSource

// restrictedEntry returns the policy entry of source, and true when the class of the entry is restricted.
func restrictedEntry(source *xmlparser.MapSource) (policy.Entry, bool) {
	entry := sourcePolicy(source)
	return entry, entry.PolicyClass == policy.Restricted
}

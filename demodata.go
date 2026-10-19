// Package cowboy holds the sample repo the demo runs on, built into the
// binary so `cowboy demo` works wherever cowboy is installed.
package cowboy

import "embed"

// Demo is the testdata folder: sample pull requests (prs/), the sample repo
// they change (acme/) and past activity for the agent check (history/).
//
//go:embed all:testdata
var Demo embed.FS

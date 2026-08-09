package e2e

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The provider matrix: the same phase across every provider that ran it.
//
// One run answers "did this build work on this provider". It cannot answer the
// question a developer actually has after a red result — is this broken
// everywhere, or only on VMware? — because that needs the other runs, and until
// now nothing read them together.
//
// So this reads the run directory rather than a single run. That is also why it
// is honest about age: a matrix built from a nightly kind run and a three-day-
// old OpenStack one is comparing two different builds, and says so rather than
// implying they were tested together.

// Cell is one phase on one provider.
type Cell struct {
	State State  `json:"state"`
	Run   string `json:"run,omitempty"`
	// CLICommit is which build produced this result. Two cells with different
	// commits are not a comparison, and a matrix that hides that invites the
	// wrong conclusion.
	CLICommit string `json:"cli_commit,omitempty"`
	Message   string `json:"message,omitempty"`
}

// MatrixRow is one phase across the providers.
type MatrixRow struct {
	Phase  ID              `json:"phase"`
	Number int             `json:"number"`
	Title  string          `json:"title"`
	Cells  map[string]Cell `json:"cells"`
}

// Matrix is the whole comparison.
type Matrix struct {
	// Profiles are the columns, in catalogue order so the table does not
	// reshuffle between renders.
	Profiles []string    `json:"profiles"`
	Rows     []MatrixRow `json:"rows"`

	// Coverage is how much of the lifecycle each profile actually exercised —
	// the question "which areas are untested" asks. A profile that skipped six
	// phases has not covered them, and a dashboard that counts skips as passes
	// reports coverage it does not have.
	Coverage map[string]Coverage `json:"coverage"`

	// Commits is the build each profile's newest run tested, so a reader can
	// see when the columns are not the same build.
	Commits map[string]string `json:"commits,omitempty"`

	// SameBuild is false when the columns come from different CLI commits.
	SameBuild bool `json:"same_build"`
}

// Coverage is one profile's share of the lifecycle.
type Coverage struct {
	Ran     int `json:"ran"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
	Blocked int `json:"blocked"`
	Total   int `json:"total"`
}

// Percent is the share of the lifecycle this profile actually exercised.
//
// Skipped and blocked phases are not covered. A configuration-only profile
// skips six phases by design and that is fine — but it is 15 of 21, not 21 of
// 21, and saying otherwise is how "fully covered" gets claimed for a matrix
// with a hole in it.
func (c Coverage) Percent() int {
	if c.Total == 0 {
		return 0
	}
	return c.Ran * 100 / c.Total
}

// BuildMatrix reads every run under root and compares the newest of each
// profile.
//
// Newest per profile rather than every run: ten runs of kind and one of vmware
// is not a matrix, it is a history, and the question here is what each provider
// currently says.
func BuildMatrix(root string) Matrix {
	newest := map[string]*Run{}

	entries, err := os.ReadDir(root)
	if err != nil {
		return Matrix{Coverage: map[string]Coverage{}}
	}
	// The ids are timestamps, so reverse order visits the newest first and the
	// first one seen for a profile is the one to keep.
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "e2e-") {
			names = append(names, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))

	for _, name := range names {
		run, err := LoadRun(filepath.Join(root, name))
		if err != nil || run.Profile == "" {
			continue
		}
		if _, seen := newest[run.Profile]; !seen {
			newest[run.Profile] = run
		}
	}

	matrix := Matrix{
		Coverage:  map[string]Coverage{},
		Commits:   map[string]string{},
		SameBuild: true,
	}

	// Columns in catalogue order, and only the profiles that actually ran. An
	// empty column for a profile nobody has run says "untested", which is true
	// and useful, so those are kept — but only if some run exists at all.
	for _, profile := range Profiles {
		if _, ran := newest[profile.Name]; ran {
			matrix.Profiles = append(matrix.Profiles, profile.Name)
		}
	}
	if len(matrix.Profiles) == 0 {
		return matrix
	}

	commit := ""
	for _, name := range matrix.Profiles {
		run := newest[name]
		matrix.Commits[name] = run.CLICommit
		if run.CLICommit != "" {
			if commit == "" {
				commit = run.CLICommit
			} else if commit != run.CLICommit {
				matrix.SameBuild = false
			}
		}

		coverage := Coverage{Total: len(Order)}
		for _, phase := range run.Phases {
			switch phase.State {
			case StatePassed:
				coverage.Ran++
				coverage.Passed++
			case StateWarning:
				coverage.Ran++
			case StateFailed, StateCancelled:
				coverage.Ran++
				coverage.Failed++
			case StateSkipped:
				coverage.Skipped++
			case StateBlocked:
				coverage.Blocked++
			}
		}
		matrix.Coverage[name] = coverage
	}

	for _, phase := range Order {
		row := MatrixRow{
			Phase: phase.ID, Number: phase.Number, Title: phase.Title,
			Cells: map[string]Cell{},
		}
		for _, name := range matrix.Profiles {
			run := newest[name]
			result := run.Result(phase.ID)
			if result == nil {
				continue
			}
			row.Cells[name] = Cell{
				State: result.State, Run: run.ID,
				CLICommit: run.CLICommit, Message: result.Message,
			}
		}
		matrix.Rows = append(matrix.Rows, row)
	}
	return matrix
}

// ProviderOnly reports whether a phase failed on some providers and passed on
// others.
//
// The question a red result raises: is this the product, or is it this
// provider? A phase red everywhere is a defect; red on one column and green on
// the rest is a provider problem, and telling them apart is most of what the
// matrix is for.
func (m Matrix) ProviderOnly(phase ID) bool {
	var passed, failed int
	for _, row := range m.Rows {
		if row.Phase != phase {
			continue
		}
		for _, cell := range row.Cells {
			switch cell.State {
			case StatePassed, StateWarning:
				passed++
			case StateFailed, StateCancelled:
				failed++
			}
		}
	}
	return passed > 0 && failed > 0
}

package actionsetup

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/opencenter-cloud/opencli-testbench/internal/gitopsupdate"
)

// Annotation is one thing a run reported as broken.
//
// The action writes a ::error:: line per failing command, which GitHub stores
// as an annotation against the job. Reading them back is what lets the console
// show the failing command itself instead of a link to go and look for it.
type Annotation struct {
	Job     string `json:"job"`
	Command string `json:"command"`
	Message string `json:"message"`
}

// A finished run's annotations never change, so they are read once.
//
// Without this the console spent two or more requests per failed run on every
// page render. Anonymously GitHub allows sixty an hour, so a board with five
// red rows exhausted the budget in a handful of refreshes and then reported
// that the token lacked access — when the real answer was that it had asked
// the same immutable question thirty times.
var (
	annotationsMu    sync.Mutex
	annotationsCache = map[int64][]Annotation{}
)

func cachedAnnotations(id int64) ([]Annotation, bool) {
	annotationsMu.Lock()
	defer annotationsMu.Unlock()
	found, ok := annotationsCache[id]
	return found, ok
}

func cacheAnnotations(id int64, found []Annotation) {
	annotationsMu.Lock()
	defer annotationsMu.Unlock()
	annotationsCache[id] = found
}

// RunFailures returns the annotations a finished run left behind.
//
// Two calls per run: the jobs, then the annotations of each job that failed.
// A run that succeeded has nothing to say, and asking costs a round trip, so
// the caller is expected to skip those.
func RunFailures(ctx context.Context, config gitopsupdate.Config,
	redactor gitopsupdate.Redactor, id int64, limit int) ([]Annotation, error) {
	slug := gitopsupdate.Slug(config.Repository)
	if slug == "" {
		return nil, fmt.Errorf("%q is not a GitHub owner/name",
			gitopsupdate.StripCredentials(config.Repository))
	}
	if limit <= 0 {
		limit = 12
	}
	if found, ok := cachedAnnotations(id); ok {
		return found, nil
	}

	base := strings.TrimSuffix(strings.TrimSpace(config.APIBase), "/")
	if base == "" {
		base = gitopsupdate.DefaultAPIBase
	}

	body, err := api(ctx, config, redactor,
		fmt.Sprintf("%s/repos/%s/actions/runs/%d/jobs?per_page=30", base, slug, id), "")
	if err != nil {
		return nil, err
	}
	var jobs struct {
		Jobs []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Conclusion string `json:"conclusion"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(body, &jobs); err != nil {
		return nil, err
	}

	var failures []Annotation
	for _, job := range jobs.Jobs {
		// Every finished job, not only the failed ones.
		//
		// A lifecycle run that ends in WARNING exits 0, so GitHub calls the job
		// a success — and skipping successes meant the half of the bench that
		// produces most of the findings reported none of them. A green job with
		// annotations on it is exactly the case worth reading.
		if job.Conclusion == "" || job.Conclusion == "skipped" || len(failures) >= limit {
			continue
		}
		body, err := api(ctx, config, redactor,
			fmt.Sprintf("%s/repos/%s/check-runs/%d/annotations?per_page=50",
				base, slug, job.ID), "")
		if err != nil {
			// One unreadable job should not blank out the others.
			continue
		}
		var notes []struct {
			Level   string `json:"annotation_level"`
			Title   string `json:"title"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(body, &notes); err != nil {
			continue
		}
		for _, note := range notes {
			// Warnings too. The lifecycle marks what is not openCenter's fault
			// as a warning, and dropping those loses the environment issues
			// that explain a run rather than condemn a build.
			if (note.Level != "failure" && note.Level != "warning") ||
				len(failures) >= limit {
				continue
			}
			command, message := splitAnnotation(note.Title, note.Message)
			failures = append(failures, Annotation{
				Job:     job.Name,
				Command: redactor.String(command),
				Message: redactor.String(message),
			})
		}
	}
	kept := withoutRunnerNoise(failures)
	cacheAnnotations(id, kept)
	return kept, nil
}

// withoutRunnerNoise drops GitHub's own "Process completed with exit code 1".
//
// The runner writes that against every failed step. It names no command and no
// cause, so alongside real findings it is padding — but on a run that produced
// nothing else it is the only thing there is, and an empty list would read as
// "nothing failed" on a red run. So it is dropped only when something better
// survives.
func withoutRunnerNoise(all []Annotation) []Annotation {
	var kept []Annotation
	for _, one := range all {
		if !strings.HasPrefix(one.Command, "Process completed with exit code") {
			kept = append(kept, one)
		}
	}
	if len(kept) == 0 {
		return all
	}
	return kept
}

// splitAnnotation pulls the command out of the front of an annotation.
//
// The action writes "<command> — <what went wrong>", because a message is only
// useful if it names the thing that produced it. An annotation written by
// anything else has no such shape, and is reported whole.
func splitAnnotation(title, message string) (command, rest string) {
	text := strings.TrimSpace(message)
	if text == "" {
		text = strings.TrimSpace(title)
	}
	if head, tail, ok := strings.Cut(text, " — "); ok {
		return strings.TrimSpace(head), strings.TrimSpace(tail)
	}
	if head, tail, ok := strings.Cut(text, ": "); ok && len(head) < 120 {
		return strings.TrimSpace(head), strings.TrimSpace(tail)
	}
	return text, ""
}

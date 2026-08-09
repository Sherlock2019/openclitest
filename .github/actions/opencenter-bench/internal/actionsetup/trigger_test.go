package actionsetup

import (
	"strings"
	"testing"
	"time"
)

// The guardrail on pressing the button twice.
//
// The cost of a second press is real and invisible: the installed workflow
// sets cancel-in-progress for the branch, so pushing again kills the run still
// going for the previous commit. Somebody watching that run sees it die and
// concludes CI is broken. The warning has to name that consequence, not merely
// report that a commit exists.
func TestTheWarningSaysASecondPressCancelsTheRunInFlight(t *testing.T) {
	warning := triggerWarning(&PreviousTrigger{
		Commit:  "9945e5c",
		Subject: triggerSubject + " (2026-08-06T16:27:00Z)",
		Age:     "3 minutes ago",
		Ours:    true,
	})

	for _, wanted := range []string{"cancels", "3 minutes ago", "9945e5c"} {
		if !strings.Contains(warning, wanted) {
			t.Errorf("the warning does not mention %q:\n%s", wanted, warning)
		}
	}
	// It must not claim a pull request. That wording belongs to the install
	// flow and sent a reader looking for something this button never opens.
	if strings.Contains(strings.ToLower(warning), "pull request") {
		t.Errorf("the warning mentions a pull request, which this button never opens:\n%s", warning)
	}
}

// A branch tip somebody else put there is a different situation, and saying
// "you already triggered a run" about it would be false.
func TestTheWarningDistinguishesACommitThisButtonDidNotMake(t *testing.T) {
	warning := triggerWarning(&PreviousTrigger{
		Commit:  "abc1234",
		Subject: "fix: something a person did by hand",
		Ours:    false,
	})

	if strings.Contains(warning, "already carries a trigger") {
		t.Errorf("a foreign commit is reported as a previous trigger:\n%s", warning)
	}
	for _, wanted := range []string{"abc1234", "nothing", "lost"} {
		if !strings.Contains(warning, wanted) {
			t.Errorf("the warning does not reassure about %q:\n%s", wanted, warning)
		}
	}
}

// The first press has nothing to warn about, and a warning printed then would
// train the reader to ignore the next one.
func TestThereIsNoWarningOnTheFirstPress(t *testing.T) {
	if warning := triggerWarning(nil); warning != "" {
		t.Errorf("a first press warns about nothing: %q", warning)
	}
}

func TestAgeReadsAsSomethingAPersonWouldSay(t *testing.T) {
	for _, c := range []struct {
		since time.Duration
		want  string
	}{
		{30 * time.Second, "less than a minute ago"},
		{90 * time.Second, "a minute ago"},
		{25 * time.Minute, "25 minutes ago"},
		{95 * time.Minute, "an hour ago"},
		{5 * time.Hour, "5 hours ago"},
		{50 * time.Hour, "2 days ago"},
	} {
		if got := humanAge(c.since); got != c.want {
			t.Errorf("humanAge(%s) = %q, want %q", c.since, got, c.want)
		}
	}
}

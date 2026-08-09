package actionsetup

import (
	"errors"
	"testing"
)

// An SSH-key install that pushed its branch is not a failed install.
//
// Found by running it for real with only the deploy key the console had saved.
// Everything worked — clone, branch, write, validate, commit, push — and the
// result came back FAILED with exit 6, because opening a pull request is a REST
// call a key cannot make. The panel's own advice is that a key is the easier
// credential, so the recommended setup was the one that always reported
// failure, which is how somebody re-runs an install that already landed.
func TestNoTokenIsRecognisedAsSuchAndNothingElseIs(t *testing.T) {
	cases := []struct {
		err  error
		want bool
		why  string
	}{
		{nil, false, "no error at all"},
		{errors.New("no GitHub token: set OPENCLI_GIT_TOKEN to open a pull request"),
			true, "the message the client actually returns"},
		{errors.New("no token"), true, "the short form"},
		{errors.New("NO GITHUB TOKEN"), true, "case must not matter"},

		// The ones that must stay failures. A token that exists and was refused
		// is a real problem with a real cause, and swallowing it would report a
		// pull request as merely skipped when the credential is wrong.
		{errors.New("GitHub refused the request — the token needs the workflow scope"),
			false, "a token that was refused"},
		{errors.New("HTTP 422: validation failed"), false, "an invalid request"},
		{errors.New("dial tcp: lookup api.github.com: no such host"), false,
			"the network being down"},
		{errors.New("403 Forbidden"), false, "a permission problem"},
	}
	for _, c := range cases {
		if got := noGitHubToken(c.err); got != c.want {
			t.Errorf("noGitHubToken(%v) = %v, want %v — %s", c.err, got, c.want, c.why)
		}
	}
}

// The two conditions that already ended in StatusPushed and the new one must
// agree: a branch on the remote with no request opened is a complete outcome,
// and the message has to say what is left to do rather than imply a request
// somebody will go looking for.
func TestPushedIsAnOutcomeNotAFailure(t *testing.T) {
	for _, status := range []Status{StatusPushed, StatusPRCreated, StatusUnchanged} {
		result := &Result{Status: status}
		if code := result.ExitCode(); code != ExitOK {
			t.Errorf("%s exits %d, want %d — the branch is on the remote",
				status, code, ExitOK)
		}
	}
	if (&Result{Status: StatusFailed}).ExitCode() == ExitOK {
		t.Error("a genuine failure exits zero")
	}
}

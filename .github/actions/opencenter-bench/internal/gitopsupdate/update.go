package gitopsupdate

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// The orchestrator: the eleven steps, in order, once.
//
// One function rather than eleven callable endpoints, because the steps are
// phases of a single operation and not independent commands. "gitops commit"
// has no meaning unless "gitops checkout" happened in the same operation
// against the same checkout, and an API that let a caller run them separately
// would need to persist a half-built git working tree between HTTP requests and
// then guard every out-of-order press. The page shows eleven rows and this
// fills them all in; the sequencing lives here, where it can be read.
//
// Preview and approved take the identical path as far as the diff. That is the
// point of preview: it is not a simulation of the update, it is the update, run
// with the writing turned off after the last local step. Anything preview
// cannot catch, approved would not have caught either.

// Request is one invocation of the stage.
type Request struct {
	Config   Config
	Run      RunSummary
	Approval Approval
	Mode     Mode

	// SandboxRoot is the throwaway directory the checkout is made in. Nothing
	// is written outside it.
	SandboxRoot string
	// BenchRoot is the repository this bench runs from, used only to make the
	// report path in the evidence relative.
	BenchRoot string
	// ReportsDir and EvidenceDir are where this run's artefacts go:
	// artifacts/runs/<id>/reports and artifacts/runs/<id>/evidence.
	ReportsDir  string
	EvidenceDir string

	Redactor Redactor
	// Canaries are the values the run planted to detect a leak. Scanned for in
	// the proposed patch.
	Canaries []string
}

// Run executes the stage and returns a filled-in result.
//
// It returns an error only for a programming mistake. Everything a person could
// have got wrong — a missing repository, an ineligible run, a rejected
// credential — is a Result with a status and a reason, because those are
// findings and the page has to show them rather than a 500.
func Run(ctx context.Context, request Request) *Result {
	if request.Redactor == nil {
		request.Redactor = noRedactor{}
	}
	if request.Mode == "" {
		request.Mode = ModePreview
	}
	result := newResult(request.Mode)
	config := request.Config
	result.Repository = StripCredentials(config.Repository)
	result.BaseBranch = config.BaseBranch

	// --- 1. preflight ---------------------------------------------------------
	done := result.begin(StepPreflight)
	if !config.Configured() {
		done(StepSkipped, "no GitOps repository configured")
		result.skipRest(StepSkipped, "GitOps is not configured")
		result.Status = StatusNotConfigured
		result.Message = "No GitOps repository is configured. Nothing was proposed, " +
			"and nothing failed."
		return result
	}
	if err := config.Validate(); err != nil {
		done(StepFailed, err.Error())
		result.skipRest(StepSkipped, "configuration is not usable")
		result.Status = StatusBlocked
		result.Reasons = []string{err.Error()}
		result.Message = err.Error()
		return result
	}
	done(StepOK, fmt.Sprintf("%s → %s", result.Repository, config.BaseBranch))

	// --- 2. quality gate ------------------------------------------------------
	done = result.begin(StepQualityGate)
	eligibility := Eligible(request.Run, config)
	result.Eligible = eligibility.Eligible
	result.Reasons = eligibility.Reasons
	if !eligibility.Eligible {
		done(StepBlocked, strings.Join(eligibility.Reasons, "; "))
		result.skipRest(StepBlocked, "the run is not eligible for promotion")
		result.Status = StatusBlocked
		result.Message = "This run may not be promoted: " + eligibility.Reasons[0]
		return result
	}
	if eligibility.Warned {
		done(StepOK, fmt.Sprintf("eligible with %d warning(s), explicitly allowed",
			request.Run.Warnings))
	} else {
		done(StepOK, fmt.Sprintf("%d passed, 0 failed, cleanup %s",
			request.Run.Passed, cleanupWord(request.Run.CleanupState)))
	}

	// --- 3. evidence ----------------------------------------------------------
	done = result.begin(StepEvidence)
	evidence := NewEvidence(request.Run, request.BenchRoot, eligibility.Warned)
	evidencePath, err := evidence.Write(request.ReportsDir)
	if err != nil {
		done(StepFailed, err.Error())
		return fail(result, "could not write the GitOps evidence: "+err.Error())
	}
	result.EvidencePath = evidencePath
	done(StepOK, filepath.Base(evidencePath))

	// The tag comes from the tested commit unless CI already built an image and
	// said which tag it pushed. Either way it is immutable and specific.
	tag := strings.TrimSpace(config.ImageTag)
	if tag == "" {
		tag = ImageTag(request.Run.SourceCommit)
	}
	if tag == "" {
		return fail(result, "no image tag: the run recorded no source commit, so there "+
			"is no specific build to promote")
	}
	result.ImageReference = config.ImageRepository + ":" + tag

	// --- 4. checkout ----------------------------------------------------------
	done = result.begin(StepCheckout)
	checkout := filepath.Join(request.SandboxRoot, "gitops")
	repo, err := Open(ctx, config, request.SandboxRoot, checkout, request.Redactor)
	if err != nil {
		done(StepFailed, err.Error())
		return fail(result, err.Error())
	}
	done(StepOK, "cloned "+config.BaseBranch)

	// --- 5. branch ------------------------------------------------------------
	done = result.begin(StepBranch)
	branch := BranchName(request.Run.RunID)
	if err := repo.CreateBranch(ctx, branch); err != nil {
		done(StepFailed, err.Error())
		return fail(result, err.Error())
	}
	result.Branch = branch
	done(StepOK, branch)

	// --- 6. manifest ----------------------------------------------------------
	done = result.begin(StepManifest)
	var expected []string
	change := ManifestChange{Path: config.ManifestPath}
	if config.ManifestPath != "" && config.ImageRepository != "" {
		change, err = UpdateImage(checkout, config.ManifestPath,
			config.ImageRepository, tag, config.ContainerName)
		if err != nil {
			done(StepFailed, err.Error())
			return fail(result, err.Error())
		}
		if change.Changed {
			expected = append(expected, config.ManifestPath)
		}
	}

	// The evidence, at the configured path and in the per-run history. Written
	// even when the manifest did not move: a re-run of the same commit still
	// produced a result worth recording.
	body, err := evidence.Bytes()
	if err != nil {
		done(StepFailed, err.Error())
		return fail(result, err.Error())
	}
	if config.EvidencePath != "" {
		for _, path := range []string{
			config.EvidencePath,
			HistoryPath(config.EvidencePath, request.Run.RunID),
		} {
			if path == "" {
				continue
			}
			if !config.Approved(path) {
				done(StepFailed, path+" is outside the approved GitOps paths")
				return fail(result, path+" is outside the approved GitOps paths")
			}
			if err := WriteInto(checkout, path, body); err != nil {
				done(StepFailed, err.Error())
				return fail(result, err.Error())
			}
			expected = append(expected, path)
		}
	}
	switch {
	case change.Changed:
		done(StepOK, fmt.Sprintf("%s → %s", change.Path, tag))
	default:
		done(StepOK, "image already at "+tag+"; evidence updated")
	}

	// --- 7. validate ----------------------------------------------------------
	done = result.begin(StepValidate)
	diff, err := Inspect(ctx, repo, config, expected)
	if err != nil {
		done(StepFailed, err.Error())
		return fail(result, err.Error())
	}
	diff.Problems = append(diff.Problems,
		ScanSecrets(diff.Patch, request.Redactor, request.Canaries)...)
	result.FilesChanged = diff.Files

	// Saved whatever the verdict. A rejected diff is precisely the one somebody
	// needs to look at, and refusing to write it because it was rejected would
	// be the wrong way round.
	if patchPath, patchErr := SavePatch(
		request.EvidenceDir, diff.Patch, request.Redactor); patchErr == nil {
		result.PatchPath = patchPath
	}

	if len(diff.Problems) > 0 {
		done(StepBlocked, strings.Join(diff.Problems, "; "))
		result.skipRest(StepBlocked, "the proposed change was rejected")
		result.Status = StatusBlocked
		result.Reasons = append(result.Reasons, diff.Problems...)
		result.Message = "The proposed GitOps change was rejected: " + diff.Problems[0]
		return result
	}
	result.Changed = true
	done(StepOK, fmt.Sprintf("%d file(s) changed, all inside the approved paths", len(diff.Files)))

	// --- the line between preparing and publishing ----------------------------
	//
	// Everything above ran identically in both modes. Below this, both gates
	// must be open, and preview stops here having written nothing remote.
	if request.Mode != ModeApproved {
		result.skipRest(StepSkipped, "preview — no remote changes made")
		result.Status = StatusPreview
		result.Message = fmt.Sprintf(
			"PREVIEW — no remote changes made. %d file(s) would change on branch %s.",
			len(diff.Files), branch)
		return result
	}
	if permitted, why := request.Approval.Permits(); !permitted {
		result.skipRest(StepBlocked, why)
		result.Status = StatusBlocked
		result.Reasons = append(result.Reasons, why)
		result.Message = "No remote change was made: " + why
		return result
	}

	// --- 8. commit ------------------------------------------------------------
	done = result.begin(StepCommit)
	sha, err := repo.Commit(ctx, CommitMessage(evidence, ShortSHA(request.Run.SourceCommit)))
	if err != nil {
		done(StepFailed, err.Error())
		return fail(result, err.Error())
	}
	result.CommitSHA = sha
	done(StepOK, ShortSHA(sha))

	// --- 9. push --------------------------------------------------------------
	done = result.begin(StepPush)
	if err := repo.Push(ctx, branch); err != nil {
		done(StepFailed, err.Error())
		return fail(result, err.Error())
	}
	done(StepOK, "pushed "+branch)

	// --- 10. pull request -----------------------------------------------------
	done = result.begin(StepPullRequest)
	slug := Slug(config.Repository)
	switch {
	case !config.CreatePR:
		done(StepSkipped, "pull request creation is switched off")
	case slug == "":
		// A local bare repository has no API. The branch is pushed and that is
		// the whole of what this remote can offer; saying so is better than
		// reporting a failure the operator cannot act on.
		done(StepSkipped, "the remote is not a GitHub repository — branch pushed, no pull request")
	default:
		client := NewClient(config, request.Redactor)
		pr, prErr := client.Create(ctx, slug, branch, config.BaseBranch,
			PullRequestTitle(ShortSHA(request.Run.SourceCommit)),
			PullRequestBody(evidence, change, diff.Files))
		if prErr != nil {
			done(StepFailed, prErr.Error())
			// The branch is on the remote. That is worth saying plainly,
			// because the operator can open the pull request by hand and does
			// not need to run any of this again.
			return fail(result, prErr.Error()+
				" — the branch "+branch+" was pushed, so a pull request can be opened by hand")
		}
		result.PullRequest = pr.URL
		result.PullRequestNumber = pr.Number
		if pr.Existing {
			done(StepOK, fmt.Sprintf("#%d was already open for this branch", pr.Number))
		} else {
			done(StepOK, fmt.Sprintf("#%d", pr.Number))
		}
	}

	// --- 11. verify -----------------------------------------------------------
	//
	// Asked of the remote, not of the local state. A push that returned zero
	// and a branch that exists on the server are two different claims, and only
	// the second one is what the pull request depends on.
	done = result.begin(StepVerify)
	if !repo.RemoteHasBranch(ctx, branch) {
		done(StepFailed, "the branch is not on the remote after a successful push")
		return fail(result, "the push reported success but "+branch+" is not on the remote")
	}
	verified := "branch " + branch + " is on the remote"
	if result.PullRequestNumber > 0 {
		verified += fmt.Sprintf(", pull request #%d is open", result.PullRequestNumber)
	}
	done(StepOK, verified)

	switch {
	case result.PullRequestNumber > 0:
		result.Status = StatusPRCreated
		result.Message = fmt.Sprintf("Pull request #%d created in %s.",
			result.PullRequestNumber, result.Repository)
	default:
		result.Status = StatusPassed
		result.Message = "Branch " + branch + " pushed. No pull request was created."
	}
	if eligibility.Warned {
		result.Message += " Promoted with warnings, which was explicitly allowed."
	}
	return result
}

// fail closes a result that broke rather than one that was refused.
//
// The distinction is kept all the way to the exit code: BLOCKED means the run
// did not earn a promotion, FAILED means the promotion machinery itself broke.
// A caller that cannot tell them apart cannot decide whether to retry.
func fail(result *Result, message string) *Result {
	result.skipRest(StepSkipped, "an earlier step failed")
	result.Status = StatusFailed
	result.Message = message
	result.Reasons = append(result.Reasons, message)
	return result
}

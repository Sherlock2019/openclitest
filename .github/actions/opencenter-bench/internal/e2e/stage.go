package e2e

import "fmt"

// Stages group the twenty-one phases into the nine things a person actually
// talks about.
//
// The phase list is the right unit for an engine and the wrong one for a rail.
// Twenty-one equal chips across a sidebar say "twenty-one steps, all alike",
// and the reader has to hold the whole lifecycle in their head to find the one
// they want. Nine named groups say where you are: building, configuring,
// deploying, proving it healthy, tearing it down.
//
// This lives beside the phases rather than in the console, so the rail, the
// report and anything else that groups them cannot disagree — and so a phase
// added to Order without a home here fails a test rather than quietly vanishing
// from the sidebar.

// Stage is one band of the rail.
type Stage struct {
	ID     string
	Number int
	Name   string

	// Purpose is what this group of phases is for, in one line.
	Purpose string

	// New marks a stage the console did not have. There are two, and only they
	// carry colours here — see Fill.
	New bool

	// After names the console stage this one is inserted behind. Only meaningful
	// for a New stage; the rest already exist in the rail and keep their place.
	After string

	// Fill and OnFill colour a New stage, and are empty for the rest.
	//
	// Empty on purpose. The console's rail already has ten bands in the editor's
	// syntax palette, and a stage that exists there keeps the colour it has —
	// repainting it would say the lifecycle replaced it, when the lifecycle
	// joined it. Only the two genuinely new stages are coloured, and both are
	// orange, so "orange" reads as "this is what the lifecycle added" at a
	// glance down one rail.
	//
	// Measured against a 4.5:1 floor: #331400 on #F76707 is 5.6:1, and the
	// lighter #FF922B is higher still. Dark ink rather than light — #FFF4E6 on
	// these mid oranges is 2.79:1 and fails.
	Fill   string
	OnFill string

	Phases []ID
}

// Stages map the twenty-one phases onto the console's own rail.
//
// Onto it, not beside it. There was a second rail here for a while and it was
// wrong: seven of its names already existed in the first one, so the sidebar
// showed PREREQUISITES, CONFIGURE, GENERATE, DEPLOY, OPERATE, TEARDOWN and
// RESULTS twice, and a reader had to work out which of the two they were
// looking at before they could read either.
//
// One rail, one story — the user story this bench exists for: build the CLI,
// make a cluster definition, check it holds, turn it into artifacts, build the
// infrastructure, prove it healthy, use it, break it, take it down, and say
// what happened. The IDs are the console's own stage ids, so a lifecycle phase
// lands in the band a reader already knows.
//
// Two stages are new, because the command table has no answer for them at all.
// Those two are orange; every other band keeps the colour it already had.
var Stages = []Stage{
	{
		ID: "prerequisites", Number: 1, Name: "Prerequisites",
		Purpose: "Say what is about to happen, get an isolated workspace, and check the tools.",
		Phases:  []ID{PhasePlan, PhaseWorkspace, PhasePrerequisites},
	},
	{
		ID: "build", Number: 2, Name: "Build",
		Purpose: "Build openCenter-cli with mise, and prove the binary is the source.",
		// Nothing in the command table builds anything: it runs commands against
		// a binary somebody else produced. "Build openCenter-cli using mise" is
		// the first line of the brief and could not be asked here before.
		New: true, After: "prerequisites",
		// Its place in the rail's ramp, between prerequisites' grey and init's
		// light blue — not a colour that means "new". Orange meant that once,
		// and generate was also orange, and the three (#FF922B here, #FF9A2E
		// generate, #F76707 health) were within five per cent of one another
		// while the legend read "Build and Health are orange". The NEW chip
		// says it in a word instead, which cannot be misread as a neighbouring
		// hue.
		// systemMint. Its place in the ramp, between prerequisites' gray and
		// init's teal.
		Fill: "#63E6E2", OnFill: "#032523",
		Phases: []ID{PhaseBuild, PhaseVerifyBinary},
	},
	{
		ID: "init", Number: 3, Name: "Init",
		Purpose: "Create the cluster definition from nothing, with run-specific names.",
		Phases:  []ID{PhaseConfigure},
	},
	{
		ID: "validate", Number: 4, Name: "Validate",
		Purpose: "Ask the CLI whether the definition is coherent, and the provider whether it is reachable.",
		Phases:  []ID{PhaseValidateConfig, PhaseDoctor},
	},
	{
		ID: "generate", Number: 5, Name: "Generate",
		Purpose: "Turn the definition into manifests and terraform, and check what came out.",
		Phases:  []ID{PhaseRenderPreview, PhaseGenerate, PhaseValidateArtifacts},
	},
	{
		ID: "deploy", Number: 6, Name: "Deploy",
		Purpose: "Build real infrastructure, and record every resource as it appears.",
		Phases:  []ID{PhaseDeploy, PhaseInfrastructure},
	},
	{
		ID: "health", Number: 7, Name: "Health",
		Purpose: "Prove Kubernetes and the openCenter platform services are actually working.",
		// The other one the command table cannot ask. "Did this command exit
		// zero" and "is this cluster healthy" are different questions, and only
		// one of them is about a cluster.
		New: true, After: "deploy",
		// Between deploy's violet and operate's orchid, for the same reason
		// Build takes cyan: its place in the ramp, not a colour meaning "new".
		// systemGreen, asked for directly.
		//
		// Against the rule the rest of the palette follows — green means PASS on
		// this page — and worth one line saying so: a green Health band sits a
		// few inches from a green PASS badge and they mean different things.
		// The reading that argues for it is stronger here than anywhere else,
		// though: this band is the one that asks whether the platform is
		// healthy, and green is what healthy looks like.
		Fill: "#30D158", OnFill: "#062A11",
		Phases: []ID{PhaseKubernetes, PhasePlatform},
	},
	{
		ID: "operate", Number: 8, Name: "Operate",
		Purpose: "Run a real application on it, then break things on purpose and watch it recover.",
		Phases:  []ID{PhaseSmoke, PhaseFailureTests},
	},
	{
		// Named Reset, like the rail band it lands in. The word describes what
		// it leaves behind — a machine in the state the run found it — rather
		// than the act of dismantling, and this bench's whole claim about
		// cleanup is about the state afterwards.
		//
		// Its phases still run here, before report: the verdict depends on what
		// verify-cleanup found, so it cannot be computed any earlier. The rail
		// shows Reset last because resetting is the last thing a person does,
		// and the band says so rather than leaving the two orders to collide
		// silently.
		ID: "teardown", Number: 9, Name: "Reset",
		Purpose: "Collect diagnostics, destroy everything, and prove nothing was left behind.",
		Phases:  []ID{PhaseDiagnostics, PhaseDestroy, PhaseVerifyCleanup},
	},
	{
		ID: "results", Number: 10, Name: "Results",
		Purpose: "The verdict, and a cause for every failure.",
		Phases:  []ID{PhaseReport},
	},
}

// NewStages are the ones the console's rail does not already have, in order.
//
// Read by the page so it can splice them into the one rail rather than draw a
// second one beside it.
func NewStages() []Stage {
	var out []Stage
	for _, stage := range Stages {
		if stage.New {
			out = append(out, stage)
		}
	}
	return out
}

// StageOf finds the stage a phase belongs to.
func StageOf(id ID) (Stage, bool) {
	for _, stage := range Stages {
		for _, member := range stage.Phases {
			if member == id {
				return stage, true
			}
		}
	}
	return Stage{}, false
}

// CheckStages reports whether every phase has exactly one home.
//
// Called by a test rather than at start-up: this can only be wrong if somebody
// edits this file, and a build that fails is a better place to find out than a
// sidebar with a phase silently missing from it.
func CheckStages() error {
	seen := map[ID]string{}
	for _, stage := range Stages {
		for _, member := range stage.Phases {
			if _, ok := Lookup(member); !ok {
				return fmt.Errorf("stage %s lists %q, which is not a phase", stage.ID, member)
			}
			if first, twice := seen[member]; twice {
				return fmt.Errorf("phase %s is in both %s and %s", member, first, stage.ID)
			}
			seen[member] = stage.ID
		}
	}
	for _, phase := range Order {
		if _, ok := seen[phase.ID]; !ok {
			return fmt.Errorf("phase %s is in no stage, so it would not appear in the rail",
				phase.ID)
		}
	}
	return nil
}

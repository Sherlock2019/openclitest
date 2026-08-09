package e2e

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

// The rail is generated from Stages, so a phase missing from it is a phase that
// silently disappears from the console. This is the only place that can catch
// it, because nothing else reads both lists.
func TestEveryPhaseHasExactlyOneStage(t *testing.T) {
	if err := CheckStages(); err != nil {
		t.Fatal(err)
	}
}

func TestStagesAreNumberedInOrder(t *testing.T) {
	for index, stage := range Stages {
		if stage.Number != index+1 {
			t.Errorf("stage %s is numbered %d but sits at position %d",
				stage.ID, stage.Number, index+1)
		}
	}
}

// A stage whose phases run out of lifecycle order would draw a rail that says
// the work happens in an order it does not.
func TestStagesFollowLifecycleOrder(t *testing.T) {
	position := map[ID]int{}
	for index, phase := range Order {
		position[phase.ID] = index
	}
	previous := -1
	for _, stage := range Stages {
		for _, member := range stage.Phases {
			at := position[member]
			if at <= previous {
				t.Errorf("%s comes after %s in the rail but before it in the lifecycle",
					member, Order[previous].ID)
			}
			previous = at
		}
	}
}

// A colour identifies a band. It does not mean "new", and it never means a
// state.
//
// Orange used to mean "the lifecycle added this", and this test enforced it —
// while generate, which the lifecycle did not add, was #FF9A2E. Three oranges
// (#FF922B build, #FF9A2E generate, #F76707 health) within five per cent of one
// another, and a legend underneath claiming two of them were special. The word
// NEW carries that now, because a word cannot be confused with a neighbouring
// hue.
//
// What is still true is that a stage the rail already has must not be
// repainted: that would say the lifecycle replaced it, when the lifecycle
// joined it. And what is newly true is the rule the whole palette turns on —
// red, amber and green belong to the verdict. A band coloured like a PASS makes
// a claim about state that a band has no knowledge of.
func TestOnlyTheNewStagesAreColoured(t *testing.T) {
	for _, stage := range Stages {
		if !stage.New {
			if stage.Fill != "" || stage.OnFill != "" {
				t.Errorf("%s already exists in the rail but carries a colour (%s on %s)",
					stage.ID, stage.OnFill, stage.Fill)
			}
			continue
		}
		if stage.Fill == "" {
			t.Errorf("%s is new and has no colour, so it has no place in the rail's ramp",
				stage.ID)
			continue
		}
		r, g, b := parseHex(t, stage.Fill)

		// Two deliberate exceptions, and they are named here rather than left to
		// be rediscovered.
		//
		// Health is systemGreen and Operate is systemRed, asked for directly.
		// Both break the rule the rest of the palette follows — green means PASS
		// and red means FAIL on this page, and these two bands sit inches from
		// badges wearing the same colours for a different reason.
		//
		// The reading that argues for them: Health is the band that asks whether
		// the platform is working, and Operate is the one that breaks things on
		// purpose. Green and red are what those two things look like.
		//
		// The cost is real and it is a product decision, so the test records it
		// instead of failing. What it still refuses is a third: the exception
		// list is closed, and any other band drifting onto a verdict colour is a
		// mistake rather than a choice.
		byChoice := map[string]string{
			"health":  "#30D158",
			"operate": "#FF453A",
		}
		if byChoice[stage.ID] == stage.Fill {
			continue
		}
		if g > r+40 && g > b+40 {
			t.Errorf("%s is %s, which reads as green — that is what PASS means",
				stage.ID, stage.Fill)
		}
		if r > g+40 && r > b+40 {
			t.Errorf("%s is %s, which reads as red or orange — red is FAIL, and "+
				"orange is the rule this palette replaced", stage.ID, stage.Fill)
		}
		if r > b+60 && g > b+60 {
			t.Errorf("%s is %s, which reads as amber — that is what WARNING means",
				stage.ID, stage.Fill)
		}
	}
}

// The two added bands must not be the same colour as each other.
//
// They sit at different points in the rail — Build after prerequisites, Health
// after deploy — and if they matched, the ramp would say they were the same
// kind of thing happening twice.
func TestTheAddedStagesAreDistinguishable(t *testing.T) {
	added := NewStages()
	for i := range added {
		for j := i + 1; j < len(added); j++ {
			if added[i].Fill == added[j].Fill {
				t.Errorf("%s and %s are both %s, so the ramp repeats itself",
					added[i].ID, added[j].ID, added[i].Fill)
			}
		}
	}
}

// The contrast floor holds whatever the palette is. It was written for two mid
// oranges and it applies just as much to a cyan and an orchid: the number chip
// and the NEW badge sit on the band's colour, and 4.5:1 is the floor for both.
func TestTheNewStagesMeetTheContrastFloor(t *testing.T) {
	const floor = 4.5
	for _, stage := range NewStages() {
		got := contrastRatio(t, stage.Fill, stage.OnFill)
		if got < floor {
			t.Errorf("%s: %s on %s is %.2f:1, under the %.1f:1 floor",
				stage.ID, stage.OnFill, stage.Fill, got, floor)
		}
	}
}

// Exactly the two stages the command table has no answer for at all, and each
// has to say where it goes or the page cannot place it in the one rail.
func TestOnlyBuildAndHealthAreMarkedNew(t *testing.T) {
	want := map[string]bool{"build": true, "health": true}
	for _, stage := range Stages {
		if stage.New != want[stage.ID] {
			t.Errorf("%s has New=%v, want %v", stage.ID, stage.New, want[stage.ID])
		}
		if stage.New && stage.After == "" {
			t.Errorf("%s is new but does not say which stage it follows", stage.ID)
		}
		if !stage.New && stage.After != "" {
			t.Errorf("%s already has a place in the rail; After is meaningless", stage.ID)
		}
	}
	if len(NewStages()) != 2 {
		t.Fatalf("NewStages returned %d stages, want 2", len(NewStages()))
	}
}

// A new stage must follow one that exists, or the page has nowhere to put it.
func TestEachNewStageFollowsARealOne(t *testing.T) {
	known := map[string]bool{}
	for _, stage := range Stages {
		known[stage.ID] = true
	}
	for _, stage := range NewStages() {
		if !known[stage.After] {
			t.Errorf("%s says it follows %q, which is not a stage", stage.ID, stage.After)
		}
	}
}

// One rail means no duplicated names. This is the check that would have caught
// the two-rail version.
func TestNoStageNameAppearsTwice(t *testing.T) {
	seen := map[string]bool{}
	for _, stage := range Stages {
		if seen[stage.ID] {
			t.Errorf("%s appears twice, so the rail would show it twice", stage.ID)
		}
		seen[stage.ID] = true
	}
}

func parseHex(t *testing.T, value string) (float64, float64, float64) {
	t.Helper()
	value = strings.TrimPrefix(value, "#")
	if len(value) != 6 {
		t.Fatalf("%q is not a six-digit hex colour", value)
	}
	channel := func(at int) float64 {
		number, err := strconv.ParseUint(value[at:at+2], 16, 8)
		if err != nil {
			t.Fatalf("%q is not a hex colour: %v", value, err)
		}
		return float64(number)
	}
	return channel(0), channel(2), channel(4)
}

// luminance is WCAG relative luminance.
func luminance(t *testing.T, colour string) float64 {
	t.Helper()
	r, g, b := parseHex(t, colour)
	linear := func(channel float64) float64 {
		channel /= 255
		if channel <= 0.04045 {
			return channel / 12.92
		}
		return math.Pow((channel+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(r) + 0.7152*linear(g) + 0.0722*linear(b)
}

func contrastRatio(t *testing.T, one, other string) float64 {
	t.Helper()
	first, second := luminance(t, one), luminance(t, other)
	if second > first {
		first, second = second, first
	}
	return (first + 0.05) / (second + 0.05)
}

func TestStageOfFindsAKnownPhase(t *testing.T) {
	stage, ok := StageOf(PhaseDeploy)
	if !ok || stage.ID != "deploy" {
		t.Fatalf("deploy is in stage %q (found: %v), want deploy", stage.ID, ok)
	}
	if _, ok := StageOf(ID("no-such-phase")); ok {
		t.Fatal("an unknown phase was placed in a stage")
	}
}

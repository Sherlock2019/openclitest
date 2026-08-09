package main

import (
	"os"
	"strings"
	"testing"

	"github.com/opencenter-cloud/opencli-testbench/internal/e2e"
)

// Results is second to last and Reset is last.
//
// This has been both ways round, so it is pinned. The band the catalogue calls
// teardown is shown as Reset and comes after Results, because resetting the
// machine — cluster destroy, cluster unlock, settings reset — is what a person
// does once they have read what happened, not before.
func TestResetIsTheLastBandAndResultsComesBeforeIt(t *testing.T) {
	catalogue := &Catalogue{StageOrder: []string{
		"prerequisites", "init", "configure", "validate", "generate",
		"deploy", "operate", "kafka", "teardown",
	}}
	insertResultsStage(catalogue)

	order := catalogue.StageOrder
	if len(order) < 2 {
		t.Fatalf("the rail lost its bands: %v", order)
	}
	if last := order[len(order)-1]; last != "teardown" {
		t.Errorf("the last band is %q, want teardown — shown as Reset", last)
	}
	if before := order[len(order)-2]; before != "results" {
		t.Errorf("the band before Reset is %q, want results", before)
	}
}

// Running it twice must not add a second Results band.
//
// The catalogue is rebuilt on every request, and a duplicate here would show
// two Results rows in the rail with the same number.
func TestInsertingResultsTwiceAddsItOnce(t *testing.T) {
	catalogue := &Catalogue{StageOrder: []string{"deploy", "teardown"}}
	insertResultsStage(catalogue)
	insertResultsStage(catalogue)

	count := 0
	for _, stage := range catalogue.StageOrder {
		if stage == "results" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("results appears %d time(s) in %v", count, catalogue.StageOrder)
	}
}

// A catalogue with no teardown band still gets a Results band.
//
// Otherwise a trimmed catalogue would silently lose the one band that shows the
// verdict, and the rail would end on whatever came last.
func TestResultsIsAppendedWhenThereIsNoTeardown(t *testing.T) {
	catalogue := &Catalogue{StageOrder: []string{"prerequisites", "deploy"}}
	insertResultsStage(catalogue)
	if last := catalogue.StageOrder[len(catalogue.StageOrder)-1]; last != "results" {
		t.Errorf("the rail ends on %q with no results band: %v",
			last, catalogue.StageOrder)
	}
}

// The rail and the lifecycle must call the band the same thing.
//
// The rail renames it through STAGE_NAMES in the page; internal/e2e names it in
// its own stage list. Two different words for one band is how a reader ends up
// believing they are two different things, which is the whole complaint this
// change came from.
func TestTheLifecycleCallsTheBandResetToo(t *testing.T) {
	var found bool
	for _, stage := range e2e.Stages {
		if stage.ID != "teardown" {
			continue
		}
		found = true
		if stage.Name != "Reset" {
			t.Errorf("the lifecycle calls the teardown band %q; the rail calls it Reset",
				stage.Name)
		}
	}
	if !found {
		t.Fatal("the lifecycle has no teardown band, so its phases are homeless")
	}

	page, err := os.ReadFile("ui.html")
	if err != nil {
		t.Fatalf("read ui.html: %v", err)
	}
	if !strings.Contains(string(page), `teardown: "Reset"`) {
		t.Error("the rail does not rename teardown to Reset, so it still reads Teardown")
	}
}

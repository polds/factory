package orchestrator

import (
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestHoldUnapproved(t *testing.T) {
	def := acmeDef()
	before := State{Stage: StageReviewing, Status: StatusOpen, RevisionCount: 0}
	after, eff, err := Step(def, before, RunFailed{Stage: StageReviewing}, M3Predicates())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := eff[0].(StartRun); !ok {
		t.Fatalf("revision effect %#v", eff)
	}
	got, held := HoldUnapproved(def, before, after, eff, nil)
	if got.Stage != StageReviewing || got.Status != StatusAwaitingApproval || got.PendingGate != GateSpec {
		t.Fatalf("parked %+v", got)
	}
	if len(held) != 1 {
		t.Fatalf("effects %#v", held)
	}
	req, ok := held[0].(RequestApproval)
	if !ok || req.Gate != GateSpec {
		t.Fatalf("effect %#v", held[0])
	}
	passed, kept := HoldUnapproved(def, before, after, eff, map[string]bool{GateSpec: true})
	start, ok := kept[0].(StartRun)
	if !ok || start.Stage != StageBuilding || passed.Stage != StageBuilding {
		t.Fatalf("approved %+v %#v", passed, kept)
	}
}

func TestStep(t *testing.T) {
	for _, tc := range stepCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			def := acmeDef()
			if tc.def != nil {
				def = *tc.def
			}
			preds := M3Predicates()
			if tc.preds != nil {
				preds = *tc.preds
			}
			got, eff, err := Step(def, tc.state, tc.event, preds)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if eff != nil {
					t.Fatalf("effects = %#v, want nil", eff)
				}
				if !reflect.DeepEqual(got, tc.state) {
					t.Fatalf("state changed on error\n got %#v\nwant %#v", got, tc.state)
				}
				return
			}
			if err != nil {
				t.Fatalf("Step: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("state\n got %#v\nwant %#v", got, tc.want)
			}
			if !reflect.DeepEqual(eff, tc.effects) {
				t.Fatalf("effects\n got %#v\nwant %#v", eff, tc.effects)
			}
		})
	}
}

func TestM3PredicatesNeverSkip(t *testing.T) {
	preds := M3Predicates()
	if preds.WhenScopeClear() || preds.WhenIntakeHasScope() {
		t.Fatal("M3 predicates returned true")
	}
	def := acmeDef()
	s := mustStep(t, def, State{}, Created{}, preds, StartRun{Stage: StageTriage, Agent: "triage"})
	if s.Stage != StageTriage {
		t.Fatalf("created stage = %q", s.Stage)
	}
	s = mustStep(t, def, s, RunSucceeded{Stage: StageTriage}, preds, StartRun{Stage: StagePlanning, Agent: "spec"})
	if s.Stage != StagePlanning || s.Status != StatusOpen {
		t.Fatalf("after triage: %#v", s)
	}
}

func TestApprovalGateBlocksBuilding(t *testing.T) {
	def := acmeDef()
	preds := M3Predicates()

	t.Run("planning success requests spec and does not start building", func(t *testing.T) {
		s := mustStep(t, def, State{}, Created{}, preds, StartRun{Stage: StageTriage, Agent: "triage"})
		s = mustStep(t, def, s, RunSucceeded{Stage: StageTriage}, preds, StartRun{Stage: StagePlanning, Agent: "spec"})
		got, eff, err := Step(def, s, RunSucceeded{Stage: StagePlanning}, preds)
		if err != nil {
			t.Fatal(err)
		}
		requireEffect(t, eff, RequestApproval{Gate: GateSpec})
		forbidEffect[StartRun](t, eff)
		if got.Status != StatusAwaitingApproval || got.PendingGate != GateSpec || got.Stage != StagePlanning {
			t.Fatalf("state = %#v", got)
		}
		if _, _, err := Step(def, got, RunSucceeded{Stage: StageBuilding}, preds); !errors.Is(err, ErrStatus) {
			t.Fatalf("building success while gated: %v", err)
		}
		if _, _, err := Step(def, got, Approved{Gate: GateHandoff}, preds); !errors.Is(err, ErrWrongGate) {
			t.Fatalf("wrong gate: %v", err)
		}
		started := mustStep(t, def, got, Approved{Gate: GateSpec}, preds, StartRun{Stage: StageBuilding, Agent: "implementer"})
		if started.Status != StatusOpen || started.PendingGate != "" || started.Stage != StageBuilding {
			t.Fatalf("after approve: %#v", started)
		}
	})

	t.Run("skipped planning still blocks building", func(t *testing.T) {
		preds := Predicates{WhenScopeClear: func() bool { return true }}
		s := mustStep(t, def, State{}, Created{}, preds, StartRun{Stage: StageTriage, Agent: "triage"})
		got, eff, err := Step(def, s, RunSucceeded{Stage: StageTriage}, preds)
		if err != nil {
			t.Fatal(err)
		}
		requireEffect(t, eff, RequestApproval{Gate: GateSpec})
		forbidEffect[StartRun](t, eff)
		if got.Stage != StageTriage || got.PendingGate != GateSpec {
			t.Fatalf("state = %#v", got)
		}
	})
}

func TestRevisionLimitEscalates(t *testing.T) {
	def := acmeDef()
	preds := M3Predicates()
	s := driveToReviewing(t, def, preds)
	if s.RevisionCount != 0 || s.Stage != StageReviewing {
		t.Fatalf("reached %#v", s)
	}
	for i := 1; i <= def.RevisionLimit; i++ {
		got, eff, err := Step(def, s, RunFailed{Stage: StageReviewing}, preds)
		if err != nil {
			t.Fatal(err)
		}
		requireEffect(t, eff, StartRun{Stage: StageBuilding, Agent: "implementer"})
		forbidEffect[RequestApproval](t, eff)
		if got.RevisionCount != i || got.Status != StatusOpen || got.Stage != StageBuilding {
			t.Fatalf("revision %d: %#v", i, got)
		}
		s = mustStep(t, def, got, RunSucceeded{Stage: StageBuilding}, preds, StartRun{Stage: StageReviewing, Agent: "reviewer"})
		if s.RevisionCount != i {
			t.Fatalf("count changed on the way back to reviewing: %#v", s)
		}
	}
	got, eff, err := Step(def, s, RunFailed{Stage: StageReviewing}, preds)
	if err != nil {
		t.Fatal(err)
	}
	requireEffect(t, eff, Escalate{})
	forbidEffect[StartRun](t, eff)
	if got.RevisionCount != def.RevisionLimit+1 || got.Status != StatusOpen || got.Stage != StageReviewing {
		t.Fatalf("escalated %#v", got)
	}
	if got.LastRunOutcome != OutcomeFailed {
		t.Fatalf("outcome = %q", got.LastRunOutcome)
	}
}

func TestRejectedReturnsToPlanning(t *testing.T) {
	def := acmeDef()
	preds := M3Predicates()
	s := mustStep(t, def, State{}, Created{}, preds, StartRun{Stage: StageTriage, Agent: "triage"})
	s = mustStep(t, def, s, RunSucceeded{Stage: StageTriage}, preds, StartRun{Stage: StagePlanning, Agent: "spec"})
	s = mustStep(t, def, s, RunSucceeded{Stage: StagePlanning}, preds, RequestApproval{Gate: GateSpec})
	got := mustStep(t, def, s, Rejected{Gate: GateSpec}, preds, StartRun{Stage: StagePlanning, Agent: "spec"})
	if got.Status != StatusOpen || got.PendingGate != "" || got.Stage != StagePlanning {
		t.Fatalf("after reject: %#v", got)
	}
}

func TestStepHappyPath(t *testing.T) {
	def := acmeDef()
	preds := M3Predicates()
	s := driveToReviewing(t, def, preds)
	s = mustStep(t, def, s, RunSucceeded{Stage: StageReviewing}, preds, RequestApproval{Gate: GateHandoff})
	if s.Status != StatusAwaitingApproval || s.PendingGate != GateHandoff {
		t.Fatalf("before handoff approval: %#v", s)
	}
	s = mustStep(t, def, s, Approved{Gate: GateHandoff}, preds, MarkComplete{})
	if s.Status != StatusComplete || s.Stage != StageHumanHandoff || s.PendingGate != "" {
		t.Fatalf("complete: %#v", s)
	}
}

func TestReviewerInfraRetriesOnce(t *testing.T) {
	def := acmeDef()
	preds := M3Predicates()
	s := driveToReviewing(t, def, preds)
	if s.RevisionCount != 0 || s.ReviewerRetryUsed {
		t.Fatalf("reached %#v", s)
	}

	got, eff, err := Step(def, s, RunFailed{Stage: StageReviewing, Infra: true}, preds)
	if err != nil {
		t.Fatal(err)
	}
	requireEffect(t, eff, StartRun{Stage: StageReviewing, Agent: "reviewer"})
	forbidEffect[Escalate](t, eff)
	if got.Stage != StageReviewing || got.Status != StatusOpen || got.RevisionCount != 0 || !got.ReviewerRetryUsed {
		t.Fatalf("first infra failure: %#v", got)
	}

	// The returned state is what a caller passes after counting one prior
	// infrastructure failure. A second one revises.
	got, eff, err = Step(def, got, RunFailed{Stage: StageReviewing, Infra: true}, preds)
	if err != nil {
		t.Fatal(err)
	}
	requireEffect(t, eff, StartRun{Stage: StageBuilding, Agent: "implementer"})
	if got.Stage != StageBuilding || got.RevisionCount != 1 || got.ReviewerRetryUsed {
		t.Fatalf("second infra failure: %#v", got)
	}

	// The building run opens a new window, so the next review can retry once.
	got = mustStep(t, def, got, RunSucceeded{Stage: StageBuilding}, preds, StartRun{Stage: StageReviewing, Agent: "reviewer"})
	if got.ReviewerRetryUsed || got.RevisionCount != 1 {
		t.Fatalf("back to reviewing: %#v", got)
	}
	got, eff, err = Step(def, got, RunFailed{Stage: StageReviewing, Infra: true}, preds)
	if err != nil {
		t.Fatal(err)
	}
	requireEffect(t, eff, StartRun{Stage: StageReviewing, Agent: "reviewer"})
	if got.Stage != StageReviewing || got.RevisionCount != 1 || !got.ReviewerRetryUsed {
		t.Fatalf("infra failure after the next build: %#v", got)
	}
}

func TestReviewerInfraPastLimitDoesNotStartAnotherReview(t *testing.T) {
	def := acmeDef()
	preds := M3Predicates()
	s := driveToReviewing(t, def, preds)
	for s.RevisionCount < def.RevisionLimit {
		got, eff, err := Step(def, s, RunFailed{Stage: StageReviewing}, preds)
		if err != nil {
			t.Fatal(err)
		}
		requireEffect(t, eff, StartRun{Stage: StageBuilding, Agent: "implementer"})
		s = mustStep(t, def, got, RunSucceeded{Stage: StageBuilding}, preds, StartRun{Stage: StageReviewing, Agent: "reviewer"})
	}
	if s.RevisionCount != def.RevisionLimit || s.ReviewerRetryUsed {
		t.Fatalf("at the limit: %#v", s)
	}

	got, eff, err := Step(def, s, RunFailed{Stage: StageReviewing, Infra: true}, preds)
	if err != nil {
		t.Fatal(err)
	}
	requireEffect(t, eff, StartRun{Stage: StageReviewing, Agent: "reviewer"})
	forbidEffect[Escalate](t, eff)
	if got.RevisionCount != def.RevisionLimit || !got.ReviewerRetryUsed || got.Stage != StageReviewing {
		t.Fatalf("retry at the limit: %#v", got)
	}

	got, eff, err = Step(def, got, RunFailed{Stage: StageReviewing, Infra: true}, preds)
	if err != nil {
		t.Fatal(err)
	}
	requireEffect(t, eff, Escalate{})
	forbidEffect[StartRun](t, eff)
	if got.Stage != StageReviewing || got.RevisionCount != def.RevisionLimit+1 || !got.ReviewerRetryUsed {
		t.Fatalf("escalate: %#v", got)
	}

	again, eff, err := Step(def, got, RunFailed{Stage: StageReviewing, Infra: true}, preds)
	if err != nil {
		t.Fatal(err)
	}
	requireEffect(t, eff, Escalate{})
	forbidEffect[StartRun](t, eff)
	if again.Stage != StageReviewing || !again.ReviewerRetryUsed {
		t.Fatalf("later infra failure: %#v", again)
	}
}

func TestReviewerAgentFailureStillRevises(t *testing.T) {
	def := acmeDef()
	preds := M3Predicates()
	s := driveToReviewing(t, def, preds)
	got, eff, err := Step(def, s, RunFailed{Stage: StageReviewing}, preds)
	if err != nil {
		t.Fatal(err)
	}
	requireEffect(t, eff, StartRun{Stage: StageBuilding, Agent: "implementer"})
	if got.RevisionCount != 1 || got.ReviewerRetryUsed || got.Stage != StageBuilding {
		t.Fatalf("agent failure: %#v", got)
	}
}

func TestRunFailedDoesNotRetry(t *testing.T) {
	def := acmeDef()
	preds := M3Predicates()
	s := mustStep(t, def, State{}, Created{}, preds, StartRun{Stage: StageTriage, Agent: "triage"})
	assertNoRetry(t, def, preds, s, StageTriage)
	s = mustStep(t, def, s, RunSucceeded{Stage: StageTriage}, preds, StartRun{Stage: StagePlanning, Agent: "spec"})
	assertNoRetry(t, def, preds, s, StagePlanning)
	s = mustStep(t, def, s, RunSucceeded{Stage: StagePlanning}, preds, RequestApproval{Gate: GateSpec})
	s = mustStep(t, def, s, Approved{Gate: GateSpec}, preds, StartRun{Stage: StageBuilding, Agent: "implementer"})
	assertNoRetry(t, def, preds, s, StageBuilding)
}

func assertNoRetry(t *testing.T, def Def, preds Predicates, s State, stage string) {
	t.Helper()
	got, eff, err := Step(def, s, RunFailed{Stage: stage}, preds)
	if err != nil {
		t.Fatal(err)
	}
	requireEffect(t, eff, Escalate{})
	forbidEffect[StartRun](t, eff)
	if got.Status != StatusOpen || got.Stage != stage || got.RevisionCount != s.RevisionCount {
		t.Fatalf("%s failed: %#v", stage, got)
	}
	if got.LastRunOutcome != OutcomeFailed {
		t.Fatalf("%s outcome = %q", stage, got.LastRunOutcome)
	}
}

func TestStepConcurrent(t *testing.T) {
	def := acmeDef()
	preds := M3Predicates()
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, eff, err := Step(def, State{}, Created{}, preds)
			if err != nil || len(eff) != 1 {
				t.Errorf("created: %v %#v", err, eff)
				return
			}
			s, eff, err = Step(def, s, RunSucceeded{Stage: StageTriage}, preds)
			if err != nil {
				t.Errorf("triage: %v", err)
				return
			}
			if _, ok := eff[0].(StartRun); !ok || s.Stage != StagePlanning {
				t.Errorf("M3 skipped or failed: %#v %#v", s, eff)
			}
		}()
	}
	wg.Wait()
}

func TestPackageIsPure(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	entries, err := os.ReadDir(filepath.Dir(file))
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"errors": true, "fmt": true, "strings": true}
	fset := token.NewFileSet()
	var saw bool
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		saw = true
		path := filepath.Join(filepath.Dir(file), name)
		parsed, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			pkg, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if !allowed[pkg] {
				t.Errorf("%s imports %s", name, pkg)
			}
		}
	}
	if !saw {
		t.Fatal("no production files")
	}
}

type stepCase struct {
	name    string
	def     *Def
	preds   *Predicates
	state   State
	event   Event
	want    State
	effects []Effect
	wantErr error
}

func stepCases() []stepCase {
	scopeClear := &Predicates{WhenScopeClear: func() bool { return true }}
	intakeScope := &Predicates{WhenIntakeHasScope: func() bool { return true }}
	zeroPreds := &Predicates{}
	return []stepCase{
		{
			name:    "created starts triage",
			event:   Created{},
			want:    State{Stage: StageTriage, Status: StatusOpen, LastStageAgent: "triage"},
			effects: []Effect{StartRun{Stage: StageTriage, Agent: "triage"}},
		},
		{
			name:    "created twice",
			state:   State{Stage: StageTriage, Status: StatusOpen, LastStageAgent: "triage"},
			event:   Created{},
			wantErr: ErrAlreadyStarted,
		},
		{
			name:    "created on a complete item",
			state:   State{Stage: StageHumanHandoff, Status: StatusComplete},
			event:   Created{},
			wantErr: ErrTerminal,
		},
		{
			name:    "empty pipeline",
			def:     defWith(func(d *Def) { d.Stages = nil }),
			event:   Created{},
			wantErr: ErrNoStage,
		},
		{
			name:    "every stage skipped",
			preds:   &Predicates{WhenScopeClear: func() bool { return true }, WhenIntakeHasScope: func() bool { return true }},
			def:     defWith(func(d *Def) { d.Skip[StageBuilding] = PredWhenScopeClear; d.Skip[StageReviewing] = PredWhenScopeClear }),
			event:   Created{},
			wantErr: ErrNoStage,
		},
		{
			name:    "unknown predicate on create",
			def:     defWith(func(d *Def) { d.Skip[StageTriage] = "whenNever" }),
			event:   Created{},
			wantErr: ErrUnknownPredicate,
		},
		{
			name:    "intake scope skips triage",
			preds:   intakeScope,
			event:   Created{},
			want:    State{Stage: StagePlanning, Status: StatusOpen, LastStageAgent: "spec"},
			effects: []Effect{StartRun{Stage: StagePlanning, Agent: "spec"}},
		},
		{
			name:    "triage success starts planning",
			state:   openStage(StageTriage, "triage", 0),
			event:   RunSucceeded{Stage: StageTriage},
			want:    State{Stage: StagePlanning, Status: StatusOpen, LastStageAgent: "spec"},
			effects: []Effect{StartRun{Stage: StagePlanning, Agent: "spec"}},
		},
		{
			name:    "nil predicate func does not skip",
			preds:   zeroPreds,
			state:   openStage(StageTriage, "triage", 0),
			event:   RunSucceeded{Stage: StageTriage},
			want:    State{Stage: StagePlanning, Status: StatusOpen, LastStageAgent: "spec"},
			effects: []Effect{StartRun{Stage: StagePlanning, Agent: "spec"}},
		},
		{
			name:  "scope clear skips planning and requests spec",
			preds: scopeClear,
			state: openStage(StageTriage, "triage", 0),
			event: RunSucceeded{Stage: StageTriage},
			want: State{
				Stage: StageTriage, Status: StatusAwaitingApproval, PendingGate: GateSpec,
				LastStageAgent: "triage", LastRunOutcome: OutcomeSucceeded,
			},
			effects: []Effect{RequestApproval{Gate: GateSpec}},
		},
		{
			name:  "skipping building skips the spec gate",
			preds: scopeClear,
			def:   defWith(func(d *Def) { d.Skip[StageBuilding] = PredWhenScopeClear }),
			state: openStage(StagePlanning, "spec", 0),
			event: RunSucceeded{Stage: StagePlanning},
			want:  State{Stage: StageReviewing, Status: StatusOpen, LastStageAgent: "reviewer"},
			effects: []Effect{
				StartRun{Stage: StageReviewing, Agent: "reviewer"},
			},
		},
		{
			name:  "planning success requests spec",
			state: openStage(StagePlanning, "spec", 0),
			event: RunSucceeded{Stage: StagePlanning},
			want: State{
				Stage: StagePlanning, Status: StatusAwaitingApproval, PendingGate: GateSpec,
				LastStageAgent: "spec", LastRunOutcome: OutcomeSucceeded,
			},
			effects: []Effect{RequestApproval{Gate: GateSpec}},
		},
		{
			name: "spec not required starts building",
			def: defWith(func(d *Def) {
				d.Approvals[GateSpec] = ApprovalGate{Required: false, Approvers: "role:editor"}
			}),
			state:   openStage(StagePlanning, "spec", 0),
			event:   RunSucceeded{Stage: StagePlanning},
			want:    State{Stage: StageBuilding, Status: StatusOpen, LastStageAgent: "implementer"},
			effects: []Effect{StartRun{Stage: StageBuilding, Agent: "implementer"}},
		},
		{
			name:    "missing building agent does not open the gate",
			def:     defWith(func(d *Def) { delete(d.Agents, StageBuilding) }),
			state:   openStage(StagePlanning, "spec", 0),
			event:   RunSucceeded{Stage: StagePlanning},
			wantErr: ErrNoAgent,
		},
		{
			name: "approved spec starts building",
			state: State{
				Stage: StagePlanning, Status: StatusAwaitingApproval, PendingGate: GateSpec,
				LastStageAgent: "spec", LastRunOutcome: OutcomeSucceeded,
			},
			event:   Approved{Gate: GateSpec},
			want:    State{Stage: StageBuilding, Status: StatusOpen, LastStageAgent: "implementer"},
			effects: []Effect{StartRun{Stage: StageBuilding, Agent: "implementer"}},
		},
		{
			name: "approved wrong gate",
			state: State{
				Stage: StagePlanning, Status: StatusAwaitingApproval, PendingGate: GateSpec,
				LastStageAgent: "spec", LastRunOutcome: OutcomeSucceeded,
			},
			event:   Approved{Gate: GateHandoff},
			wantErr: ErrWrongGate,
		},
		{
			name:    "approved with nothing pending",
			state:   openStage(StageBuilding, "implementer", 0),
			event:   Approved{Gate: GateSpec},
			wantErr: ErrNoPending,
		},
		{
			name: "rejected spec returns to planning",
			state: State{
				Stage: StagePlanning, Status: StatusAwaitingApproval, PendingGate: GateSpec,
				RevisionCount: 2, LastStageAgent: "spec", LastRunOutcome: OutcomeSucceeded,
			},
			event: Rejected{Gate: GateSpec},
			want: State{
				Stage: StagePlanning, Status: StatusOpen, RevisionCount: 2, LastStageAgent: "spec",
			},
			effects: []Effect{StartRun{Stage: StagePlanning, Agent: "spec"}},
		},
		{
			name: "rejected handoff returns to planning",
			state: State{
				Stage: StageReviewing, Status: StatusAwaitingApproval, PendingGate: GateHandoff,
				RevisionCount: 1, LastStageAgent: "reviewer", LastRunOutcome: OutcomeSucceeded,
			},
			event: Rejected{Gate: GateHandoff},
			want: State{
				Stage: StagePlanning, Status: StatusOpen, RevisionCount: 1, LastStageAgent: "spec",
			},
			effects: []Effect{StartRun{Stage: StagePlanning, Agent: "spec"}},
		},
		{
			name: "rejected wrong gate",
			state: State{
				Stage: StagePlanning, Status: StatusAwaitingApproval, PendingGate: GateSpec,
			},
			event:   Rejected{Gate: GateHandoff},
			wantErr: ErrWrongGate,
		},
		{
			name:    "building success starts reviewing",
			state:   openStage(StageBuilding, "implementer", 1),
			event:   RunSucceeded{Stage: StageBuilding},
			want:    State{Stage: StageReviewing, Status: StatusOpen, RevisionCount: 1, LastStageAgent: "reviewer"},
			effects: []Effect{StartRun{Stage: StageReviewing, Agent: "reviewer"}},
		},
		{
			name:  "reviewing success requests handoff",
			state: openStage(StageReviewing, "reviewer", 1),
			event: RunSucceeded{Stage: StageReviewing},
			want: State{
				Stage: StageReviewing, Status: StatusAwaitingApproval, RevisionCount: 1,
				PendingGate: GateHandoff, LastStageAgent: "reviewer", LastRunOutcome: OutcomeSucceeded,
			},
			effects: []Effect{RequestApproval{Gate: GateHandoff}},
		},
		{
			name: "approved handoff completes",
			state: State{
				Stage: StageReviewing, Status: StatusAwaitingApproval, PendingGate: GateHandoff,
				LastStageAgent: "reviewer", LastRunOutcome: OutcomeSucceeded,
			},
			event: Approved{Gate: GateHandoff},
			want: State{
				Stage: StageHumanHandoff, Status: StatusComplete,
				LastStageAgent: "reviewer", LastRunOutcome: OutcomeSucceeded,
			},
			effects: []Effect{MarkComplete{}},
		},
		{
			name:  "handoff not required completes",
			def:   defWith(func(d *Def) { d.Approvals[GateHandoff] = ApprovalGate{Required: false} }),
			state: openStage(StageReviewing, "reviewer", 0),
			event: RunSucceeded{Stage: StageReviewing},
			want: State{
				Stage: StageHumanHandoff, Status: StatusComplete,
				LastStageAgent: "reviewer", LastRunOutcome: OutcomeSucceeded,
			},
			effects: []Effect{MarkComplete{}},
		},
		{
			name: "merge required does not block completion",
			def: defWith(func(d *Def) {
				d.Approvals[GateHandoff] = ApprovalGate{Required: false}
				d.Approvals[GateMerge] = ApprovalGate{Required: true, Approvers: "role:user"}
			}),
			state: openStage(StageReviewing, "reviewer", 0),
			event: RunSucceeded{Stage: StageReviewing},
			want: State{
				Stage: StageHumanHandoff, Status: StatusComplete,
				LastStageAgent: "reviewer", LastRunOutcome: OutcomeSucceeded,
			},
			effects: []Effect{MarkComplete{}},
		},
		{
			name: "human handoff stage waits on its gate then runs",
			def: defWith(func(d *Def) {
				d.Stages = append(d.Stages, StageHumanHandoff)
				d.Agents[StageHumanHandoff] = "handoff"
			}),
			state: openStage(StageReviewing, "reviewer", 0),
			event: RunSucceeded{Stage: StageReviewing},
			want: State{
				Stage: StageReviewing, Status: StatusAwaitingApproval, PendingGate: GateHandoff,
				LastStageAgent: "reviewer", LastRunOutcome: OutcomeSucceeded,
			},
			effects: []Effect{RequestApproval{Gate: GateHandoff}},
		},
		{
			name: "approved handoff starts the handoff stage",
			def: defWith(func(d *Def) {
				d.Stages = append(d.Stages, StageHumanHandoff)
				d.Agents[StageHumanHandoff] = "handoff"
			}),
			state: State{
				Stage: StageReviewing, Status: StatusAwaitingApproval, PendingGate: GateHandoff,
				LastStageAgent: "reviewer", LastRunOutcome: OutcomeSucceeded,
			},
			event:   Approved{Gate: GateHandoff},
			want:    State{Stage: StageHumanHandoff, Status: StatusOpen, LastStageAgent: "handoff"},
			effects: []Effect{StartRun{Stage: StageHumanHandoff, Agent: "handoff"}},
		},
		{
			name: "handoff stage success completes",
			def: defWith(func(d *Def) {
				d.Stages = append(d.Stages, StageHumanHandoff)
				d.Agents[StageHumanHandoff] = "handoff"
			}),
			state: openStage(StageHumanHandoff, "handoff", 0),
			event: RunSucceeded{Stage: StageHumanHandoff},
			want: State{
				Stage: StageHumanHandoff, Status: StatusComplete,
				LastStageAgent: "handoff", LastRunOutcome: OutcomeSucceeded,
			},
			effects: []Effect{MarkComplete{}},
		},
		{
			name:    "reviewing failure under the limit starts building",
			state:   openStage(StageReviewing, "reviewer", 0),
			event:   RunFailed{Stage: StageReviewing},
			want:    State{Stage: StageBuilding, Status: StatusOpen, RevisionCount: 1, LastStageAgent: "implementer"},
			effects: []Effect{StartRun{Stage: StageBuilding, Agent: "implementer"}},
		},
		{
			name:  "reviewing infra failure retries reviewing once",
			state: openStage(StageReviewing, "reviewer", 0),
			event: RunFailed{Stage: StageReviewing, Infra: true},
			want: State{
				Stage: StageReviewing, Status: StatusOpen, LastStageAgent: "reviewer",
				ReviewerRetryUsed: true,
			},
			effects: []Effect{StartRun{Stage: StageReviewing, Agent: "reviewer"}},
		},
		{
			name:  "reviewing infra failure at the revision limit still retries",
			state: openStage(StageReviewing, "reviewer", 3),
			event: RunFailed{Stage: StageReviewing, Infra: true},
			want: State{
				Stage: StageReviewing, Status: StatusOpen, RevisionCount: 3,
				LastStageAgent: "reviewer", ReviewerRetryUsed: true,
			},
			effects: []Effect{StartRun{Stage: StageReviewing, Agent: "reviewer"}},
		},
		{
			name: "reviewing infra failure with the retry used starts building",
			state: State{
				Stage: StageReviewing, Status: StatusOpen, LastStageAgent: "reviewer",
				ReviewerRetryUsed: true,
			},
			event:   RunFailed{Stage: StageReviewing, Infra: true},
			want:    State{Stage: StageBuilding, Status: StatusOpen, RevisionCount: 1, LastStageAgent: "implementer"},
			effects: []Effect{StartRun{Stage: StageBuilding, Agent: "implementer"}},
		},
		{
			name:  "building infra failure does not retry",
			state: openStage(StageBuilding, "implementer", 1),
			event: RunFailed{Stage: StageBuilding, Infra: true},
			want: State{
				Stage: StageBuilding, Status: StatusOpen, RevisionCount: 1,
				LastStageAgent: "implementer", LastRunOutcome: OutcomeFailed,
			},
			effects: []Effect{Escalate{}},
		},
		{
			name:  "planning infra failure does not retry",
			state: openStage(StagePlanning, "spec", 0),
			event: RunFailed{Stage: StagePlanning, Infra: true},
			want: State{
				Stage: StagePlanning, Status: StatusOpen,
				LastStageAgent: "spec", LastRunOutcome: OutcomeFailed,
			},
			effects: []Effect{Escalate{}},
		},
		{
			name:  "triage infra failure does not retry",
			state: openStage(StageTriage, "triage", 0),
			event: RunFailed{Stage: StageTriage, Infra: true},
			want: State{
				Stage: StageTriage, Status: StatusOpen,
				LastStageAgent: "triage", LastRunOutcome: OutcomeFailed,
			},
			effects: []Effect{Escalate{}},
		},
		{
			name:    "reviewing failure at the limit still starts building",
			state:   openStage(StageReviewing, "reviewer", 2),
			event:   RunFailed{Stage: StageReviewing},
			want:    State{Stage: StageBuilding, Status: StatusOpen, RevisionCount: 3, LastStageAgent: "implementer"},
			effects: []Effect{StartRun{Stage: StageBuilding, Agent: "implementer"}},
		},
		{
			name:  "reviewing failure past the limit escalates",
			state: openStage(StageReviewing, "reviewer", 3),
			event: RunFailed{Stage: StageReviewing},
			want: State{
				Stage: StageReviewing, Status: StatusOpen, RevisionCount: 4,
				LastStageAgent: "reviewer", LastRunOutcome: OutcomeFailed,
			},
			effects: []Effect{Escalate{}},
		},
		{
			name:  "revision limit zero escalates on the first review failure",
			def:   defWith(func(d *Def) { d.RevisionLimit = 0 }),
			state: openStage(StageReviewing, "reviewer", 0),
			event: RunFailed{Stage: StageReviewing},
			want: State{
				Stage: StageReviewing, Status: StatusOpen, RevisionCount: 1,
				LastStageAgent: "reviewer", LastRunOutcome: OutcomeFailed,
			},
			effects: []Effect{Escalate{}},
		},
		{
			name:  "review success past the limit still advances",
			state: openStage(StageReviewing, "reviewer", 4),
			event: RunSucceeded{Stage: StageReviewing},
			want: State{
				Stage: StageReviewing, Status: StatusAwaitingApproval, RevisionCount: 4,
				PendingGate: GateHandoff, LastStageAgent: "reviewer", LastRunOutcome: OutcomeSucceeded,
			},
			effects: []Effect{RequestApproval{Gate: GateHandoff}},
		},
		{
			name:    "reviewing failure without a building stage",
			def:     defWith(func(d *Def) { d.Stages = []string{StageTriage, StagePlanning, StageReviewing} }),
			state:   openStage(StageReviewing, "reviewer", 0),
			event:   RunFailed{Stage: StageReviewing},
			wantErr: ErrNoStage,
		},
		{
			name:    "reviewing failure without a building agent",
			def:     defWith(func(d *Def) { delete(d.Agents, StageBuilding) }),
			state:   openStage(StageReviewing, "reviewer", 0),
			event:   RunFailed{Stage: StageReviewing},
			wantErr: ErrNoAgent,
		},
		{
			name:  "building failure stays open and escalates",
			state: openStage(StageBuilding, "implementer", 1),
			event: RunFailed{Stage: StageBuilding},
			want: State{
				Stage: StageBuilding, Status: StatusOpen, RevisionCount: 1,
				LastStageAgent: "implementer", LastRunOutcome: OutcomeFailed,
			},
			effects: []Effect{Escalate{}},
		},
		{
			name:  "planning failure does not retry",
			state: openStage(StagePlanning, "spec", 0),
			event: RunFailed{Stage: StagePlanning},
			want: State{
				Stage: StagePlanning, Status: StatusOpen,
				LastStageAgent: "spec", LastRunOutcome: OutcomeFailed,
			},
			effects: []Effect{Escalate{}},
		},
		{
			name:    "stale stage",
			state:   openStage(StagePlanning, "spec", 0),
			event:   RunSucceeded{Stage: StageTriage},
			wantErr: ErrStaleStage,
		},
		{
			name:    "empty event stage",
			state:   openStage(StageTriage, "triage", 0),
			event:   RunSucceeded{},
			wantErr: ErrStaleStage,
		},
		{
			name: "run success does not bypass a pending gate",
			state: State{
				Stage: StagePlanning, Status: StatusAwaitingApproval, PendingGate: GateSpec,
				LastStageAgent: "spec", LastRunOutcome: OutcomeSucceeded,
			},
			event:   RunSucceeded{Stage: StageBuilding},
			wantErr: ErrStatus,
		},
		{
			name: "run failure while awaiting approval",
			state: State{
				Stage: StagePlanning, Status: StatusAwaitingApproval, PendingGate: GateSpec,
			},
			event:   RunFailed{Stage: StagePlanning},
			wantErr: ErrStatus,
		},
		{
			name:    "run success on a complete item",
			state:   State{Stage: StageHumanHandoff, Status: StatusComplete},
			event:   RunSucceeded{Stage: StageHumanHandoff},
			wantErr: ErrTerminal,
		},
		{
			name:    "stage not in the pipeline",
			state:   State{Stage: "nope", Status: StatusOpen},
			event:   RunSucceeded{Stage: "nope"},
			wantErr: ErrNoStage,
		},
		{
			name:    "unknown predicate before the next stage",
			def:     defWith(func(d *Def) { d.Skip[StageReviewing] = "whenNever" }),
			state:   openStage(StageBuilding, "implementer", 0),
			event:   RunSucceeded{Stage: StageBuilding},
			wantErr: ErrUnknownPredicate,
		},
		{
			name:  "cancel from open",
			state: openStage(StageBuilding, "implementer", 1),
			event: Cancel{},
			want: State{
				Stage: StageBuilding, Status: StatusCancelled, RevisionCount: 1,
				LastStageAgent: "implementer", LastRunOutcome: OutcomeCancelled,
			},
			effects: []Effect{MarkCancelled{}},
		},
		{
			name: "cancel from awaiting approval",
			state: State{
				Stage: StagePlanning, Status: StatusAwaitingApproval, PendingGate: GateSpec,
				LastStageAgent: "spec", LastRunOutcome: OutcomeSucceeded,
			},
			event: Cancel{},
			want: State{
				Stage: StagePlanning, Status: StatusCancelled,
				LastStageAgent: "spec", LastRunOutcome: OutcomeCancelled,
			},
			effects: []Effect{MarkCancelled{}},
		},
		{
			name: "cancel from awaiting input",
			state: State{
				Stage: StageBuilding, Status: StatusAwaitingInput, LastStageAgent: "implementer",
			},
			event: Cancel{},
			want: State{
				Stage: StageBuilding, Status: StatusCancelled,
				LastStageAgent: "implementer", LastRunOutcome: OutcomeCancelled,
			},
			effects: []Effect{MarkCancelled{}},
		},
		{
			name:    "cancel complete",
			state:   State{Status: StatusComplete, Stage: StageHumanHandoff},
			event:   Cancel{},
			wantErr: ErrTerminal,
		},
		{
			name:    "cancel cancelled",
			state:   State{Status: StatusCancelled, Stage: StageBuilding},
			event:   Cancel{},
			wantErr: ErrTerminal,
		},
		{
			name:    "cancel before create",
			event:   Cancel{},
			wantErr: ErrStatus,
		},
		{
			name:    "ask input",
			state:   openStage(StageBuilding, "implementer", 0),
			event:   AskInput{},
			want:    State{Stage: StageBuilding, Status: StatusAwaitingInput, LastStageAgent: "implementer"},
			effects: []Effect{Noop{}},
		},
		{
			name: "ask input while awaiting approval",
			state: State{
				Stage: StagePlanning, Status: StatusAwaitingApproval, PendingGate: GateSpec,
			},
			event:   AskInput{},
			wantErr: ErrStatus,
		},
		{
			name: "resume input",
			state: State{
				Stage: StageBuilding, Status: StatusAwaitingInput, RevisionCount: 2, LastStageAgent: "implementer",
			},
			event: ResumeInput{},
			want: State{
				Stage: StageBuilding, Status: StatusOpen, RevisionCount: 2, LastStageAgent: "implementer",
			},
			effects: []Effect{Noop{}},
		},
		{
			name:    "resume when open",
			state:   openStage(StageBuilding, "implementer", 0),
			event:   ResumeInput{},
			wantErr: ErrNotAwaitingInput,
		},
		{
			name:    "resume when complete",
			state:   State{Status: StatusComplete},
			event:   ResumeInput{},
			wantErr: ErrTerminal,
		},
		{
			name:    "unknown event",
			event:   "nope",
			wantErr: ErrUnknownEvent,
		},
		{
			name:    "nil event",
			wantErr: ErrUnknownEvent,
		},
		{
			name:    "pointer event",
			event:   &Created{},
			wantErr: ErrUnknownEvent,
		},
	}
}

func acmeDef() Def {
	return Def{
		Stages: []string{StageTriage, StagePlanning, StageBuilding, StageReviewing},
		Agents: map[string]string{
			StageTriage:    "triage",
			StagePlanning:  "spec",
			StageBuilding:  "implementer",
			StageReviewing: "reviewer",
		},
		Skip: map[string]string{
			StageTriage:   PredWhenIntakeHasScope,
			StagePlanning: PredWhenScopeClear,
		},
		Approvals: map[string]ApprovalGate{
			GateSpec:    {Required: true, Approvers: "role:editor"},
			GateHandoff: {Required: true, Approvers: "role:user"},
			GateMerge:   {Required: false},
		},
		RevisionLimit: 3,
	}
}

func defWith(fn func(*Def)) *Def {
	d := acmeDef()
	fn(&d)
	return &d
}

func openStage(stage, agent string, rev int) State {
	return State{Stage: stage, Status: StatusOpen, RevisionCount: rev, LastStageAgent: agent}
}

func mustStep(t *testing.T, def Def, s State, ev Event, preds Predicates, want Effect) State {
	t.Helper()
	got, eff, err := Step(def, s, ev, preds)
	if err != nil {
		t.Fatalf("Step(%T): %v", ev, err)
	}
	requireEffect(t, eff, want)
	return got
}

func requireEffect(t *testing.T, eff []Effect, want Effect) {
	t.Helper()
	if len(eff) != 1 || !reflect.DeepEqual(eff[0], want) {
		t.Fatalf("effects = %#v, want [%#v]", eff, want)
	}
}

func forbidEffect[T any](t *testing.T, eff []Effect) {
	t.Helper()
	for _, e := range eff {
		if _, ok := e.(T); ok {
			t.Fatalf("forbidden effect in %#v", eff)
		}
	}
}

func driveToReviewing(t *testing.T, def Def, preds Predicates) State {
	t.Helper()
	s := mustStep(t, def, State{}, Created{}, preds, StartRun{Stage: StageTriage, Agent: "triage"})
	s = mustStep(t, def, s, RunSucceeded{Stage: StageTriage}, preds, StartRun{Stage: StagePlanning, Agent: "spec"})
	s = mustStep(t, def, s, RunSucceeded{Stage: StagePlanning}, preds, RequestApproval{Gate: GateSpec})
	s = mustStep(t, def, s, Approved{Gate: GateSpec}, preds, StartRun{Stage: StageBuilding, Agent: "implementer"})
	return mustStep(t, def, s, RunSucceeded{Stage: StageBuilding}, preds, StartRun{Stage: StageReviewing, Agent: "reviewer"})
}

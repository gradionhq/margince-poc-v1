// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package runner

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// agent_run.degrade_reason is read by the ordinary human the run acted for, so
// the wrapped cause behind a degrade must not reach it. The causes that matter
// are the ones the runner does not author: a provider error (which embeds the
// vendor's own message, and in the credential arms the key it echoed back) and a
// JSON decoder error (which quotes what the model wrote). Both are pinned here
// with a marker no reason may ever contain.
const providerLeak = "sk-live-NEVER-ON-A-SCREEN"

// failingBrain is a provider that refuses the completion the way a real one
// does: an error whose text is the upstream body, verbatim.
type failingBrain struct{}

func (failingBrain) Complete(context.Context, model.Request) (model.Response, Meta, error) {
	return model.Response{}, Meta{}, errors.New(
		"ai: openai-compat: invalid_api_key: Incorrect API key provided: " + providerLeak + " (http 401)",
	)
}

// leakingOutput is a model that cannot produce a step and names a secret while
// failing, which the JSON decoder then quotes back in its own error.
type leakingOutput struct{}

func (leakingOutput) Complete(context.Context, model.Request) (model.Response, Meta, error) {
	return model.Response{Text: `{"` + providerLeak + `": 1}`, OutputTokens: 1}, Meta{}, nil
}

func TestADegradeReasonNeverCarriesTheCauseItDegradedOn(t *testing.T) {
	for _, tc := range []struct {
		name       string
		brain      Brain
		cancelled  bool
		wantReason string
		wantCause  string
	}{
		{
			name: "the provider refused the call", brain: failingBrain{},
			wantReason: "model call failed", wantCause: providerLeak,
		},
		{
			name: "the model could not produce a step", brain: leakingOutput{},
			wantReason: "failed validation", wantCause: providerLeak,
		},
		{
			name: "the run ran out of wall clock", brain: failingBrain{}, cancelled: true,
			wantReason: "wall clock exceeded", wantCause: "context canceled",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			res, err := New(&fakeSurface{}, tc.brain).Run(ctx, Job{Goal: "g", TriggerRef: "morning_brief:2026-08-21"})
			if err != nil {
				t.Fatalf("a degrade is an answer, not an error: %v", err)
			}
			if res.Outcome != OutcomeDegraded {
				t.Fatalf("want a degraded run, got %q", res.Outcome)
			}
			if !strings.Contains(res.DegradeReason, tc.wantReason) {
				t.Errorf("the reason must name what stopped the run; want %q in %q", tc.wantReason, res.DegradeReason)
			}
			// The other half of the trade: the cause is kept for an operator
			// surface, on a field nothing persists or serializes.
			if !strings.Contains(res.DegradeCause, tc.wantCause) {
				t.Errorf("the cause must survive for an operator; want %q in %q", tc.wantCause, res.DegradeCause)
			}
			if !strings.Contains(res.DegradeDetail(), res.DegradeReason) {
				t.Errorf("the operator's detail must still name the reason, got %q", res.DegradeDetail())
			}
			// The partial result carries the same reason and reaches the same
			// reader, so it is held to the same rule.
			for field, text := range map[string]string{
				"degrade_reason": res.DegradeReason,
				"result":         string(res.Final),
			} {
				if strings.Contains(text, providerLeak) {
					t.Errorf("%s carries the underlying cause verbatim: %q", field, text)
				}
			}
		})
	}
}

// Every reason ANY writer of agent_run.degrade_reason can put there is spelled in
// this package and owes the reader an action, which is what stops the next author
// reaching for the cause's own text to make a reason informative. The loop's own
// arms and the out-of-loop closes are one list because they are one column.
func TestEveryClosedReasonSaysWhatStoppedTheRun(t *testing.T) {
	for _, reason := range []string{
		reasonWallClockExceeded, reasonModelCallFailed,
		reasonStepBudgetExhausted, reasonOutputTokenBudgetExhausted,
		invalidOutputReason(consecutiveInvalidLimit),
		string(FailureEditedApprovalCarriedNoChange), string(FailurePassportNoLongerValid),
		string(FailureSpecLeftTheCatalog), string(FailureRunFaulted),
		string(FailureCouldNotStart),
	} {
		if strings.TrimSpace(reason) == "" {
			t.Error("a degrade with no reason tells the reader only that something went wrong")
		}
		if strings.Contains(reason, "%") || strings.Contains(reason, "\n") {
			t.Errorf("a reason is one plain sentence for a person, got %q", reason)
		}
	}
	if got := invalidOutputReason(3); !strings.Contains(got, "3 times") {
		t.Errorf("the attempt count is server-authored and belongs in the reason, got %q", got)
	}
}

// The operator's half of the trade: the cause is withheld from the row, so it
// has to be somewhere. A degrade whose cause reached neither is a run nobody can
// diagnose.
func TestTheCauseWithheldFromTheRowIsHandedToTheOperatorLog(t *testing.T) {
	logged := captureRunnerLog(t)
	res, err := New(&fakeSurface{}, failingBrain{}).Run(context.Background(),
		Job{Goal: "g", TriggerRef: "morning_brief:2026-08-21"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeDegraded {
		t.Fatalf("want a degraded run, got %q", res.Outcome)
	}
	entry := logged.String()
	if !strings.Contains(entry, providerLeak) {
		t.Errorf("the provider's message must survive for forensics, log was: %q", entry)
	}
	if !strings.Contains(entry, "morning_brief:2026-08-21") {
		t.Errorf("the log must name the occurrence it is about, log was: %q", entry)
	}
}

// captureRunnerLog redirects the operator log for one test and restores it. The
// runner writes diagnostics to the process default rather than an injected
// logger, so reading them back means standing in front of that default.
func captureRunnerLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

// The list above is a list, and a list drifts: a FailureReason added without a
// line in it would go uncovered while the suite still read as complete. So the
// declared vocabulary is read off this package's own source and matched against
// what the test actually exercises.
func TestNoFailureReasonIsDeclaredWithoutBeingCovered(t *testing.T) {
	const file = "degrade.go"
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}

	covered := map[string]bool{
		"FailureEditedApprovalCarriedNoChange": true,
		"FailurePassportNoLongerValid":         true,
		"FailureSpecLeftTheCatalog":            true,
		"FailureRunFaulted":                    true,
		"FailureCouldNotStart":                 true,
	}
	declared := 0
	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || !isFailureReason(value.Type) {
				continue
			}
			for _, name := range value.Names {
				declared++
				if !covered[name.Name] {
					t.Errorf("%s declares FailureReason %s, which no test exercises — add it to "+
						"TestEveryClosedReasonSaysWhatStoppedTheRun and to the map here, so a reason "+
						"cannot reach a reader's screen unmeasured", file, name.Name)
				}
			}
		}
	}
	if declared != len(covered) {
		t.Errorf("%s declares %d FailureReason constants but this gate expects %d — a removed one "+
			"leaves a stale claim behind", file, declared, len(covered))
	}
}

func isFailureReason(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "FailureReason"
}

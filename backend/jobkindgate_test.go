// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The registration gate's own falsification. Every shape the tree actually
// uses, proven accepted; the shape it exists to reject, proven rejected.
// Compiled with `go build` rather than asserted by eye, because the claim is
// specifically about what the COMPILER does — and a gate that blocks a
// legitimate author gets weakened by the person it stopped, which is the hole
// the next undeclared job walks back in through.
//
// Two halves, and the second is the one that binds. The scratch module below
// models the SHAPE of the mechanism — a closed union, a narrowed worker
// interface, one entry point constrained to the union — and holds the three
// authoring cases apart with nothing else in the way. But a miniature proves a
// property of itself, so the file then puts the same undeclared registration
// in front of the REAL generated union, through an overlay that writes nothing
// to the tree.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// compileScratch builds a one-file module and reports the compiler's own
// output. The module is dependency-free so the build never leaves the
// temp directory.
func compileScratch(t *testing.T, body string) (string, bool) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module scratch\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatalf("writing go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing main.go: %v", err)
	}
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	// GOWORK=off because the gate lanes run under the composed workspace
	// (build/composition/go.work), and a workspace that does not list this
	// throwaway module refuses to build it — a failure that would read as the
	// constraint refusing, for every case.
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

// scratchPreamble models the generated shape in miniature: a closed union over
// the DECLARED args types (A and B), a worker interface narrowed to Work, and
// the one registration entry point constrained to the union. C is the
// undeclared kind — a type that exists and is a perfectly good job args, but
// that the contract has never heard of.
const scratchPreamble = `package main

type jobArgs interface{ Kind() string }

type AArgs struct{}

func (AArgs) Kind() string { return "a" }

type BArgs struct{}

func (BArgs) Kind() string { return "b" }

type CArgs struct{}

func (CArgs) Kind() string { return "c" }

type declaredJobArgs interface {
	jobArgs

	AArgs | BArgs
}

type workOnly[T jobArgs] interface{ Work(*T) error }

func addDeclaredWorker[T declaredJobArgs](w workOnly[T]) {}

type aWorker struct{}

func (aWorker) Work(*AArgs) error { return nil }

type cWorker struct{}

func (cWorker) Work(*CArgs) error { return nil }

func main() {
`

func TestTheGateAcceptsADeclaredKind(t *testing.T) {
	out, ok := compileScratch(t, scratchPreamble+"\taddDeclaredWorker[AArgs](aWorker{})\n}\n")
	if !ok {
		t.Fatalf("a declared kind must register cleanly; the gate would block a legitimate author:\n%s", out)
	}
}

func TestTheGateRejectsAnUndeclaredKind(t *testing.T) {
	out, ok := compileScratch(t, scratchPreamble+"\taddDeclaredWorker[CArgs](cWorker{})\n}\n")
	if ok {
		t.Fatal("an undeclared kind compiled — the closed type set is not closed")
	}
	// The refusal has to be the constraint's, not some unrelated build error:
	// a gate that rejects for the wrong reason stops rejecting the moment the
	// unrelated error is fixed.
	if !strings.Contains(out, "does not satisfy") || !strings.Contains(out, "CArgs") {
		t.Errorf("expected a constraint-satisfaction error naming the undeclared type, got:\n%s", out)
	}
}

func TestTheGateRejectsAWorkerRegisteredUnderTheWrongKind(t *testing.T) {
	out, ok := compileScratch(t, scratchPreamble+"\taddDeclaredWorker[BArgs](aWorker{})\n}\n")
	if ok {
		t.Fatal("worker A registered under kind B compiled — the type parameter is not binding the worker")
	}
	// BArgs is declared, so the type parameter itself is fine; what must fail
	// is the worker not implementing Work for it. Naming aWorker in the error
	// is how this test stays distinct from the undeclared-kind one above.
	if !strings.Contains(out, "aWorker") {
		t.Errorf("expected the mismatch to be reported against the worker, got:\n%s", out)
	}
}

// jobKindProbeSource is one extra file for internal/compose that registers a
// single kind through the sanctioned door. The args type is the parameter: a
// DECLARED one must compile, and the undeclared one beside it must not.
//
// The unused type in the declared case is deliberate — one source, one
// difference, so the two builds differ in the type argument and in nothing
// else that could explain a refusal.
const jobKindProbeSource = `package compose

import (
	"context"

	"github.com/riverqueue/river"
)

type undeclaredProbeArgs struct{}

func (undeclaredProbeArgs) Kind() string { return "undeclared_probe" }

type jobKindProbeWorker struct{}

func (jobKindProbeWorker) Work(context.Context, *river.Job[%[1]s]) error { return nil }

func registerJobKindProbe(reg *jobRegistry) {
	addDeclaredWorker[%[1]s](reg, jobKindProbeWorker{})
}
`

// buildComposeWith compiles the real internal/compose package with one extra
// source file layered over it, and reports the compiler's own output. The
// overlay is go build's own mechanism: the file exists for the duration of the
// build and never lands in the tree, so this gate cannot leave a probe kind
// behind for another lane to register.
//
// An empty body builds the package as it stands, which is the control: a
// refusal below means nothing unless the same package compiles without the
// probe.
func buildComposeWith(t *testing.T, body string) (string, bool) {
	t.Helper()
	args := []string{"build", "./internal/compose"}
	if body != "" {
		dir := t.TempDir()
		probe := filepath.Join(dir, "probe.go")
		if err := os.WriteFile(probe, []byte(body), 0o600); err != nil {
			t.Fatalf("writing the probe source: %v", err)
		}
		target, err := filepath.Abs(filepath.Join("internal", "compose", "zz_jobkindprobe.go"))
		if err != nil {
			t.Fatalf("resolving the overlay target: %v", err)
		}
		overlay := filepath.Join(dir, "overlay.json")
		spec, err := json.Marshal(map[string]map[string]string{"Replace": {target: probe}})
		if err != nil {
			t.Fatalf("encoding the overlay: %v", err)
		}
		if err := os.WriteFile(overlay, spec, 0o600); err != nil {
			t.Fatalf("writing the overlay: %v", err)
		}
		args = []string{"build", "-overlay=" + overlay, "./internal/compose"}
	}
	cmd := exec.Command("go", args...)
	// The environment is inherited, unlike the scratch module above: this build
	// is of the real package in the real module, and the workspace the lane
	// runs under is the one it must resolve through.
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

// TestTheRealGeneratedUnionRefusesAnUndeclaredKind is the claim on the tree
// somebody actually ships. The union in internal/compose is generated from
// api/jobs.yaml and names 55 types; what the scratch module above cannot say is
// whether THAT union, reached through THAT registration helper, still refuses a
// kind the file has never heard of.
//
// The declared case runs first and is not a formality: it is what makes the
// refusal attributable. A probe that failed to compile for its own reasons — a
// stale import, a renamed helper — would otherwise read exactly like a union
// doing its job.
func TestTheRealGeneratedUnionRefusesAnUndeclaredKind(t *testing.T) {
	if out, unmodified := buildComposeWith(t, ""); !unmodified {
		t.Fatalf("internal/compose does not build as it stands, so nothing below could be attributed to the union:\n%s", out)
	}

	declared := fmt.Sprintf(jobKindProbeSource, "VoiceBuildArgs")
	if out, ok := buildComposeWith(t, declared); !ok {
		t.Fatalf("a declared kind was refused by the real union; the gate would block a legitimate author, and the author would then reach around it:\n%s", out)
	}

	undeclared := fmt.Sprintf(jobKindProbeSource, "undeclaredProbeArgs")
	out, ok := buildComposeWith(t, undeclared)
	if ok {
		t.Fatal("a kind api/jobs.yaml never declared compiled against the real generated union — the closed set is not closed, and such a kind would run at River's silent one-minute default")
	}
	// The refusal has to be the constraint's, at the registration line, naming
	// the type: a build that broke for another reason stops refusing the moment
	// that other reason is fixed.
	if !strings.Contains(out, "does not satisfy declaredJobArgs") || !strings.Contains(out, "undeclaredProbeArgs") {
		t.Errorf("expected the union to refuse undeclaredProbeArgs by name, got:\n%s", out)
	}
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The registration gate's own falsification. Every shape the tree actually
// uses, proven accepted; the shape it exists to reject, proven rejected.
// Compiled with `go build` against a scratch package rather than asserted by
// eye, because the claim is specifically about what the COMPILER does — and a
// gate that blocks a legitimate author gets weakened by the person it stopped,
// which is the hole the next undeclared job walks back in through.

import (
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

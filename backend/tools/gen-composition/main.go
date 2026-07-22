// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Command gen-composition materializes build/composition/ — the ONE
// ignored root for every installation-dependent artifact (ADR-0069 §2):
// the composed go.work(.sum), the composition Go module wiring the
// enabled extension set into the role binaries, the frontend and
// contract composition (degenerate vanilla forms until their slices
// land), and composition.json binding input digests to reproducible
// output hashes. Vanilla (an empty extensions/ tree) reproduces the
// committed composition/ stub byte-identically, so bare and composed
// builds provably wire the same thing.
//
// The path default suits `make gen` (run from backend/).
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
)

var (
	rootPath     = flag.String("root", "..", "repository root (the directory holding extensions/ and build/)")
	verify       = flag.Bool("verify", false, "regenerate in memory and compare against composition.json and the files on disk; write nothing")
	verifyInputs = flag.Bool("verify-inputs", false, "recompute input digests only and compare against composition.json; write nothing")
)

// genMode is the tool's three mutually exclusive operations; when both
// verify flags are set, the full compare wins (it subsumes the input
// probe).
type genMode int

const (
	modeGenerate     genMode = iota
	modeVerify               // regenerate in memory, compare manifest + files on disk
	modeVerifyInputs         // recompute input digests only — the fast staleness probe
)

func main() {
	flag.Parse()
	mode := modeGenerate
	switch {
	case *verify:
		mode = modeVerify
	case *verifyInputs:
		mode = modeVerifyInputs
	}
	if err := run(*rootPath, mode); err != nil {
		fmt.Fprintln(os.Stderr, "gen-composition:", err)
		os.Exit(1)
	}
}

// The composition's fixed artifact names, spelled once — they appear as
// output keys, on-disk paths, and gate messages alike.
const (
	manifestFile  = "composition.json"
	goWorkFile    = "go.work"
	goWorkSumFile = "go.work.sum"
)

// manifest is composition.json: the digest binding that replaces the
// committed-file drift gate for ignored composition output (the ADR's
// "regenerate-don't-merge" rule made checkable).
type manifest struct {
	Schema    int               `json:"schema"`
	Toolchain string            `json:"toolchain"`
	Inputs    manifestInputs    `json:"inputs"`
	Outputs   map[string]string `json:"outputs"`
}

type manifestInputs struct {
	Core          string                    `json:"core"`
	ApprovalsLock string                    `json:"approvals_lock"`
	Extensions    map[string]manifestExtRow `json:"extensions"`
}

// manifestExtRow gains the manifest.generated.json digest when the
// governance slice lands (ADR-0069 §5/§7); until then the tree digest is
// the unit's identity.
type manifestExtRow struct {
	Tree string `json:"tree"`
}

func run(root string, mode genMode) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if mode == modeGenerate {
		return generate(root)
	}
	recorded, err := readManifest(root)
	if err != nil {
		return fmt.Errorf("%w — run 'make gen'", err)
	}
	current, err := computeInputs(root)
	if err != nil {
		return err
	}
	if err := compareInputs(recorded.Inputs, current); err != nil {
		return fmt.Errorf("composition stale: %w — run 'make gen'", err)
	}
	if mode == modeVerifyInputs {
		return nil
	}
	return verifyOutputs(root, recorded)
}

// generate rebuilds build/composition/ from scratch: deterministic
// content first, then the go.work.sum materialization (the one output
// only the go command can produce), composition.json last — a crash
// leaves no manifest claiming a half-written tree is current.
func generate(root string) error {
	outRoot := filepath.Join(root, "build", "composition")
	if err := os.RemoveAll(outRoot); err != nil {
		return err
	}
	if err := stubMatchesVanilla(root); err != nil {
		return err
	}
	files, err := composedFiles(root)
	if err != nil {
		return err
	}
	for rel, content := range files {
		path := filepath.Join(outRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, content, 0o644); err != nil { // #nosec G306 -- generated build artifacts, not secrets
			return err
		}
	}
	if err := materializeWorkSum(root, outRoot); err != nil {
		return err
	}
	m, err := currentManifest(root, files)
	if err != nil {
		return err
	}
	encoded, err := encodeManifest(m)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outRoot, manifestFile), encoded, 0o644) // #nosec G306 -- generated build artifact
}

// currentManifest assembles composition.json content for the given
// deterministic outputs plus the on-disk go.work.sum.
func currentManifest(root string, files map[string][]byte) (manifest, error) {
	inputs, err := computeInputs(root)
	if err != nil {
		return manifest{}, err
	}
	outputs := make(map[string]string, len(files)+1)
	for rel, content := range files {
		outputs[rel] = digestBytes(content)
	}
	sumDigest, err := digestFileOrEmpty(filepath.Join(root, "build", "composition", goWorkSumFile))
	if err != nil {
		return manifest{}, err
	}
	outputs[goWorkSumFile] = sumDigest
	return manifest{Schema: 1, Toolchain: runtime.Version(), Inputs: inputs, Outputs: outputs}, nil
}

// materializeWorkSum lets the go command write go.work.sum for the
// composed workspace: `go list -m all` resolves the full module graph and
// records any hash beyond the members' go.sum files; a dependency-free
// composition legitimately produces no file. The binary is resolved from
// the running toolchain's GOROOT, never PATH — this generator runs in
// build pipelines, and a writable PATH entry must not choose which go
// resolves the composed graph.
func materializeWorkSum(root, outRoot string) error {
	goRoot := runtime.GOROOT()
	if goRoot == "" {
		return fmt.Errorf("cannot locate the go toolchain (empty GOROOT)")
	}
	cmd := exec.Command(filepath.Join(goRoot, "bin", "go"), "list", "-m", "all")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK="+filepath.Join(outRoot, goWorkFile))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("resolving the composed workspace (go list -m all): %v\n%s", err, out)
	}
	return nil
}

// verifyOutputs is the reproducibility gate: the deterministic outputs
// are regenerated in memory and must match both the recorded hashes and
// the files on disk, composition.json itself must be byte-identical to
// its re-encoding (a hand edit, an unknown field, or a foreign encoder
// fails here even when the semantic content agrees), and the output tree
// must hold exactly the generated files — a stale or injected extra file
// would ride into the composed build unnoticed otherwise. go.work.sum (a
// pure function of the members' go.mod/go.sum graph) is checked against
// its recorded hash.
func verifyOutputs(root string, recorded manifest) error {
	if err := stubMatchesVanilla(root); err != nil {
		return err
	}
	if recorded.Schema != 1 {
		return fmt.Errorf("%s carries schema %d, this tool writes schema 1 — run 'make gen'", manifestFile, recorded.Schema)
	}
	files, err := composedFiles(root)
	if err != nil {
		return err
	}
	current, err := currentManifest(root, files)
	if err != nil {
		return err
	}
	if current.Toolchain != recorded.Toolchain {
		return fmt.Errorf("composition built with %s, verifying with %s — run 'make gen'", recorded.Toolchain, current.Toolchain)
	}
	if err := verifyRecordedOutputs(root, current, recorded); err != nil {
		return err
	}
	if err := verifyManifestBytes(root, current); err != nil {
		return err
	}
	return verifyNoExtraFiles(root, current.Outputs)
}

// verifyRecordedOutputs holds every regenerated output against the
// recorded hash AND the bytes on disk (go.work.sum only against the
// record — regeneration does not re-run the go command).
func verifyRecordedOutputs(root string, current, recorded manifest) error {
	names := make([]string, 0, len(current.Outputs))
	for rel := range current.Outputs {
		names = append(names, rel)
	}
	sort.Strings(names)
	for _, rel := range names {
		if got, want := current.Outputs[rel], recorded.Outputs[rel]; got != want {
			return fmt.Errorf("output %s: regenerated hash %s does not reproduce recorded %s — run 'make gen'", rel, got, want)
		}
		onDisk, err := digestFileOrEmpty(filepath.Join(root, "build", "composition", filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if rel != goWorkSumFile && onDisk != current.Outputs[rel] {
			return fmt.Errorf("output %s on disk differs from its regeneration — hand-edited? run 'make gen'", rel)
		}
	}
	if len(recorded.Outputs) != len(current.Outputs) {
		return fmt.Errorf("%s records %d outputs, regeneration produced %d — run 'make gen'", manifestFile, len(recorded.Outputs), len(current.Outputs))
	}
	return nil
}

// verifyManifestBytes requires the on-disk composition.json to be
// byte-identical to the regenerated manifest's encoding — a hand edit,
// an unknown field, or a foreign encoder fails even when the semantic
// content agrees.
func verifyManifestBytes(root string, current manifest) error {
	encoded, err := encodeManifest(current)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(root, "build", "composition", manifestFile))
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, encoded) {
		return fmt.Errorf("%s on disk differs from its re-encoding — hand-edited? run 'make gen'", manifestFile)
	}
	return nil
}

// verifyNoExtraFiles walks build/composition/ and rejects anything the
// generator did not write: expected outputs + composition.json, all
// regular files.
func verifyNoExtraFiles(root string, outputs map[string]string) error {
	outRoot := filepath.Join(root, "build", "composition")
	expected := map[string]bool{manifestFile: true}
	for rel := range outputs {
		expected[rel] = true
	}
	return filepath.WalkDir(outRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(outRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !d.Type().IsRegular() {
			return fmt.Errorf("build/composition/%s: only generated regular files belong here — run 'make gen'", rel)
		}
		// go.work.sum legitimately may not exist; anything present must
		// be expected.
		if !expected[rel] {
			return fmt.Errorf("build/composition/%s was not written by this generation — stale or injected; run 'make gen'", rel)
		}
		return nil
	})
}

// stubMatchesVanilla holds the two lanes together: the committed
// composition/ stub (what a bare go build wires) must be byte-identical
// to this generator's vanilla output (what a composed vanilla build
// wires) — otherwise "vanilla output unchanged" would be an assertion,
// not a checked fact.
func stubMatchesVanilla(root string) error {
	stub, err := os.ReadFile(filepath.Join(root, "composition", "extensions_gen.go"))
	if err != nil {
		return err
	}
	if !bytes.Equal(stub, extensionsGen(nil)) {
		return fmt.Errorf("composition/extensions_gen.go differs from the generator's vanilla output — align the committed stub with tools/gen-composition")
	}
	return nil
}

func readManifest(root string) (manifest, error) {
	raw, err := os.ReadFile(filepath.Join(root, "build", "composition", manifestFile))
	if err != nil {
		return manifest{}, fmt.Errorf("no composition manifest (%v)", err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return manifest{}, fmt.Errorf("composition.json unreadable: %w", err)
	}
	return m, nil
}

func compareInputs(recorded, current manifestInputs) error {
	if recorded.Core != current.Core {
		return fmt.Errorf("core inputs changed since generation")
	}
	if recorded.ApprovalsLock != current.ApprovalsLock {
		return fmt.Errorf("extensions/approvals.lock changed since generation")
	}
	for name, row := range current.Extensions {
		rec, ok := recorded.Extensions[name]
		if !ok {
			return fmt.Errorf("extension %s added since generation", name)
		}
		if rec.Tree != row.Tree {
			return fmt.Errorf("extension %s changed since generation", name)
		}
	}
	for name := range recorded.Extensions {
		if _, ok := current.Extensions[name]; !ok {
			return fmt.Errorf("extension %s removed since generation", name)
		}
	}
	return nil
}

func encodeManifest(m manifest) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

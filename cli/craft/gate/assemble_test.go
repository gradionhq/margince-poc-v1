package gate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAssemble_gathersDiffTouchedAndSiblingFiles(t *testing.T) {
	root := t.TempDir()
	write := func(path, content string) {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("crm/crm-core/handler_person.go", "package crmcore\nfunc Person() {}\n")
	write("crm/crm-core/handler_deal.go", "package crmcore\nfunc Deal() {}\n")
	write("crm/crm-core/zz_gen.go", "// generated; must be skipped\n")
	write("AGENTS.md", "# root\n## Craftsmanship\nrules\n")

	a := &Assembler{Root: root, Git: func(_ context.Context, _ string, args ...string) (string, error) {
		switch {
		case args[0] == "diff" && contains(args, "--name-only"):
			return "crm/crm-core/handler_person.go", nil
		case args[0] == "diff":
			return "@@ a fake unified diff @@", nil
		}
		return "", nil
	}}

	in, err := a.Assemble(context.Background(), "main", "HEAD")
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if in.Diff == "" {
		t.Error("diff not captured")
	}
	if _, ok := in.TouchedFiles["crm/crm-core/handler_person.go"]; !ok {
		t.Error("touched file content not captured")
	}
	if _, ok := in.SiblingFiles["crm/crm-core/handler_deal.go"]; !ok {
		t.Error("sibling file not captured")
	}
	if _, ok := in.SiblingFiles["crm/crm-core/zz_gen.go"]; ok {
		t.Error("generated _gen.go must be skipped as a sibling")
	}
	if in.ModuleAGENTS == "" {
		t.Error("nearest AGENTS.md not captured")
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

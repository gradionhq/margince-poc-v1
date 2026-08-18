// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deployconfig

// A secret names where its value lives; it never IS the value.
//
// OPS-CFG-3: no configuration layer holds a secret's value, only a reference to
// one. This file is the mechanism — and the reason it is a mechanism rather
// than a convention is that margince.yaml is the most-read and most-pasted
// artefact an installation has. It goes in tickets, in chat, in a screenshot of
// a terminal. A password that can be written there eventually is.
//
// So a secret field REFUSES a literal, at decode time, before anything runs.
// Not "discouraged" — refused, with the field named, because the failure this
// prevents is silent and permanent: nobody notices a working installation whose
// credential is also in a support thread.
//
// Two forms, and they are not equally compliant:
//
//	${file:/run/secrets/name}   the mounted file IS the secret store — a
//	                            Kubernetes Secret, a Docker secret, a vault
//	                            projection. Fully OPS-CFG-3.
//	${env:MARGINCE_NAME}        keeps the value out of the FILE, but leaves it
//	                            in the environment layer, which OPS-CFG-3 names
//	                            alongside files and flags. A deliberate,
//	                            bounded transitional form — the universal
//	                            container pattern, and what every secret in this
//	                            repo does today — not a claim of compliance.
//
// The distinction is written down here rather than left for an operator to
// infer, because "both are supported" and "both are equivalent" are different
// sentences and only the first is true.

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/internal/platform/config"
)

// secretFileLimit bounds what a reference may pull in. A secret is a password,
// a key or a token; anything larger is a path typed wrong, and reading it into
// memory to find that out is the wrong order.
const secretFileLimit = 64 << 10

// The two reference kinds, named because they are matched, compared and
// rendered in errors — three places that must agree on the spelling.
const (
	secretFromEnv  = "env"
	secretFromFile = "file"
)

// secretRef matches the two accepted forms and captures which and what.
var secretRef = regexp.MustCompile(`^\$\{(env|file):([^}]+)\}$`)

// Secret is a reference to a value held somewhere else.
//
// The zero value means "not configured", which is distinct from "configured and
// empty": an installation that names no licence token runs unlicensed, while
// one that names an empty file has made a mistake worth reporting.
type Secret struct {
	kind string // "env" or "file"
	arg  string // the variable name, or the path
	// field is the YAML path this was decoded from, carried so an error can
	// name what an operator must go and fix rather than what the parser saw.
	field string
}

// UnmarshalYAML decodes a reference and REFUSES anything else.
//
// The refusal happens here, at decode, so a literal cannot reach a running
// process even once. A boot that fails with "this is a secret, write a
// reference" costs an operator a minute; a boot that succeeds with the password
// in the file costs them a rotation they do not know they need.
func (s *Secret) UnmarshalYAML(node *yaml.Node) error {
	var raw string
	if err := node.Decode(&raw); err != nil {
		return err
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		*s = Secret{}
		return nil
	}
	m := secretRef.FindStringSubmatch(raw)
	if m == nil {
		// The value is NOT echoed back. Reporting what was written would put
		// the very literal this refuses into the boot log, which is the leak
		// wearing a different hat.
		return fmt.Errorf("deployconfig: line %d holds a literal secret — write a reference instead: "+
			"${file:/run/secrets/name} (the mounted secret store) or ${env:MARGINCE_NAME} (the environment). "+
			"The value it currently holds is in this file, so treat it as disclosed and rotate it", node.Line)
	}
	*s = Secret{kind: m[1], arg: strings.TrimSpace(m[2])}
	if s.arg == "" {
		return fmt.Errorf("deployconfig: ${%s:} names nothing — give it %s", s.kind,
			map[string]string{secretFromEnv: "a variable name", secretFromFile: "a path"}[s.kind])
	}
	return nil
}

// Configured reports whether the deployment named a source at all.
func (s Secret) Configured() bool { return s.kind != "" }

// withField returns a copy that knows its own YAML path, so a failure names the
// key an operator edits rather than the Go field a maintainer reads.
func (s Secret) withField(path string) Secret { s.field = path; return s }

// Resolve fetches the value, or says precisely what to do about it.
//
// An unresolved REQUIRED secret must abort the boot (OPS-CFG-2's fail-fast
// rule), and the caller decides required-ness — this reports the empty string
// and no error for a reference that is simply unset, because "the operator has
// not configured optional thing X" is not a failure at this layer.
func (s Secret) Resolve(lookup config.Lookup) (string, error) {
	switch s.kind {
	case "":
		return "", nil
	case secretFromEnv:
		return strings.TrimSpace(lookup(s.arg)), nil
	case secretFromFile:
		return s.readFile()
	default:
		return "", fmt.Errorf("deployconfig: %s has unknown secret kind %q", s.name(), s.kind)
	}
}

// readFile reads a mounted secret, refusing the two failures that otherwise
// present as "unlicensed" or "no password" rather than as mistakes.
func (s Secret) readFile() (string, error) {
	info, err := os.Stat(s.arg)
	if err != nil {
		return "", fmt.Errorf("deployconfig: %s points at %s, which cannot be read: %w", s.name(), s.arg, err)
	}
	if info.Size() > secretFileLimit {
		return "", fmt.Errorf("deployconfig: %s points at %s, which is %d bytes — a secret is not (limit %d). "+
			"Check the path names the secret and not something else", s.name(), s.arg, info.Size(), secretFileLimit)
	}
	raw, err := os.ReadFile(s.arg) // #nosec G304 -- the operator's own secret path; reading it is this function's purpose
	if err != nil {
		return "", fmt.Errorf("deployconfig: %s points at %s, which cannot be read: %w", s.name(), s.arg, err)
	}
	// Trimmed both ends, because a secret carries no meaningful surrounding
	// whitespace and every editor and secret store terminates a file its own
	// way. A trailing newline must not become part of a password.
	return strings.TrimSpace(string(raw)), nil
}

// Missing renders the fail-fast message for a required secret that resolved to
// nothing: what is missing, and the two ways to supply it.
//
// It names the SOURCE the deployment already chose rather than lecturing about
// both forms, because an operator who wrote ${file:...} wants to know the file
// is empty, not to be told ${env:} also exists.
func (s Secret) Missing() error {
	switch s.kind {
	case "":
		return fmt.Errorf("deployconfig: %s is required and names no source — set it to "+
			"${file:/run/secrets/name} or ${env:MARGINCE_NAME}", s.name())
	case secretFromEnv:
		return fmt.Errorf("deployconfig: %s is required; %s is unset or empty in this process's environment", s.name(), s.arg)
	default:
		return fmt.Errorf("deployconfig: %s is required; %s holds nothing", s.name(), s.arg)
	}
}

// name is the YAML path when one was recorded, and a bare description when the
// value was built outside decoding.
func (s Secret) name() string {
	if s.field == "" {
		return "a secret"
	}
	return s.field
}

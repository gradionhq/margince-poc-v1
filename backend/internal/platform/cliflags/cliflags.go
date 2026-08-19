// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package cliflags registers command-line flags whose value may come from the
// environment, and applies the environment only where the flag was not given.
//
// It exists because `flag` echoes a non-empty default in its usage output as
// `(default "…")`. Wiring os.Getenv straight into a flag's default therefore puts
// whatever the environment supplied into the usage text, and every binary here
// prints that text on a mistyped flag or `-h`. The values in question are DSNs,
// signing keys, OAuth client secrets and bearer tokens; on CI that stderr is a
// public build log, and in a container it is the pod's log stream.
//
// Registering the LITERAL default and resolving the environment after Parse keeps
// the value out of the usage text while leaving the precedence unchanged: an
// explicit flag still wins over the environment, which still wins over the
// literal.
//
// The obligation is derived, not maintained: each binary asserts that its own
// usage output contains no value from any MARGINCE_* variable it reads, so a flag
// added later cannot quietly reintroduce the leak by being left off a list.
package cliflags

import (
	"flag"

	"github.com/gradionhq/margince/backend/internal/platform/config"
)

// Env collects the flag-to-environment bindings of one FlagSet.
type Env struct {
	bindings []binding
}

type binding struct {
	name   string
	env    string
	target *string
}

// String registers name on fs with its literal default — empty when only the
// environment supplies a value — and records env as that flag's source. The
// literal is safe to echo; the environment's value is not, which is the whole
// reason the two are separated here.
func (e *Env) String(fs *flag.FlagSet, target *string, name, env, literal, usage string) {
	fs.StringVar(target, name, literal, usage)
	e.bindings = append(e.bindings, binding{name: name, env: env, target: target})
}

// Apply fills every registered flag the caller did not pass from its environment
// variable. Call it immediately after fs.Parse.
//
// An empty environment value is treated as unset, matching .env.example's
// promise that "an empty value is treated as unset, so a blank line is safe" —
// otherwise a blank line in a sourced env file would erase a literal default.
func (e *Env) Apply(fs *flag.FlagSet, getenv func(string) string) {
	given := make(map[string]bool, fs.NFlag())
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })

	for _, b := range e.bindings {
		if given[b.name] {
			continue
		}
		if v := getenv(b.env); v != "" {
			*b.target = v
		}
	}
}

// EnvKeys returns the environment variables this Env reads, so a test can seed
// every one of them without maintaining a second copy of the list.
func (e *Env) EnvKeys() []string {
	keys := make([]string, 0, len(e.bindings))
	for _, b := range e.bindings {
		keys = append(keys, b.env)
	}
	return keys
}

// Items describes the flags registered on fs as configuration items, so a role
// that already declares its surface once — as flags with defaults and usage —
// does not declare it a second time as data.
//
// Name, default and doc are READ BACK from the FlagSet rather than restated:
// the usage text an operator sees on `-h` and the doc a generated template
// carries are then the same sentence by construction, and cannot drift.
//
// public names the bindings whose values are safe to echo. EVERYTHING ELSE IS
// TREATED AS A SECRET, and that direction is the point: a map miss must not
// mean "publish it". A flag added later and classified by nobody is withheld
// until somebody decides it is safe — the failure an operator can recover from.
// The other direction puts a bearer token in a build log and cannot be undone.
//
// The caller supplies it because only the role knows which of its own flags
// carry a DSN, a signing key or a bearer token; the mechanism here cannot tell
// a path from a password.
func (e *Env) Items(fs *flag.FlagSet, role string, public map[string]bool) []config.Item {
	registered := make(map[string]*flag.Flag, len(e.bindings))
	fs.VisitAll(func(f *flag.Flag) { registered[f.Name] = f })

	items := make([]config.Item, 0, len(e.bindings))
	for _, b := range e.bindings {
		f := registered[b.name]
		items = append(items, config.Item{
			Name:     b.env,
			FlagName: b.name,
			Kind:     config.KindString,
			Default:  f.DefValue,
			Secret:   !public[b.env],
			Roles:    []string{role},
			Doc:      f.Usage,
		})
	}
	return items
}

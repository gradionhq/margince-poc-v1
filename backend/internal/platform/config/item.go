// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package config

// What an installation can configure, declared as data rather than discovered
// by grep.
//
// The names alone were already enumerable — envcontract_test.go finds every
// MARGINCE_* literal in the tree and demands a row in configuration.md. What
// that cannot see is everything ELSE a generator needs: whether a value is a
// duration or a count, what it defaults to, whether it is a secret that must
// never be echoed, which process role reads it, and what it is for. Those live
// in prose today, which is why the prose and the code drift.
//
// So each package declares its own items beside the code that reads them, and
// each cmd/<role> assembles the ones it wires. A central list of sixty is the
// "list to maintain" this repo's second review rule warns about; sixty
// declarations next to sixty readers is not.

import (
	"fmt"
	"sort"
)

// Kind is how a raw string becomes a value, and therefore how a schema
// describes it and what a bad value fails on.
type Kind string

const (
	// KindString is any free text — a DSN, a path, a base URL.
	KindString Kind = "string"
	// KindDuration parses with time.ParseDuration ("15m", "24h").
	KindDuration Kind = "duration"
	// KindInt is a base-10 integer.
	KindInt Kind = "int"
	// KindBool is the literal "true"; anything else is false. Spelled out
	// because the tree already reads it that way and a schema must say so.
	KindBool Kind = "bool"
)

// The process roles an item can belong to. Named here, beside Item.Roles,
// because a role that is spelled as a literal in each declaring package is a
// role that can be misspelled in one of them — and a template grouped by role
// would then quietly grow a fourth.
const (
	RoleAPI    = "api"
	RoleWorker = "worker"
)

// Item is one thing an installation can configure.
type Item struct {
	// Name is the environment variable, e.g. MARGINCE_REDIS.
	Name string
	// FlagName is the command-line flag that overrides it, without the dash, or
	// "" when the value arrives only through the environment. Both belong in a
	// reference an operator reads: OPS-CFG-1 puts flags above the environment,
	// so a template that named only one half would describe the weaker source.
	FlagName string
	// Kind decides how the raw string is read.
	Kind Kind
	// Default is what the product uses when nothing supplies a value. It is
	// the LITERAL default only — never a value sampled from the environment,
	// which is the leak platform/cliflags exists to prevent.
	Default string
	// Secret marks a credential. A secret's VALUE never appears in usage text,
	// a log line, a generated template or an error — only its name does.
	Secret bool
	// Roles names the process roles that read it ("api", "worker", "migrate"),
	// so a template can say where a value matters instead of implying all of
	// them everywhere.
	Roles []string
	// Doc is one sentence an operator can act on: what it does, not what type
	// it is, which Kind already says.
	Doc string
}

// Registry is the assembled surface of one process role.
type Registry struct {
	items map[string]Item
}

// NewRegistry assembles items and refuses a set that cannot be honest.
//
// The refusals happen here rather than at the point of use, because each
// produces a configuration surface that describes something the product does
// not do — or, in the secret case, publishes something it must not. A duplicate
// name means two packages claim one variable, and their defaults and docs must
// then disagree.
func NewRegistry(groups ...[]Item) (*Registry, error) {
	r := &Registry{items: make(map[string]Item)}
	for _, group := range groups {
		for _, item := range group {
			if err := r.add(item); err != nil {
				return nil, err
			}
		}
	}
	return r, nil
}

func (r *Registry) add(item Item) error {
	switch {
	case item.Name == "":
		return fmt.Errorf("config: an item with no name (doc %q)", item.Doc)
	case item.Kind == "":
		return fmt.Errorf("config: %s declares no kind", item.Name)
	case item.Secret && item.Default != "":
		return fmt.Errorf("config: %s is a secret with a default — a default is echoed wherever the surface is described, so this would publish a credential by construction", item.Name)
	case item.Doc == "":
		return fmt.Errorf("config: %s has no doc — an item nobody can act on is one nobody can configure", item.Name)
	}
	if existing, clash := r.items[item.Name]; clash {
		return fmt.Errorf("config: %s is declared twice (%q and %q) — one variable has one meaning",
			item.Name, existing.Doc, item.Doc)
	}
	r.items[item.Name] = item
	return nil
}

// Items returns every declared item, ordered by name so a generated artefact
// does not churn between runs.
func (r *Registry) Items() []Item {
	out := make([]Item, 0, len(r.items))
	for _, item := range r.items {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

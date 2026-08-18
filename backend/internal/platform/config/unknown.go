// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package config

// A misspelled variable currently does nothing at all.
//
// MARGINCE_REDDIS=… is not an error, not a warning, and not a different
// behaviour — the process reads MARGINCE_REDIS, finds nothing, and takes the
// default. The operator sees a working installation ignoring the setting they
// just made, with no thread to pull. That is the failure this reports.
//
// It is a WARNING and never a refusal. A deployment legitimately carries
// variables this process does not read — another role's, the entrypoint's, the
// build's — so refusing to boot on one would turn a helpful observation into an
// outage. The observation is the whole value.

import (
	"log/slog"
	"os"
	"sort"
	"strings"
)

// namespace is the prefix that makes a variable ours to have an opinion about.
// A deployment's environment holds PATH, HOME and whatever else the platform
// puts there; none of it is a typo of anything we declare.
const namespace = "MARGINCE_"

// consumedByTheContainerNotTheProcess names variables whose consumer is the
// image itself rather than any Go role, so reporting them would be noise on
// every boot of every installation.
//
// Each says WHO consumes it, so a name that stops being consumed can be found
// and removed rather than accumulating:
//
//   - the container entrypoint writes the bootstrap credential from
//     MARGINCE_ADMIN_PASSWORD before the api starts (ADR-0061 §2);
//   - the build stamps MARGINCE_BUILD_REVISION and MARGINCE_COMPOSITION_FRONTEND
//     into the image, and they outlive the build inside it.
//
// MARGINCE_ADMIN_PASSWORD_FILE is deliberately NOT here. The entrypoint records
// that it was retired and its path hardcoded, so an operator still setting it is
// being ignored — which is exactly what this report exists to say out loud.
//
// MARGINCE_OWNER_DSN is likewise not here. It is the migrate role's, and the
// report now says only "this role does not read it", which is true of both the
// api and the worker — and worth saying, because that DSN is the superuser
// credential FORCE row-level security does not bind.
//
// The names are assembled from the prefix rather than written whole, because
// the tree-wide documentation gate reads every quoted MARGINCE_* literal as a
// variable this code READS and demands a row for it. These are the opposite:
// names this code deliberately does not read.
var consumedByTheContainerNotTheProcess = map[string]bool{
	namespace + "ADMIN_PASSWORD":       true,
	namespace + "BUILD_REVISION":       true,
	namespace + "COMPOSITION_FRONTEND": true,
}

// Undeclared reports the namespaced variables present in environ that no item
// declares and nothing else is known to consume — the typo report.
//
// environ is the "NAME=value" form os.Environ returns. Only the NAME half is
// ever read or returned: a misspelled secret is still a secret, and a warning
// that echoed it would leak the value while complaining about the name.
func (r *Registry) Undeclared(environ []string) []string {
	var unknown []string
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		switch {
		case !ok, !strings.HasPrefix(name, namespace):
			continue
		// Set to nothing IS unset: .env.template ships its optional lines as
		// bare NAME= placeholders, both entrypoints export them with `set -a`,
		// and cliflags.Apply already treats an empty value as absent. There is
		// no value being ignored, so there is nothing to report.
		case value == "":
			continue
		// The suite's own plumbing — where its database is, whether to record a
		// benchmark. Not installation configuration, and present in every
		// developer's shell.
		case strings.HasPrefix(name, namespace+"TEST_"), strings.HasPrefix(name, namespace+"BENCH_"):
			continue
		case consumedByTheContainerNotTheProcess[name]:
			continue
		}
		if _, declared := r.items[name]; !declared {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// Environ is the process environment in "NAME=value" form — the only os.Environ
// in the product, here for the same reason FromOS is.
func Environ() []string { return os.Environ() }

// WarnUndeclared writes the report, or nothing when there is nothing to say.
// It takes the names Undeclared produced rather than the registry, so a role
// can compute them where its surface is assembled and report them where its
// logger exists.
//
// One implementation rather than one per role, because the sentence is the
// load-bearing part: it claims only that THIS role does not read the variable,
// which is all a single role's registry can know. Two copies would be two
// chances to word it as "no such variable" — the claim that talks an operator
// into deleting configuration a sibling role depends on.
//
// Names travel, values never do: a misspelled secret is still a secret, and a
// warning that echoed one would leak the value while complaining about the name.
func WarnUndeclared(logger *slog.Logger, unknown []string) {
	if len(unknown) == 0 {
		return
	}
	logger.Warn("set but not read by this process — check the spelling, or ignore this if another role reads it",
		"variables", unknown)
}

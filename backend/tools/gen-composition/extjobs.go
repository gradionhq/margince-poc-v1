// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The jobs.yaml half of the contract → declaration direction, beside
// extverbs.go and on exactly the same terms: every scheduled job an extension
// publishes is read back OUT of build/composition/api/jobs.yaml — the merged
// document — rather than out of the fragment, so what the boot registers and
// what the file says cannot disagree. The result goes into extensions_gen.go as
// Go LITERALS, because the two boot refusals in compose/extjobs.go read Tier
// and RequestedScope and must keep refusing inside a bare role binary.
//
// A scheduled job is TWO kinds. gen-jobs' validateCadence forbids a cadence on
// a workspace-role kind, so a unit declares a cadenced DISPATCHER and the
// workspace CHILD it fans out to, and this reader pairs them by name:
// ext_<ns>_<job> and ext_<ns>_<job>_ws.

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// jobsContractBase is the merged contract extension job kinds live in.
const jobsContractBase = "jobs.yaml"

// extJobKind is one `kinds.<name>` entry an extension fragment writes.
//
// It is a NARROWER document than gen-jobs' kindDef, and every omission is
// deliberate rather than unimplemented:
//
//   - go_type names a struct in package compose. An extension has none — one
//     shared args pair serves every composed kind — so declaring one would name
//     a Go type nothing could resolve.
//   - fans_out_to / fan_out_unit are DERIVED here: the pair is ext_<ns>_<job>
//     and ext_<ns>_<job>_ws, and a second spelling of an edge this reader
//     computes could only disagree with it.
//   - registration gates a kind on a JobRunnerConfig field. That struct is
//     core's, an extension cannot name a field of it, and a job whose
//     registration depended on core's model lanes would be a unit reaching into
//     the installation's wiring.
//   - opts_owner is fixed by the seam: a dispatcher's opts are its args' and a
//     child's are the fan-out helper's, in compose/extjobs.go.
//
// `job`, `tier` and `scope` are the extension spellings, and they carry what
// jobs.yaml has no core vocabulary for: which Jobs entry the kind joins to, and
// the governance the tick REQUESTS. Strictly decoded (KnownFields), for the
// reason every author-written document in this tier is: a typo'd `scope` on a
// unit that meant `send` would read as an omission and be refused, but a typo'd
// key that fell through would publish a request nobody reviewed.
type extJobKind struct {
	Job         string `yaml:"job"`
	Role        string `yaml:"role"`
	Queue       string `yaml:"queue"`
	Timeout     string `yaml:"timeout"`
	Cadence     string `yaml:"cadence"`
	MaxAttempts int    `yaml:"max_attempts"`
	Tier        string `yaml:"tier"`
	Scope       string `yaml:"scope"`
}

const (
	extRoleDispatcher = "dispatcher"
	extRoleWorkspace  = "workspace"
)

// extensionJobs reads every enabled unit's scheduled jobs out of the merged
// jobs contract. Result order is (unit, job), deterministic and independent of
// map iteration, because it is emitted into generated Go.
func extensionJobs(units []extensionUnit, contracts map[string][]byte) ([]extension.JobDeclaration, error) {
	raw, ok := contracts[jobsContractBase]
	if !ok {
		return nil, fmt.Errorf("no composed contract for %s", jobsContractBase)
	}
	var doc struct {
		Queues yaml.Node `yaml:"queues"`
		Kinds  yaml.Node `yaml:"kinds"`
	}
	// Not KnownFields: the core contract carries blocks this reader has no
	// business knowing about. The strict read is on the fragment's own kind
	// entries below, which are what a unit author writes.
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s/%s: %w", apiLayer, jobsContractBase, err)
	}
	owners := make(map[string]string, len(units))
	for _, u := range units {
		ns, err := extension.Name(u.Name).Namespace()
		if err != nil {
			return nil, fmt.Errorf("extensions/%s: %w", u.Name, err)
		}
		owners[ns] = u.Name
	}
	entries, err := extensionKindEntries(&doc.Kinds, owners)
	if err != nil {
		return nil, fmt.Errorf("%s/%s: %w", apiLayer, jobsContractBase, err)
	}
	decls, err := pairExtensionKinds(entries, mappingKeys(&doc.Queues))
	if err != nil {
		return nil, fmt.Errorf("%s/%s: %w", apiLayer, jobsContractBase, err)
	}
	sort.Slice(decls, func(i, j int) bool {
		if decls[i].Unit != decls[j].Unit {
			return decls[i].Unit < decls[j].Unit
		}
		return decls[i].Job < decls[j].Job
	})
	return decls, nil
}

// namedExtKind is one read entry, kept with its kind string so the pairing
// below can report which line is at fault.
type namedExtKind struct {
	kind string
	unit string
	def  extJobKind
}

// extensionKindEntries reads every kinds.<name> entry in the extension
// namespace. A kind under ext_ that no enabled unit owns is an error rather
// than a skip, for the reason an unowned extension ROUTE is: it can only mean
// the base contract declared one, and a core kind in the extension namespace
// would be worked by nothing and attributed to nobody.
func extensionKindEntries(kinds *yaml.Node, owners map[string]string) ([]namedExtKind, error) {
	if kinds.IsZero() || kinds.Kind != yaml.MappingNode {
		return nil, nil
	}
	var out []namedExtKind
	for i := 0; i+1 < len(kinds.Content); i += 2 {
		kind := kinds.Content[i].Value
		if !strings.HasPrefix(kind, extension.NamespacePrefix) {
			continue
		}
		unit, ok := owners[namespaceOf(kind, owners)]
		if !ok {
			return nil, fmt.Errorf("kind %s is in the extension namespace but no enabled unit owns it", kind)
		}
		def, err := decodeStrict[extJobKind](kinds.Content[i+1])
		if err != nil {
			return nil, fmt.Errorf("kind %s: %w", kind, err)
		}
		out = append(out, namedExtKind{kind: kind, unit: unit, def: def})
	}
	return out, nil
}

// namespaceOf finds the ext_<ns> an owned kind sits in. A unit name may contain
// a hyphen, which becomes an underscore in the namespace, so the boundary
// cannot be found by scanning for the next underscore — the owned namespaces
// are matched instead, longest first, so `ext_a_b` never claims a kind of
// `ext_a_b_c` when both units exist.
func namespaceOf(kind string, owners map[string]string) string {
	namespaces := make([]string, 0, len(owners))
	for ns := range owners {
		namespaces = append(namespaces, ns)
	}
	sort.Slice(namespaces, func(i, j int) bool { return len(namespaces[i]) > len(namespaces[j]) })
	for _, ns := range namespaces {
		if strings.HasPrefix(kind, ns+"_") {
			return ns
		}
	}
	return ""
}

// pairExtensionKinds joins each dispatcher to the workspace child it implies,
// and refuses every half-declared pair. A dispatcher with no child would tick
// and enqueue rows of a kind nothing declares; a child with no dispatcher would
// be a worker no clock ever reaches.
func pairExtensionKinds(entries []namedExtKind, queues []string) ([]extension.JobDeclaration, error) {
	byKind := make(map[string]namedExtKind, len(entries))
	for _, e := range entries {
		byKind[e.kind] = e
	}
	var decls []extension.JobDeclaration
	for _, e := range entries {
		if e.def.Role != extRoleDispatcher {
			continue
		}
		childKind := e.kind + extension.JobKindSuffix
		child, ok := byKind[childKind]
		if !ok {
			return nil, fmt.Errorf("kind %s is a dispatcher but nothing declares %s — a scheduled job is a cadenced dispatcher AND the workspace child it fans out to, because a cadence on a workspace-role kind is refused", e.kind, childKind)
		}
		if child.def.Role != extRoleWorkspace {
			return nil, fmt.Errorf("kind %s declares role %q — the child of a dispatcher carries one tenant's pass and its role is %q", childKind, child.def.Role, extRoleWorkspace)
		}
		d, err := declarationFrom(e, child, queues)
		if err != nil {
			return nil, err
		}
		decls = append(decls, d)
	}
	// Every child must have been claimed by the loop above; one that was not is
	// a worker nothing schedules.
	for _, e := range entries {
		if e.def.Role != extRoleWorkspace {
			continue
		}
		if _, ok := byKind[strings.TrimSuffix(e.kind, extension.JobKindSuffix)]; !ok || !strings.HasSuffix(e.kind, extension.JobKindSuffix) {
			return nil, fmt.Errorf("kind %s is a workspace kind that no dispatcher fans out to — name it <dispatcher>%s and declare the dispatcher, or delete it", e.kind, extension.JobKindSuffix)
		}
	}
	return decls, nil
}

// declarationFrom folds one dispatcher/child pair into the value the composed
// program carries, and holds each half to declaring only what it governs.
func declarationFrom(dispatcher, child namedExtKind, queues []string) (extension.JobDeclaration, error) {
	fail := func(format string, args ...any) (extension.JobDeclaration, error) {
		return extension.JobDeclaration{}, fmt.Errorf("kind %s: "+format, append([]any{dispatcher.kind}, args...)...)
	}
	if dispatcher.def.Job != child.def.Job {
		return fail("declares job %q but %s declares %q — the pair is one job", dispatcher.def.Job, child.kind, child.def.Job)
	}
	// The kind is the namespace, the job name, and nothing between: a
	// dispatcher whose kind and `job:` disagree would register behavior under a
	// name the contract does not spell.
	ns, err := extension.Name(dispatcher.unit).Namespace()
	if err != nil {
		return extension.JobDeclaration{}, err
	}
	if want := ns + "_" + dispatcher.def.Job; want != dispatcher.kind {
		return fail("declares job %q, whose kind is %s — a job's kind is its unit's namespace and its name", dispatcher.def.Job, want)
	}
	cadence, err := parseJobDuration("cadence", dispatcher.def.Cadence)
	if err != nil {
		return fail("%w", err)
	}
	dispatcherTimeout, err := parseJobDuration("timeout", dispatcher.def.Timeout)
	if err != nil {
		return fail("%w", err)
	}
	childTimeout, err := parseJobDuration("timeout", child.def.Timeout)
	if err != nil {
		return extension.JobDeclaration{}, fmt.Errorf("kind %s: %w", child.kind, err)
	}
	if child.def.Cadence != "" {
		return extension.JobDeclaration{}, fmt.Errorf("kind %s declares a cadence — a workspace kind is enqueued by its dispatcher, never ticked", child.kind)
	}
	if dispatcher.def.MaxAttempts != 0 {
		return fail("declares max_attempts — the attempt cap is the CHILD's, and a dispatcher's retry is its own next tick")
	}
	// One queue for the pair. Two would let a dispatcher tick on a pool whose
	// bound has nothing to do with the one its children compete for, which is
	// the kind of split that only shows up as a starved queue in production.
	if dispatcher.def.Queue != child.def.Queue {
		return fail("lands on queue %q but %s lands on %q — a job's two kinds share one pool", dispatcher.def.Queue, child.kind, child.def.Queue)
	}
	// The queues DECISION, made checkable. `queues` is not a container a
	// fragment may extend (see contractmerge.go's ownershipContainers), so the
	// only queues in the merged document are the ones the installation already
	// declared — an extension job rides a pool that exists rather than
	// allocating a share of the process's worker budget from a directory.
	if !slices.Contains(queues, dispatcher.def.Queue) {
		return fail("lands on queue %q, which the contract does not declare — an extension job rides one of the installation's own pools (%s); `queues` is not a container a fragment may extend, so a unit cannot allocate one",
			dispatcher.def.Queue, strings.Join(queues, ", "))
	}
	d := extension.JobDeclaration{
		Unit:              extension.Name(dispatcher.unit),
		Job:               dispatcher.def.Job,
		Queue:             dispatcher.def.Queue,
		Cadence:           cadence,
		DispatcherTimeout: dispatcherTimeout,
		Timeout:           childTimeout,
		MaxAttempts:       child.def.MaxAttempts,
		Tier:              extension.Tier(dispatcher.def.Tier),
		RequestedScope:    extension.Scope(dispatcher.def.Scope),
	}
	// The SAME Validate the boot runs, so a fragment this generator accepts can
	// never be one the composed process then refuses to register.
	if err := d.Validate(); err != nil {
		return extension.JobDeclaration{}, err
	}
	return d, nil
}

// parseJobDuration reads one duration field, refusing the absence that would
// otherwise reach River as its silent one-minute default.
func parseJobDuration(field, value string) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return 0, fmt.Errorf("declares no %s", field)
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s %q: %w", field, value, err)
	}
	return d, nil
}

// mappingKeys lists a mapping node's keys in document order, or nothing for a
// node that is absent or is not a mapping.
func mappingKeys(node *yaml.Node) []string {
	if node.IsZero() || node.Kind != yaml.MappingNode {
		return nil
	}
	keys := make([]string, 0, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		keys = append(keys, node.Content[i].Value)
	}
	return keys
}

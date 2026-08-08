// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The JOB half of the declaration read, split out of astreader.go beside the
// tool half, and on the same seam: a Jobs entry is {Name, Handle} and
// contributes no MECHANICS to the manifest. What this file does is decide
// whether a unit's Go behavior and its contract-declared job kinds agree, and
// turn the contract-declared side into the manifest's risk-tier entries.

import (
	"fmt"
	"go/ast"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// opJobTick is the operation a scheduled extension job performs. It is not
// agent.tool.invoke: a tick is not a call, nobody made it, and an operator
// resolution recorded against one must never read as a resolution of the other.
const opJobTick = "extension.job.tick"

// kindExtensionJob is the CAPABILITY KIND the descriptor records for a
// scheduled job — the second kind kindAgentTool's comment anticipated.
const kindExtensionJob = "extension_job"

// readJobs reads the unit's Jobs slice: the job name, and whether behavior is
// declared for it. Nothing here reaches the manifest's mechanics — cadence,
// wall clocks, queue and attempt cap are declared in the unit's api/jobs.yaml
// fragment and derived from the merged contract (extjobs.go).
func (r *unitReader) readJobs(expr ast.Expr, file *ast.File) ([]declaredTool, error) {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, r.errAt(expr, "Jobs must be a slice literal")
	}
	ext := importAlias(file, extensionPkgPath)
	jobs := make([]declaredTool, 0, len(lit.Elts))
	seen := map[string]bool{}
	for _, elt := range lit.Elts {
		j, err := r.readJob(elt, ext)
		if err != nil {
			return nil, err
		}
		if seen[j.name] {
			return nil, r.errAt(elt, "job %s declared twice", j.name)
		}
		seen[j.name] = true
		jobs = append(jobs, j)
	}
	return jobs, nil
}

// readJob reads one entry. It reuses declaredTool because the two declarations
// carry the SAME three facts a static reader can get at — a name, whether a
// handler is served, and the position to report a mismatch at — and a second
// struct holding those three would be a type distinction with no field
// difference. The join below is what tells them apart.
func (r *unitReader) readJob(elt ast.Expr, ext string) (declaredTool, error) {
	lit, ok := elt.(*ast.CompositeLit)
	if !ok || (lit.Type != nil && !isSelector(lit.Type, ext, "Job")) {
		return declaredTool{}, r.errAt(elt, "a Jobs entry must be an extension.Job literal")
	}
	var d declaredTool
	d.at = lit
	for _, e := range lit.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			return declaredTool{}, r.errAt(e, "Job fields must be keyed")
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			return declaredTool{}, r.errAt(kv.Key, "Job fields must be keyed by name")
		}
		var err error
		switch key.Name {
		case "Name":
			d.name, err = r.stringLit(kv.Value, "Job.Name")
		case "Handle":
			// Same rule and same reasoning as Tool.Handle: a declared nil is
			// how the seam spells "declare it, run nothing", and any non-nil
			// spelling other than a bare identifier is refused because the AST
			// cannot tell an inert package-level function from a method value
			// that closes over a receiver.
			d.served, err = r.readJobHandle(kv.Value, ext)
		default:
			// Fail closed, and this arm is what keeps the split honest: a unit
			// declaring Cadence, Timeout or Queue in Go is told, at that line,
			// that the field belongs to its jobs.yaml fragment — rather than
			// having it silently ignored while the contract's value governs.
			err = r.errAt(kv, "Job field %s is not derivable by this generator — a Job declares {Name, Handle}; the cadence, wall clocks, queue, attempt cap, tier and scope are declared in the unit's %s/jobs.yaml fragment and read from the merged contract", key.Name, apiLayer)
		}
		if err != nil {
			return declaredTool{}, err
		}
	}
	if err := (extension.Job{Name: d.name}).Validate(); err != nil {
		return declaredTool{}, r.errPos(lit, "%v", err)
	}
	return d, nil
}

// readJobHandle reports whether a Jobs entry runs a handler. It is
// readHandle's twin rather than readHandle itself because the one nil spelling
// that names a published type names a DIFFERENT one — extension.JobHandler,
// not extension.ToolHandler — and accepting either on both fields would let a
// Tools entry spell its inertness with the job type and vice versa.
func (r *unitReader) readJobHandle(expr ast.Expr, ext string) (bool, error) {
	if isStaticallyNilJob(expr, ext) {
		return false, nil
	}
	if _, ok := expr.(*ast.Ident); ok {
		return true, nil
	}
	return false, r.errAt(expr, "Job.Handle must be a plain identifier naming the handler function, or one of the documented inert nil spellings (nil, extension.JobHandler(nil), (nil))")
}

// isStaticallyNilJob is isStaticallyNil over extension.JobHandler. The callee
// check on the CallExpr arm is load-bearing for the reason stated there: a
// syntactic conversion and an ordinary one-argument call parse identically, so
// accepting any one-argument nil call would read `mustDial(nil)` as inert.
func isStaticallyNilJob(expr ast.Expr, ext string) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name == "nil"
	case *ast.CallExpr:
		return len(e.Args) == 1 && isSelector(e.Fun, ext, "JobHandler") && isStaticallyNilJob(e.Args[0], ext)
	case *ast.ParenExpr:
		return isStaticallyNilJob(e.X, ext)
	}
	return false
}

// joinJobsToContract refuses behavior for a job no kind declares.
//
// One direction only, exactly as joinToolsToContract: a declared job pair with
// no Go behavior is a contract-only request the manifest records and nothing
// ticks, which is a legitimate shape. Behavior with no declaration is not — the
// dispatcher would have no cadence, the child no wall clock and no attempt cap,
// and the seam would be registering a kind the contract never published.
func (r *unitReader) joinJobsToContract(jobs []declaredTool, decls []extension.JobDeclaration) error {
	declared := make(map[string]bool, len(decls))
	for _, d := range decls {
		declared[d.Job] = true
	}
	for _, j := range jobs {
		if declared[j.name] {
			continue
		}
		return r.errPos(j.at, "job %q has behavior here but no kind in this unit's %s/jobs.yaml fragment declares it — declare the dispatcher and its _ws child in the contract, or delete the entry", j.name, apiLayer)
	}
	return nil
}

// jobRequests turns the unit's contract-declared jobs into manifest risk-tier
// entries. A job requests one scope and one tier, the same two things an
// operator resolves for a tool — but under its own capability kind and its own
// operation, so a resolution recorded for a tool can never carry to a job that
// happens to share every other field.
//
// Route and Method are empty and that is the honest answer rather than a gap: a
// job publishes no HTTP surface. OperationID carries the DISPATCHER KIND, which
// is the job's published identity — the string River persists, the string the
// declared-kind gauge names, and the string an operator would grep for.
func jobRequests(decls []extension.JobDeclaration) ([]riskTierRequest, error) {
	out := make([]riskTierRequest, 0, len(decls))
	for _, d := range decls {
		c := riskTierRequest{
			ID:          "job/" + d.Job,
			Unit:        string(d.Unit),
			Kind:        kindExtensionJob,
			Contract:    jobsContractBase,
			Operation:   opJobTick,
			OperationID: d.DispatcherKind(),
			Scopes:      []string{string(d.RequestedScope)},
			Tier:        string(d.Tier),
			// The declaration IS the fragment here — there is no prose or
			// schema beside it the way a tool operation has — so the mechanics
			// a resolution should not silently outlive are hashed directly.
			FragmentHash: jobDeclarationHash(d),
		}
		digest, err := descriptorDigest(c)
		if err != nil {
			return nil, err
		}
		c.Digest = digest
		out = append(out, c)
	}
	return out, nil
}

// jobDeclarationHash covers what the descriptor's own fields do not: the
// cadence, the two wall clocks, the queue and the attempt cap. None of them
// grants authority, and all of them change what the granted authority is spent
// on and how often — a job an operator resolved at six-hourly should not
// silently become one running every minute.
func jobDeclarationHash(d extension.JobDeclaration) string {
	return digestBytes(fmt.Appendf(nil, "%s\x00%s\x00%d\x00%d\x00%d\x00%d",
		d.Queue, d.Job, int64(d.Cadence), int64(d.DispatcherTimeout), int64(d.Timeout), d.MaxAttempts))
}

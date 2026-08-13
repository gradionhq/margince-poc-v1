// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The permission fixtures every suite acts through. They live beside the
// environment rather than inside it because they are the one part of the
// harness that has to mirror something outside the test tree: identity's
// seeded role documents. A fixture that drifts from that seed still passes,
// while proving nothing about production.

import (
	"maps"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The RBAC object keys the permission fixtures below repeat often enough to
// name. They are identity's policy vocabulary only — deliberately NOT reused for
// the activity_link.entity_type values seed.go writes, which spell some of the
// same words today from a different namespace and are free to diverge.
// The seeded role keys these fixtures act as. Named because the fixtures
// below and the ad-hoc principals in harness.go spell the same ones, and a
// fixture whose role key is a typo denies for the wrong reason while still
// reading as a refusal.
const (
	roleAdmin    = "admin"
	roleRep      = "rep"
	roleReadOnly = "read_only"
	roleOps      = "ops"
)

const (
	objPerson   = "person"
	objActivity = "activity"
	objDeal     = "deal"
	objOrg      = "organization"
	objPipeline = "pipeline"
	// objInstallSettings gates the read of the installation's own values —
	// name, base currency, timezone. Every fixture that reads deals or
	// accounts carries it, because those reads resolve the basis they are
	// reported in (issue #521), and 0191 grants it to all five seeded roles.
	objInstallSettings = "installation_settings"
)

// permissions fixtures mirror the RBAC matrix rows the suites
// exercise; the seeded JSONB↔these shapes is identity's policy tests.
var (
	RepPerms = principal.Permissions{
		RoleKeys: []string{roleRep},
		Objects: map[string]principal.ObjectGrant{
			objPerson:          {Create: true, Read: true, Update: true},
			objDeal:            {Create: true, Read: true, Update: true},
			objPipeline:        {Read: true},
			objInstallSettings: {Read: true},
		},
		RowScope: principal.RowScopeTeam,
	}
	// AccountRepPerms is the rep the account sections are read by: the
	// organization itself, its people and deals, its activities, and the tag/list
	// chips. It is a fixture in its own right rather than RepPerms plus a delta —
	// RepPerms stays narrow because several suites read it as a rep who CANNOT
	// see an organization, and widening it would make those pass while proving
	// nothing. Row scope stays team for the same reason: the interesting failures
	// here are row-scope ones, and an unbounded admin short-circuits every clause.
	AccountRepPerms = principal.Permissions{
		RoleKeys: []string{roleRep},
		Objects: map[string]principal.ObjectGrant{
			objOrg:             {Read: true},
			objPerson:          {Create: true, Read: true, Update: true},
			objDeal:            {Create: true, Read: true, Update: true},
			objActivity:        {Create: true, Read: true, Update: true},
			objPipeline:        {Read: true},
			"tag":              {Read: true},
			"list":             {Read: true},
			objInstallSettings: {Read: true},
		},
		RowScope: principal.RowScopeTeam,
	}
	ReadOnlyPerms = principal.Permissions{
		RoleKeys: []string{roleReadOnly},
		Objects: map[string]principal.ObjectGrant{
			objPerson: {Read: true}, objDeal: {Read: true}, objPipeline: {Read: true},
			objInstallSettings: {Read: true},
		},
		RowScope: principal.RowScopeAll,
	}
	// OpsPerms is the integrations identity: the real seed gives it the SAME
	// object grid as an admin — the role exists so machine-origin actions are
	// attributable, not so it holds narrower rights — over an unbounded row
	// scope. It carries the admin object map for exactly that reason: a
	// narrower fixture would let a suite conclude ops was refused for want of a
	// grant when production would have admitted it.
	//
	// What ops does NOT hold is governance authority: user administration, role
	// assignment, passport binding for another user, and the full-workspace
	// audit read are the admin's alone, and those gate on the role rather than
	// on any object. An unbounded row scope is not a stand-in for admin, which
	// is the confusion this fixture exists to make testable.
	// Cloned, not aliased: these are package-level fixtures, and a suite that
	// wrote one grant into a shared map would silently widen the admin too.
	OpsPerms = principal.Permissions{
		RoleKeys: []string{roleOps},
		Objects:  maps.Clone(AdminPerms.Objects),
		RowScope: principal.RowScopeAll,
	}
	// AdminWithSignals is AdminPerms plus the warm-room signal grants the real
	// admin role holds (identity/internal/policy.go). It is separate rather
	// than folded in because several tests read AdminPerms as "an admin who
	// cannot see signals" to prove a section is withheld — a fixture that
	// granted everything would make those pass without testing anything.
	AdminWithSignals = withFullSignalGrant(AdminPerms)
	AdminPerms       = principal.Permissions{
		RoleKeys: []string{roleAdmin},
		Objects: map[string]principal.ObjectGrant{
			objPerson:   {Create: true, Read: true, Update: true, Delete: true},
			objOrg:      {Create: true, Read: true, Update: true, Delete: true},
			objDeal:     {Create: true, Read: true, Update: true, Delete: true},
			"lead":      {Create: true, Read: true, Update: true, Delete: true},
			objActivity: {Create: true, Read: true, Update: true, Delete: true},
			objPipeline: {Create: true, Read: true, Update: true, Delete: true},
			// computed_field is read-only for every system role, admin
			// included (RD-AC-7: no runtime formula-authoring surface
			// exists) — identity/internal/policy.go's real seed, mirrored
			// here so the harness's admin fixture matches production.
			"computed_field": {Read: true},
			// fx_rate + ai_model_rate are admin/ops-only config surfaces
			// (identity/internal/policy.go's real seed), mirrored here so
			// the harness admin fixture can exercise the rate editors.
			"fx_rate":       {Create: true, Read: true, Update: true, Delete: true},
			"ai_model_rate": {Create: true, Read: true, Update: true, Delete: true},
			// capture_settings mirrors the real admin seed: create + read +
			// update (0210 added create — any seat may contribute a consumer
			// domain, and the admin fixture must hold what the seed holds or
			// the RBAC assertions stop being evidence about production). It
			// gates the workspace's own-domain set — including the
			// company-domain change that feeds it, since that decides whose
			// mail is stored at all.
			"capture_settings": {Create: true, Read: true, Update: true},
			// installation_settings mirrors 0191's real seed: readable by every
			// system role, updatable by admin/ops. Money readers resolve the
			// base currency through this gate.
			objInstallSettings: {Read: true, Update: true},
			"project":          {Create: true, Read: true, Update: true, Delete: true},
			"relationship":     {Create: true, Read: true, Update: true, Delete: true},
		},
		RowScope: principal.RowScopeAll,
	}
)

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The import seam: the tool surface's four import verbs, over the SAME
// handlers the REST transport uses.
//
// It delegates rather than reimplementing, and that is the whole design. The
// dry run is the only review a migrate-in flow gets, so two code paths that
// both "validate a mapping" would be two chances for them to disagree about
// what an import is going to do — and the one a person read would not be the
// one that ran.
//
// It holds the Server by pointer rather than copying importHandlers, because
// the object store arrives after assembly (WithBlobstore) and a seam that
// captured the handlers by value at wiring time could hold a copy with no
// blobstore in it. Same reason companyEnricher holds `srv` rather than a
// store. Both wiring sites build the seam AFTER the option loop has run, so
// what it points at is the Server that is actually served.
//
// A registry built with no Server at all — NewRegistry, which several roles and
// every parity gate use — gets a seam over bare handlers instead. Its reads
// work; its three source-bearing verbs refuse with errNoObjectStore, which is
// what a role that stores no objects can honestly offer. What it must NOT do is
// go missing: the contract declares these four verbs, and a registry that does
// not serve a declared verb advertises something tools/list cannot offer.

import (
	"context"
	"fmt"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/migration"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// importSeam adapts the import handlers to the agents module's Imports port.
type importSeam struct {
	// srv is the served Server, when there is one. The handlers are read off it
	// per call so a blobstore installed after wiring is visible.
	srv *Server
	// handlers is the standalone fallback, used only when srv is nil.
	handlers importHandlers
}

// door answers the handlers this seam speaks through.
func (i importSeam) door() importHandlers {
	if i.srv != nil {
		return i.srv.importHandlers
	}
	return i.handlers
}

// importsFor answers the seam. It is never nil, and that is deliberate.
//
// The first cut returned nil for a role with no object store, on the reasoning
// that four tools which always refuse are worse than none. That reasoning is
// wrong here: the CONTRACT declares these four verbs, and a registry that does
// not serve a declared verb fails TestEveryDeclaredToolVerbIsRegistered — the
// contract would be advertising something tools/list cannot offer, which is
// the exact dishonesty that gate exists to prevent. So the tools are always
// registered, and the three that need the source bytes refuse at call time
// with errNoObjectStore, naming what is missing.
//
//nolint:ireturn // the PORT is the return type: agents owns the interface, compose supplies an implementation of it, and naming the concrete type here would put the seam's shape on the wrong side of the module edge (ADR-0054). Same as tagSeam and colleagueLister.
func importsFor(s *Server) agents.Imports {
	return importSeam{srv: s}
}

// importsOverDB is importsFor for a registry with no Server: the reads reach
// the database, and everything needing the source file refuses.
//
//nolint:ireturn // same as importsFor above — the port is the return type.
func importsOverDB(db *database.DB) agents.Imports {
	return importSeam{handlers: importHandlers{db: db}}
}

func (i importSeam) ProfileSource(
	ctx context.Context, object, csv string,
) (crmcontracts.ImportSourceProfile, error) {
	// THE GRANT IS TAKEN BEFORE THE FILE IS STORED. profileAndStore parses the
	// CSV and puts it in the object store, and the first authorization check on
	// the REST path is the one CreateImportRun makes before calling it. The
	// tool path had no equivalent, so a caller with write scope and no
	// import_run grant could call preview_import repeatedly, leaving an orphan
	// blob each time before the refusal arrived — storage a stranger can spend,
	// and a probe of whether their CSV parses.
	if err := auth.Require(ctx, migration.ImportRunObject, principal.ActionCreate); err != nil {
		return crmcontracts.ImportSourceProfile{}, err
	}
	if err := i.ready(); err != nil {
		return crmcontracts.ImportSourceProfile{}, err
	}
	return i.door().profileAndStore(ctx, object, []byte(csv))
}

func (i importSeam) StageRun(
	ctx context.Context, req crmcontracts.CreateImportRunRequest,
) (crmcontracts.ImportRun, error) {
	if err := i.ready(); err != nil {
		return crmcontracts.ImportRun{}, err
	}
	return i.door().stageRun(ctx, req)
}

func (i importSeam) ReadRun(ctx context.Context, id ids.UUID) (crmcontracts.ImportRun, error) {
	run, err := i.door().stagedFor(ctx, openapi_types.UUID(id))
	if err != nil {
		return crmcontracts.ImportRun{}, err
	}
	return toContractImportRun(run), nil
}

func (i importSeam) ReadReport(ctx context.Context, id ids.UUID) (crmcontracts.ImportRunReport, error) {
	run, err := i.door().stagedFor(ctx, openapi_types.UUID(id))
	if err != nil {
		return crmcontracts.ImportRunReport{}, err
	}
	if run.Report == nil {
		// The same 409 the REST door gives, and for the same reason: a run
		// that has not been validated has no report, and answering an empty
		// one would read as "this import will do nothing".
		return crmcontracts.ImportRunReport{}, fmt.Errorf(
			"import run %s has not been validated yet: %w", run.ID, apperrors.ErrConflict)
	}
	return toContractImportReport(run), nil
}

func (i importSeam) Commit(ctx context.Context, id ids.UUID) (crmcontracts.ImportRun, error) {
	if err := i.ready(); err != nil {
		return crmcontracts.ImportRun{}, err
	}
	return i.door().commitRun(ctx, openapi_types.UUID(id))
}

// ready refuses the three verbs that need the source bytes when this role
// stores no objects.
//
// The two pure reads do not call it: reading a run's state or its report only
// touches the database, and refusing those for want of a blobstore would hide
// a run that genuinely exists.
func (i importSeam) ready() error {
	if i.door().blobs == nil {
		return errNoObjectStore
	}
	return nil
}

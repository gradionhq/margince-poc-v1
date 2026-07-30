// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The flip's stage materialization: incumbent deals carry a stage
// identity the native pipeline model has no direct slot for (OVA-MAP-6
// leaves overlay deals' pipeline/stage null), so the importer resolves
// them onto the native catalog here — an exact name match wins, the
// canonical closed keys land on the won/lost stages, and anything else
// falls back to the default pipeline's first open stage WITH a
// per-deal disclosure in the run report. The fallback is the disclosed
// spec-fill; the disclosure is what keeps it honest.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The native stage semantics (stage.semantic's CHECK vocabulary) and the
// incumbent's canonical closed-stage keys, named once.
const (
	stageSemanticOpen  = "open"
	stageSemanticWon   = "won"
	stageSemanticLost  = "lost"
	incumbentStageWon  = "closedwon"
	incumbentStageLost = "closedlost"
)

// flipStageCatalog resolves incumbent stage identities onto the native
// stage catalog: an exact (normalized) name match wins; HubSpot's
// canonical closedwon/closedlost keys land on the default pipeline's
// won/lost stages; anything else falls back to the default pipeline's
// first open stage, disclosed.
type flipStageCatalog struct {
	pipeline   ids.PipelineID // the default pipeline
	firstOpen  ids.StageID    // the default pipeline's first open stage
	openIn     map[ids.PipelineID]ids.StageID
	byName     map[string]flipStage
	bySemantic map[string]flipStage
}

type flipStage struct {
	id       ids.StageID
	pipeline ids.PipelineID
	semantic string
}

type flipPlacement struct {
	pipeline       ids.PipelineID
	birthStage     ids.StageID
	closedStage    *ids.StageID
	closedSemantic string
	matched        bool
}

func (c *flipStageCatalog) place(rawStage string) flipPlacement {
	norm := normalizeStageKey(rawStage)
	if st, ok := c.byName[norm]; ok {
		if st.semantic == stageSemanticOpen {
			return flipPlacement{pipeline: st.pipeline, birthStage: st.id, matched: true}
		}
		// A closed match is born on its own pipeline's first open stage
		// (transient — the advance below moves it out immediately); a
		// pipeline with no open stage cannot birth a deal at all, so the
		// whole placement falls back to the default pipeline.
		if birth, ok := c.openIn[st.pipeline]; ok {
			closed := st.id
			return flipPlacement{pipeline: st.pipeline, birthStage: birth, closedStage: &closed, closedSemantic: st.semantic, matched: true}
		}
	}
	if norm == incumbentStageWon || norm == incumbentStageLost {
		semantic := stageSemanticWon
		if norm == incumbentStageLost {
			semantic = stageSemanticLost
		}
		if st, ok := c.bySemantic[semantic]; ok {
			closed := st.id
			return flipPlacement{pipeline: st.pipeline, birthStage: c.firstOpen, closedStage: &closed, closedSemantic: semantic, matched: true}
		}
	}
	return flipPlacement{pipeline: c.pipeline, birthStage: c.firstOpen}
}

// add folds one native stage into the catalog. First-wins throughout:
// the query orders default-pipeline stages first and then by position,
// so the earliest match for a name, a pipeline's open stage, and the
// default pipeline's terminal stages are the ones kept.
func (c *flipStageCatalog) add(st flipStage, name string, isDefault bool) {
	key := normalizeStageKey(name)
	if _, taken := c.byName[key]; !taken {
		c.byName[key] = st
	}
	if st.semantic == stageSemanticOpen {
		if _, taken := c.openIn[st.pipeline]; !taken {
			c.openIn[st.pipeline] = st.id
		}
	}
	if !isDefault {
		return
	}
	if c.pipeline == (ids.PipelineID{}) {
		c.pipeline = st.pipeline
	}
	if st.semantic == stageSemanticOpen {
		if c.firstOpen == (ids.StageID{}) {
			c.firstOpen = st.id
		}
		return
	}
	if _, taken := c.bySemantic[st.semantic]; !taken {
		c.bySemantic[st.semantic] = st
	}
}

// disclosure names a deal whose incumbent stage did not resolve. It
// takes the placement the caller already computed rather than redoing
// the lookup, so the line can never describe a different decision than
// the one the deal actually got.
func stageDisclosure(p flipPlacement, rawStage, dealExt string) string {
	if !p.matched {
		if strings.TrimSpace(rawStage) == "" {
			return fmt.Sprintf("deal %s: no incumbent stage identity; placed on the default pipeline's first open stage", dealExt)
		}
		return fmt.Sprintf("deal %s: incumbent stage %q has no native match; placed on the default pipeline's first open stage", dealExt, rawStage)
	}
	return ""
}

func normalizeStageKey(s string) string {
	return strings.ToLower(strings.NewReplacer(" ", "", "-", "", "_", "").Replace(strings.TrimSpace(s)))
}

func (w *flipWriters) stageCatalog(ctx context.Context) (*flipStageCatalog, error) {
	if w.stages != nil {
		return w.stages, nil
	}
	cat := &flipStageCatalog{
		openIn: map[ids.PipelineID]ids.StageID{},
		byName: map[string]flipStage{}, bySemantic: map[string]flipStage{},
	}
	err := database.WithWorkspaceTx(ctx, w.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT s.id, s.pipeline_id, s.name, s.semantic, p.is_default
			FROM stage s JOIN pipeline p ON p.id = s.pipeline_id AND p.workspace_id = s.workspace_id
			WHERE s.archived_at IS NULL AND p.archived_at IS NULL
			ORDER BY p.is_default DESC, s.position`)
		if err != nil {
			return fmt.Errorf("flip import: reading the native stage catalog: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var st flipStage
			var name, semantic string
			var isDefault bool
			if err := rows.Scan(&st.id, &st.pipeline, &name, &semantic, &isDefault); err != nil {
				return fmt.Errorf("flip import: scanning a native stage: %w", err)
			}
			st.semantic = semantic
			cat.add(st, name, isDefault)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if cat.pipeline == (ids.PipelineID{}) || cat.firstOpen == (ids.StageID{}) {
		return nil, errors.New("flip import: the workspace has no default pipeline with an open stage; seed the workspace before flipping")
	}
	w.stages = cat
	return cat, nil
}

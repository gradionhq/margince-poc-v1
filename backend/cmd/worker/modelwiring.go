// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// modelPathSpec names the boot knobs selectModelPath switches on, so a call
// site labels each flag instead of passing anonymous booleans.
type modelPathSpec struct {
	routingPath     string
	fake            bool
	capturePayloads bool
}

// selectModelPath resolves the model path: a routing config for real
// deployments, the offline fake behind an explicit dev flag, or the
// zero path — the runner and the embed lane simply don't start without
// a declared model; nothing is picked silently.
func selectModelPath(spec modelPathSpec, pool *pgxpool.Pool, log *slog.Logger) (compose.ModelPath, map[string]map[string]bool, error) {
	switch {
	case spec.routingPath != "":
		cfg, err := ai.LoadRoutingFile(spec.routingPath)
		if err != nil {
			return compose.ModelPath{}, nil, err
		}
		// A task whose whole fallback ladder has no bound tier is not a
		// boot error (a deployment may legitimately not run every
		// workload), but it must be loud: log it now, not discover it
		// from a refused call.
		for _, w := range cfg.UnboundLadderWarnings() {
			log.Warn(w)
		}
		// The bound model ids travel with the path because they describe the
		// SAME binding: the cost refresh narrows a provider catalog to the
		// models this deployment actually calls.
		return modelPathWithBoundModels(cfg, pool, spec.capturePayloads, log)
	case spec.fake:
		// A real ModelPath over ai.FakeRoutingConfig() rather than
		// FakeModelPath's direct client wiring: the worker always has a
		// pool, so --ai-fake safely rides the real Router (tiering, the
		// budget guardrail, metering, call tracing) with only the
		// provider swapped for the deterministic fake. capturePayloads
		// still names the deployment's own posture — cmd/api's
		// resolveModelPath honors it on this same arm, and two process
		// roles must never disagree on whether content capture is on.
		return modelPathWithBoundModels(ai.FakeRoutingConfig(), pool, spec.capturePayloads, log)
	default:
		return compose.ModelPath{}, nil, nil
	}
}

// modelPathWithBoundModels assembles the path and reports which models the SAME
// binding names, so the two can never describe different routing configs.
func modelPathWithBoundModels(cfg ai.RoutingConfig, pool *pgxpool.Pool, capturePayloads bool, log *slog.Logger) (compose.ModelPath, map[string]map[string]bool, error) {
	path, err := compose.NewModelPath(cfg, pool, capturePayloads, log)
	return path, cfg.BoundModelIDsByProvider(), err
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package reporting

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// Handlers shadows the generated RunReport stub over the engine.
type Handlers struct {
	engine *Engine
}

// NewHandlers binds the report HTTP handlers to an engine.
func NewHandlers(e *Engine) Handlers { return Handlers{engine: e} }

func (h Handlers) RunReport(w http.ResponseWriter, r *http.Request, report string) {
	var req Request
	// The body is optional (a prebuilt report runs on its defaults);
	// anything present must decode strictly.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		httperr.Write(w, r, httperr.Validation("body", "malformed_json", err.Error()))
		return
	}

	outcome, err := h.engine.Run(r.Context(), report, req)
	if err != nil {
		writeReportError(w, r, err)
		return
	}

	// Every aggregate row carries its own "Explain This Number" handle
	// (AC-R6): the plan's filters plus the row's group-key values. The
	// result-level handle explains the whole filtered set.
	rows := make([]map[string]interface{}, len(outcome.Rows))
	copy(rows, outcome.Rows)
	for _, row := range rows {
		row[reservedDerivationColumn] = derivationURL(outcome.Report, outcome.Filters, outcome.GroupBy, outcome.Aggregates, row)
	}
	resultURL := derivationURL(outcome.Report, outcome.Filters, nil, outcome.Aggregates, nil)
	totalRows := len(rows)
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.ReportResult{
		Report:        outcome.Report,
		Plan:          outcome.Plan,
		Columns:       outcome.Columns,
		Rows:          rows,
		TotalRows:     &totalRows,
		GeneratedAt:   &outcome.GeneratedAt,
		DerivationUrl: &resultURL,
	})
}

// ExplainReport resolves a derivation handle: the plain-language
// definition plus the drill-through source rows behind one aggregate.
// The reserved `by`/`agg` keys and the free-form vocabulary predicates
// both live in the raw query string, so the parse owns the whole of it;
// the generated params struct is redundant here.
func (h Handlers) ExplainReport(w http.ResponseWriter, r *http.Request, report string, _ crmcontracts.ExplainReportParams) {
	q, err := parseDerivationQuery(r.URL.Query())
	if err != nil {
		writeReportError(w, r, err)
		return
	}
	outcome, err := h.engine.Derive(r.Context(), report, q)
	if err != nil {
		writeReportError(w, r, err)
		return
	}
	rows := make([]map[string]interface{}, len(outcome.Rows))
	copy(rows, outcome.Rows)
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.ReportDerivation{
		Report:      outcome.Report,
		Definition:  outcome.Definition,
		Plan:        outcome.Plan,
		Columns:     outcome.Columns,
		Rows:        rows,
		Aggregates:  &outcome.Aggregates,
		TotalRows:   &outcome.TotalRows,
		GeneratedAt: &outcome.GeneratedAt,
	})
}

// writeReportError maps the engine's vocabulary rejections to the
// contract's TOP-LEVEL 422 code (report_field_not_allowed), not a
// per-field validation entry; everything else rides the sentinels.
func writeReportError(w http.ResponseWriter, r *http.Request, err error) {
	var notAllowed *FieldNotAllowedError
	if errors.As(err, &notAllowed) {
		httperr.Write(w, r, &httperr.DetailedError{
			Status:  http.StatusUnprocessableEntity,
			Code:    "report_field_not_allowed",
			Detail:  notAllowed.Error(),
			Details: map[string]any{"field": notAllowed.Field},
		})
		return
	}
	httperr.Write(w, r, err)
}

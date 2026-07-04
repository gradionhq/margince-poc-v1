package compose

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// reportHandlers shadows the generated RunReport stub over the engine.
type reportHandlers struct {
	engine *reportEngine
}

func (h reportHandlers) RunReport(w http.ResponseWriter, r *http.Request, report string) {
	var req reportRequest
	// The body is optional (a prebuilt report runs on its defaults);
	// anything present must decode strictly.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		httperr.Write(w, r, httperr.Validation("body", "malformed_json", err.Error()))
		return
	}

	outcome, err := h.engine.Run(r.Context(), report, req)
	if err != nil {
		var notAllowed *FieldNotAllowedError
		if errors.As(err, &notAllowed) {
			// The contract pins the TOP-LEVEL code (422
			// report_field_not_allowed), not a per-field validation entry.
			httperr.Write(w, r, &httperr.DetailedError{
				Status:  http.StatusUnprocessableEntity,
				Code:    "report_field_not_allowed",
				Detail:  notAllowed.Error(),
				Details: map[string]any{"field": notAllowed.Field},
			})
			return
		}
		httperr.Write(w, r, err)
		return
	}

	rows := make([]map[string]interface{}, len(outcome.Rows))
	copy(rows, outcome.Rows)
	totalRows := len(rows)
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.ReportResult{
		Report:      outcome.Report,
		Plan:        outcome.Plan,
		Columns:     outcome.Columns,
		Rows:        rows,
		TotalRows:   &totalRows,
		GeneratedAt: &outcome.GeneratedAt,
	})
}

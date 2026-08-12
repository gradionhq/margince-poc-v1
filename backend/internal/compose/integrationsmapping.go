// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Mapping between the integrations module's own types and the generated
// contract shapes. Named toProviderConnection rather than
// toContractConnection because capture already owns that name for its own
// channel connections — two different things called "a connection".

import (
	"strconv"
	"strings"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/integrations"
	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

func toProviderConnection(c integrations.Connection) crmcontracts.ProviderConnection {
	cats := crmcontracts.ProviderCategorySelection{}
	for _, name := range c.Categories {
		cats[name] = true
	}

	cfg := crmcontracts.ProviderConfiguration{
		Mode:                      crmcontracts.ProviderConnectionMode(c.Mode),
		Preset:                    c.Preset,
		AutomaticIndividualCreate: c.AutomaticCreate,
		AutomaticImport:           c.AutomaticImport,
		Categories:                cats,
		RefreshAfterDays:          c.RefreshAfterDays,
		DailyRunLimit:             c.DailyRunLimit,
	}

	// Budgets and credits are two readings of the same pools: what the
	// customer told us to spend, and what the provider says is left.
	if len(c.Budgets) > 0 {
		budgets := map[string]crmcontracts.ProviderPoolBudget{}
		for _, b := range c.Budgets {
			budgets[b.Pool] = crmcontracts.ProviderPoolBudget{
				MonthlyCeiling:    b.MonthlyCeiling,
				PauseBelowBalance: b.PauseBelowBalance,
			}
		}
		cfg.Budgets = &budgets
	}

	credits := crmcontracts.ProviderCredits{Pools: map[string]*int{}}
	for _, b := range c.Budgets {
		credits.Pools[b.Pool] = b.LastKnownBalance
		if b.BalanceReadAt != nil && credits.ReadAt == nil {
			credits.ReadAt = b.BalanceReadAt
		}
	}

	out := crmcontracts.ProviderConnection{
		Provider:      crmcontracts.Provider(c.Provider),
		Status:        crmcontracts.ProviderConnectionStatus(c.Status),
		Configuration: cfg,
		Credits:       credits,
		// The ONLY credential fact that ever leaves: whether one is set.
		CredentialPresent: c.CredentialPresent,
		ConnectedAt:       c.ConnectedAt,
		LastVerifiedAt:    c.LastVerifiedAt,
		LastUsedAt:        c.LastUsedAt,
		CreatedAt:         c.CreatedAt,
		UpdatedAt:         c.UpdatedAt,
	}
	if c.SafeStatusCode != "" {
		code := c.SafeStatusCode
		out.SafeStatusCode = &code
	}
	if c.Version != 0 {
		v := crmcontracts.RowVersion(c.Version)
		out.Version = &v
	}
	return out
}

// fromProviderConfig maps a connect body's optional configuration. Absent
// resolves to the descriptor's defaults inside the store, so nil stays nil.
func fromProviderConfig(in *crmcontracts.ProviderConfiguration) *integrations.ConfigInput {
	if in == nil {
		return nil
	}
	out := &integrations.ConfigInput{
		Mode:             string(in.Mode),
		Preset:           in.Preset,
		Categories:       selectedCategories(in.Categories),
		AutomaticCreate:  &in.AutomaticIndividualCreate,
		AutomaticImport:  &in.AutomaticImport,
		RefreshAfterDays: in.RefreshAfterDays,
		DailyRunLimit:    in.DailyRunLimit,
		Budgets:          fromProviderBudgets(in.Budgets),
	}
	return out
}

// fromProviderConfigPatch maps a sparse PATCH body. Every field is optional
// here: an absent one means "leave it alone", which is what makes the patch
// sparse rather than a full replacement.
func fromProviderConfigPatch(in crmcontracts.ProviderConfigurationPatch) integrations.ConfigPatch {
	out := integrations.ConfigPatch{
		AutomaticCreate:  in.AutomaticIndividualCreate,
		AutomaticImport:  in.AutomaticImport,
		RefreshAfterDays: in.RefreshAfterDays,
		DailyRunLimit:    in.DailyRunLimit,
	}
	if in.Mode != nil {
		m := string(*in.Mode)
		out.Mode = &m
	}
	if in.Preset != nil {
		out.Preset = in.Preset
	}
	if in.Categories != nil {
		cats := selectedCategories(*in.Categories)
		out.Categories = &cats
	}
	if in.Budgets != nil {
		b := fromProviderBudgets(in.Budgets)
		out.Budgets = &b
	}
	return out
}

// selectedCategories keeps only the categories set to true. The contract
// carries a map of booleans, so an explicit false is a deselection rather
// than a selection — dropping it here is what makes "at least one selected"
// mean what it says.
func selectedCategories(sel crmcontracts.ProviderCategorySelection) []string {
	out := make([]string, 0, len(sel))
	for name, on := range sel {
		if on {
			out = append(out, name)
		}
	}
	return out
}

func fromProviderBudgets(in *map[string]crmcontracts.ProviderPoolBudget) []integrations.PoolBudget {
	if in == nil {
		return nil
	}
	out := make([]integrations.PoolBudget, 0, len(*in))
	for pool, b := range *in {
		out = append(out, integrations.PoolBudget{
			Pool:              pool,
			MonthlyCeiling:    b.MonthlyCeiling,
			PauseBelowBalance: b.PauseBelowBalance,
		})
	}
	return out
}

func toProviderRun(r provider.Run) crmcontracts.ProviderRun {
	cats := make([]string, 0, len(r.RequestedCategories))
	for _, c := range r.RequestedCategories {
		cats = append(cats, string(c))
	}
	out := crmcontracts.ProviderRun{
		Id:              mustUUID(r.ID),
		SubjectKind:     crmcontracts.ProviderRunSubjectKind(r.SubjectKind),
		Provider:        crmcontracts.Provider(r.Provider),
		Trigger:         crmcontracts.ProviderRunTrigger(r.Trigger),
		State:           crmcontracts.ProviderRunState(r.State),
		ClaimsUnwritten: r.ClaimsUnwritten,
		// The frozen snapshot, echoed so a caller can see what the run was
		// admitted under rather than what the connection says now.
		ConfigurationSnapshot: toProviderSnapshot(r.Snapshot),
		ConnectionVersion:     r.ConnectionVersion,
		RequestedCategories:   cats,
		CreatedAt:             r.CreatedAt,
		UpdatedAt:             r.UpdatedAt,
		SubmittedAt:           r.SubmittedAt,
		CompletedAt:           r.CompletedAt,
	}
	if r.PersonID != "" {
		id := mustUUID(r.PersonID)
		out.PersonId = &id
	}
	if r.SkipReason != "" {
		reason := crmcontracts.ProviderRunSkipReason(r.SkipReason)
		out.SkipReason = &reason
	}
	if r.SafeStatusCode != "" {
		code := r.SafeStatusCode
		out.SafeStatusCode = &code
	}
	for _, res := range r.Reservations {
		out.Reservations = append(out.Reservations, struct {
			ActualCredits   *int   `json:"actual_credits,omitempty"`
			Pool            string `json:"pool"`
			ReservedCredits int    `json:"reserved_credits"`
		}{Pool: string(res.Pool), ReservedCredits: res.Reserved, ActualCredits: res.Actual})
	}
	return out
}

// derefString reads an optional contract string. The generated ApiKey is a
// pointer because the schema marks it writeOnly; absent is empty, which the
// store then refuses by name.
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ifMatchVersion reads the optional If-Match header into a version. Absent is
// zero, which the store treats as an unconditional write — the contract makes
// the header optional, so requiring it here would refuse a legal request.
//
// A header that is PRESENT but unparseable is a different thing entirely, and
// it must not become zero: the caller asked for a conditional write, and
// answering it with an unconditional one applies the very overwrite they were
// guarding against. The error says the header is malformed rather than letting
// the write through and hoping the store catches it — the store cannot check a
// version nobody handed it.
func ifMatchVersion(v *crmcontracts.IfMatch) (int64, error) {
	if v == nil {
		return 0, nil
	}
	n, err := strconv.ParseInt(strings.Trim(string(*v), `"`), 10, 64)
	if err != nil {
		return 0, &malformedIfMatchError{}
	}
	return n, nil
}

// malformedIfMatchError maps to 422: the precondition could not be read, so
// the write is refused rather than silently promoted to unconditional.
//
// It implements MessageFault rather than FieldFault because the offending
// input is a HEADER, and no request-body field path names it — publishing
// "If-Match" in the field slot would put prose where a machine reads a
// contract path.
type malformedIfMatchError struct{}

func (e *malformedIfMatchError) Error() string {
	return "the If-Match header must be the last-seen integer version"
}

func (e *malformedIfMatchError) MessageFault() (code, message string) {
	// The rejected value is deliberately not echoed: it is caller-supplied
	// text and reflecting it is how a header becomes an injection vector.
	return "malformed_if_match", "The If-Match header must be the last-seen integer version. Re-read the connection and retry with its version."
}

// toProviderSnapshot renders the frozen configuration in the same shape the
// live configuration uses, so a caller comparing "what this run was allowed"
// against "what the connection allows now" is comparing like with like.
func toProviderSnapshot(s provider.Snapshot) crmcontracts.ProviderConfiguration {
	cats := crmcontracts.ProviderCategorySelection{}
	for _, c := range s.Categories {
		cats[string(c)] = true
	}
	return crmcontracts.ProviderConfiguration{
		Mode:                      crmcontracts.ProviderConnectionMode(s.Mode),
		Preset:                    s.Preset,
		AutomaticIndividualCreate: s.AutomaticCreate,
		AutomaticImport:           s.AutomaticImport,
		Categories:                cats,
		RefreshAfterDays:          s.RefreshAfterDays,
		DailyRunLimit:             s.DailyRunLimit,
	}
}

// mustUUID parses an id the database produced. A malformed one is a
// programming error rather than a caller's mistake, and the zero uuid is the
// honest rendering of "this row's id did not parse".
func mustUUID(s string) openapi_types.UUID {
	parsed, err := uuid.Parse(s)
	if err != nil {
		return openapi_types.UUID{}
	}
	return parsed
}

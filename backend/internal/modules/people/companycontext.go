// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// CompanyContextScope is one bounded business-context view. The order below is
// also the wire and fingerprint order; callers cannot make the same selection
// hash differently by changing query-parameter order.
type CompanyContextScope string

const (
	// CompanyContextIdentity holds names, domains and firmographic identity.
	CompanyContextIdentity CompanyContextScope = "identity"
	// CompanyContextPositioning holds ICP, value and differentiation statements.
	CompanyContextPositioning CompanyContextScope = "positioning"
	// CompanyContextSales holds pains, buyers, triggers, objections and motion.
	CompanyContextSales CompanyContextScope = "sales"
	// CompanyContextOffer holds offers, capabilities and technologies.
	CompanyContextOffer CompanyContextScope = "offer"
	// CompanyContextMarket holds served industries, sizes, regions and languages.
	CompanyContextMarket CompanyContextScope = "market"
	// CompanyContextProof holds grounded customer, outcome and credential claims.
	CompanyContextProof CompanyContextScope = "proof"
	// CompanyContextAdministrative holds legal and registration identity.
	CompanyContextAdministrative CompanyContextScope = "administrative"
)

var companyContextScopeOrder = []CompanyContextScope{
	CompanyContextIdentity,
	CompanyContextPositioning,
	CompanyContextSales,
	CompanyContextOffer,
	CompanyContextMarket,
	CompanyContextProof,
	CompanyContextAdministrative,
}

// ParseCompanyContextScope rejects unknown scope names instead of silently
// widening or narrowing the context a caller requested.
func ParseCompanyContextScope(value string) (CompanyContextScope, bool) {
	scope := CompanyContextScope(value)
	for _, candidate := range companyContextScopeOrder {
		if scope == candidate {
			return scope, true
		}
	}
	return "", false
}

// CompanyContextItem is one confirmed statement or accepted fact supplied to
// a bounded context consumer. Evidence snippets stay on the company-profile
// read; model context carries the source URL and confidence without copying raw
// page text into every call.
type CompanyContextItem struct {
	Key        string
	Value      string
	Source     string
	CapturedBy string
	SourceURL  string
	Confidence *float32
}

// CompanyContextSection is one requested scope, including an honest empty
// items list when the company has no confirmed data for it.
type CompanyContextSection struct {
	Scope CompanyContextScope
	Items []CompanyContextItem
}

// CompanyContext is the deterministic read model over the anchor organization,
// its curated profile and its repeatable facts.
type CompanyContext struct {
	OrganizationID ids.OrganizationID
	SchemaVersion  int
	Scopes         []CompanyContextSection
	Fingerprint    string
	GeneratedAt    time.Time
}

var profileContextScopes = map[string]CompanyContextScope{
	fieldDisplayName:       CompanyContextIdentity,
	fieldIndustry:          CompanyContextIdentity,
	fieldHistory:           CompanyContextIdentity,
	fieldICP:               CompanyContextPositioning,
	fieldValueProposition:  CompanyContextPositioning,
	fieldUSP:               CompanyContextPositioning,
	fieldDesiredOutcomes:   CompanyContextPositioning,
	fieldCustomerPains:     CompanyContextSales,
	fieldBuyingCenter:      CompanyContextSales,
	fieldBuyingIntents:     CompanyContextSales,
	fieldCommonObjections:  CompanyContextSales,
	fieldSalesMotion:       CompanyContextSales,
	fieldOfferSummary:      CompanyContextOffer,
	fieldLegalName:         CompanyContextAdministrative,
	fieldRegisteredAddress: CompanyContextAdministrative,
	fieldRegisterVat:       CompanyContextAdministrative,
}

// GetCompanyContext reads and assembles the selected anchor-company scopes
// under the normal workspace transaction and organization visibility gate.
func (s *Store) GetCompanyContext(ctx context.Context, requested []CompanyContextScope) (CompanyContext, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return CompanyContext{}, err
	}
	var out CompanyContext
	err := s.tx(ctx, func(tx pgx.Tx) error {
		orgID, err := anchorOrganization(ctx, tx, false)
		if err != nil {
			return err
		}
		if err := auth.EnsureVisible(ctx, tx, "organization", orgID.UUID); err != nil {
			return err
		}
		company, err := readCompany(ctx, tx, orgID)
		if err != nil {
			return err
		}
		var generatedAt time.Time
		if err := tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&generatedAt); err != nil {
			return fmt.Errorf("read company context timestamp: %w", err)
		}
		out = assembleCompanyContext(company, requested, generatedAt)
		return nil
	})
	if err != nil {
		return CompanyContext{}, err
	}
	return out, nil
}

func assembleCompanyContext(company Company, requested []CompanyContextScope, generatedAt time.Time) CompanyContext {
	scopes := normalizeCompanyContextScopes(requested)
	sections := make(map[CompanyContextScope][]CompanyContextItem, len(scopes))
	selected := make(map[CompanyContextScope]bool, len(scopes))
	for _, scope := range scopes {
		selected[scope] = true
		sections[scope] = []CompanyContextItem{}
	}

	seenDisplayName := false
	for _, field := range company.ProfileFields {
		scope, known := profileContextScopes[field.Field]
		if !known || !selected[scope] {
			continue
		}
		confidence := field.Confidence
		sections[scope] = append(sections[scope], CompanyContextItem{
			Key: field.Field, Value: field.Value, Source: field.Source,
			CapturedBy: field.CapturedBy, SourceURL: field.SourceURL,
			Confidence: &confidence,
		})
		seenDisplayName = seenDisplayName || field.Field == fieldDisplayName
	}
	if selected[CompanyContextIdentity] {
		fallbackSource := normalizeCompanySource(company.OrganizationSource)
		if !seenDisplayName {
			sections[CompanyContextIdentity] = append(sections[CompanyContextIdentity], CompanyContextItem{
				Key: fieldDisplayName, Value: company.DisplayName, Source: fallbackSource,
				CapturedBy: company.OrganizationCapturedBy,
			})
		}
		if company.Website != nil {
			sections[CompanyContextIdentity] = append(sections[CompanyContextIdentity], CompanyContextItem{
				Key: "primary_domain", Value: *company.Website, Source: fallbackSource,
				CapturedBy: company.OrganizationCapturedBy,
			})
		}
	}
	for _, fact := range company.Facts {
		scope, known := factContextScope(fact)
		if !known || !selected[scope] {
			continue
		}
		confidence := fact.Confidence
		sections[scope] = append(sections[scope], CompanyContextItem{
			Key: fact.Field, Value: fact.Value, Source: fact.Source,
			CapturedBy: fact.CapturedBy, SourceURL: fact.SourceURL,
			Confidence: &confidence,
		})
	}

	ordered := make([]CompanyContextSection, 0, len(scopes))
	for _, scope := range scopes {
		items := sections[scope]
		sort.Slice(items, func(i, j int) bool {
			left, right := items[i], items[j]
			return companyContextItemKey(left) < companyContextItemKey(right)
		})
		ordered = append(ordered, CompanyContextSection{Scope: scope, Items: items})
	}
	return CompanyContext{
		OrganizationID: company.OrganizationID,
		SchemaVersion:  1,
		Scopes:         ordered,
		Fingerprint:    fingerprintCompanyContext(ordered),
		GeneratedAt:    generatedAt,
	}
}

func factContextScope(fact CompanyFact) (CompanyContextScope, bool) {
	switch fact.Category {
	case factCategoryCompany:
		return CompanyContextIdentity, true
	case factCategoryOffering, factCategorySignal:
		if fact.Category == factCategorySignal && fact.Field != "technology" {
			return CompanyContextProof, true
		}
		return CompanyContextOffer, true
	case factCategoryMarket:
		return CompanyContextMarket, true
	default:
		return "", false
	}
}

func normalizeCompanyContextScopes(requested []CompanyContextScope) []CompanyContextScope {
	if len(requested) == 0 {
		return append([]CompanyContextScope(nil), companyContextScopeOrder...)
	}
	wanted := make(map[CompanyContextScope]bool, len(requested))
	for _, scope := range requested {
		wanted[scope] = true
	}
	ordered := make([]CompanyContextScope, 0, len(wanted))
	for _, scope := range companyContextScopeOrder {
		if wanted[scope] {
			ordered = append(ordered, scope)
		}
	}
	return ordered
}

func companyContextItemKey(item CompanyContextItem) string {
	confidence := ""
	if item.Confidence != nil {
		confidence = fmt.Sprintf("%.6f", *item.Confidence)
	}
	return strings.Join([]string{item.Key, item.Value, item.Source, item.CapturedBy, item.SourceURL, confidence}, "\x00")
}

func fingerprintCompanyContext(sections []CompanyContextSection) string {
	var canonical strings.Builder
	for _, section := range sections {
		canonical.WriteString(string(section.Scope))
		canonical.WriteByte('\n')
		for _, item := range section.Items {
			canonical.WriteString(companyContextItemKey(item))
			canonical.WriteByte('\n')
		}
	}
	digest := sha256.Sum256([]byte(canonical.String()))
	return hex.EncodeToString(digest[:])
}

func normalizeCompanySource(source string) string {
	switch source {
	case "manual", companySourceHuman:
		return companySourceHuman
	case "coldstart", "deepread", companySourceSiteRead:
		return companySourceSiteRead
	case "enrich", "connector":
		return "connector"
	default:
		return "migration"
	}
}

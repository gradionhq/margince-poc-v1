// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// qualify_lead (interfaces.md §2.2, DECISIONS A15): gap-only agentic
// qualification. The tool fills ONLY fields that are both currently
// empty and deterministically inferable from the lead's own data, then
// surfaces what still needs a human — it never overwrites a value and
// never invents one (a fill without evidence is a guess, and guesses
// are absent by construction).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// qualificationFields is the lead's qualification surface, in the fixed
// order gaps are reported (derived from the contract's lead shape).
var qualificationFields = []string{"email", "full_name", "company_name", "title", "source"}

type qualifyLead struct {
	p datasource.SystemOfRecordProvider
}

func (t qualifyLead) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "qualify_lead", Version: "1.0.0",
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "getLead + updateLead",
		InputSchema: schema(`{"type":"object","required":["record_id"],"properties":{
			"record_id":{"type":"string","format":"uuid","description":"The lead to qualify"}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t qualifyLead) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct {
		RecordID ids.UUID `json:"record_id"`
	}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	rec, err := t.p.Read(ctx, datasource.EntityRef{Type: datasource.EntityLead, ID: args.RecordID})
	if err != nil {
		return nil, err
	}
	var lead struct {
		Email       *string `json:"email"`
		FullName    *string `json:"full_name"`
		CompanyName *string `json:"company_name"`
		Title       *string `json:"title"`
		Source      *string `json:"source"`
	}
	if err := json.Unmarshal(rec.Fields, &lead); err != nil {
		return nil, fmt.Errorf("crmagents: lead %s read back with unreadable fields: %w", args.RecordID, err)
	}

	patch := map[string]string{}
	filled := map[string]any{}
	if isBlank(lead.CompanyName) && !isBlank(lead.Email) {
		if company, ok := companyFromEmailDomain(*lead.Email); ok {
			patch["company_name"] = company
			lead.CompanyName = &company
			filled["company_name"] = map[string]any{
				"value": company,
				"evidence": []map[string]string{
					{"source": "lead.email", "snippet": *lead.Email},
				},
			}
		}
	}

	if len(patch) > 0 {
		raw, err := json.Marshal(patch)
		if err != nil {
			return nil, err
		}
		// Pin the update to the version the fill decision was read from:
		// if the lead changed underneath, the honest answer is skew, not a
		// blind write over whatever it became.
		if _, err := t.p.Update(ctx, datasource.UpdateInput{
			Ref:       datasource.EntityRef{Type: datasource.EntityLead, ID: args.RecordID},
			Patch:     raw,
			Source:    toolSource,
			IfVersion: &rec.Version,
		}); err != nil {
			return nil, err
		}
	}

	gaps := []string{}
	for _, field := range qualificationFields {
		var value *string
		switch field {
		case "email":
			value = lead.Email
		case "full_name":
			value = lead.FullName
		case "company_name":
			value = lead.CompanyName
		case "title":
			value = lead.Title
		case "source":
			value = lead.Source
		}
		if isBlank(value) {
			gaps = append(gaps, field)
		}
	}
	return json.Marshal(map[string]any{
		"record_id": args.RecordID,
		"filled":    filled,
		"gaps":      gaps,
	})
}

func isBlank(s *string) bool { return s == nil || strings.TrimSpace(*s) == "" }

// freemailDomains are provider domains that name a mailbox host, never
// the lead's company — an inference from them would be a guess.
var freemailDomains = map[string]bool{
	"gmail.com": true, "googlemail.com": true, "yahoo.com": true,
	"outlook.com": true, "hotmail.com": true, "live.com": true,
	"icloud.com": true, "me.com": true, "aol.com": true,
	"gmx.de": true, "gmx.net": true, "web.de": true, "t-online.de": true,
	"proton.me": true, "protonmail.com": true,
}

// companyFromEmailDomain derives a company name from a corporate email
// domain: the registrable label, word-split on -/_ and title-cased
// ("jane@acme-corp.io" → "Acme Corp"). Freemail domains and bare hosts
// yield nothing — reporting the gap beats inventing an employer.
func companyFromEmailDomain(email string) (string, bool) {
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return "", false
	}
	domain := strings.ToLower(strings.TrimSpace(email[at+1:]))
	if freemailDomains[domain] {
		return "", false
	}
	label, _, hasTLD := strings.Cut(domain, ".")
	if !hasTLD || label == "" {
		return "", false
	}
	words := strings.FieldsFunc(label, func(r rune) bool { return r == '-' || r == '_' })
	if len(words) == 0 {
		return "", false
	}
	for i, w := range words {
		runes := []rune(w)
		runes[0] = unicode.ToUpper(runes[0])
		words[i] = string(runes)
	}
	return strings.Join(words, " "), true
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package surfe

// Turning Surfe's answer into the bounded claim shapes the platform stores.
//
// Two rules govern everything here, and both exist because the alternative
// asserts something the vendor never said:
//
//   - An EMPTY STRING is not a value. Surfe returns "" for job-history fields
//     it does not have, and a stored empty string renders as a blank line on
//     the page and exports as one in an Art. 15 package.
//   - A label the platform inferred is never presented as the provider's.
//     emailType is frequently ABSENT even under the professional cascade, so
//     the address is labeled from what was REQUESTED and marked as such —
//     the person page reads that marker to say which is which.
//
// What the vendor returns beyond the requested categories is dropped: the
// claim vocabulary is closed, and no raw payload is retained.

import (
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

// claimsFor normalizes one person's result. A category the vendor answered
// emptily produces NO claim rather than an empty one, so "we asked and they
// had nothing" is stored as an absence and reads as one.
func claimsFor(p wireResult) ([]provider.Claim, error) {
	var claims []provider.Claim
	var failed error
	add := func(key provider.ClaimKey, value any) {
		raw, err := json.Marshal(value)
		if err != nil {
			// Surfaced, never dropped. A dropped claim is indistinguishable
			// from a category the vendor had nothing for — and if every claim
			// dropped, Poll would read the empty set as no_match and release
			// the reservation for a run that completed and was charged.
			failed = fmt.Errorf("surfe: encoding the %s claim: %w", key, err)
			return
		}
		claims = append(claims, provider.Claim{Key: key, Value: raw})
	}

	professional, personal := splitEmails(p.Emails)
	if len(professional) > 0 {
		add(provider.ClaimProfessionalEmails, professional)
	}
	if len(personal) > 0 {
		add(provider.ClaimPersonalEmails, personal)
	}
	if mobiles := mobilesFor(p.MobilePhones); len(mobiles) > 0 {
		add(provider.ClaimMobilePhones, mobiles)
	}
	if p.LinkedInURL != "" {
		add(provider.ClaimLinkedInProfile, p.LinkedInURL)
	}
	if p.JobTitle != "" || p.CompanyName != "" {
		add(provider.ClaimCurrentEmployment, map[string]any{
			"company_name":   p.CompanyName,
			"company_domain": p.CompanyDomain,
			"job_title":      p.JobTitle,
		})
	}
	if history := historyFor(p.JobHistory); len(history) > 0 {
		add(provider.ClaimJobHistory, history)
	}
	if p.Location != "" {
		add(provider.ClaimLocation, p.Location)
	}
	if len(p.Departments) > 0 {
		add(provider.ClaimDepartments, p.Departments)
	}
	if len(p.Seniorities) > 0 {
		add(provider.ClaimSeniorities, p.Seniorities)
	}
	if failed != nil {
		return nil, failed
	}
	return claims, nil
}

// splitEmails separates what the vendor labeled personal from everything
// else. An address with NO type is professional: that is what the
// professional cascade asked for, and the platform records that the label
// came from the request rather than from Surfe (email_type is left absent
// here, and the reader supplies the requested-cascade marker).
func splitEmails(emails []wireEmail) (professional, personal []map[string]any) {
	for _, e := range emails {
		if e.Email == "" {
			continue
		}
		entry := map[string]any{"value": e.Email}
		if e.ValidationStatus != "" {
			entry["validation_status"] = e.ValidationStatus
		}
		if e.EmailType != "" {
			entry["email_type"] = e.EmailType
		}
		if e.EmailType == emailTypePersonal {
			personal = append(personal, entry)
			continue
		}
		professional = append(professional, entry)
	}
	return professional, personal
}

// mobilesFor keeps the number and the vendor's own confidence. The score is
// carried rather than rounded to a band: it is the vendor's assertion, and
// the display layer decides how much precision to show.
func mobilesFor(phones []wireMobile) []map[string]any {
	var out []map[string]any
	for _, m := range phones {
		if m.MobilePhone == "" {
			continue
		}
		entry := map[string]any{"value": m.MobilePhone}
		if m.ConfidenceScore != nil {
			entry["confidence"] = *m.ConfidenceScore
		}
		out = append(out, entry)
	}
	return out
}

// historyFor normalizes past roles, dropping the empty strings Surfe returns
// for fields it lacks and the entries that carry no employer at all — a role
// with no company name is a line the page cannot render.
func historyFor(jobs []wireJob) []map[string]any {
	var out []map[string]any
	for _, j := range jobs {
		if j.CompanyName == "" {
			continue
		}
		entry := map[string]any{"company_name": j.CompanyName}
		if j.JobTitle != "" {
			entry["job_title"] = j.JobTitle
		}
		if j.LinkedInURL != "" {
			entry["linkedin_url"] = j.LinkedInURL
		}
		if started := monthOrDate(j.StartDate); started != "" {
			entry["started_at"] = started
		}
		if ended := monthOrDate(j.EndDate); ended != "" {
			entry["ended_at"] = ended
		}
		out = append(out, entry)
	}
	return out
}

// spendFor reports what Surfe charged, per pool: ONE credit per pool that
// returned anything.
//
// The vendor's response carries no cost figure, so the charge is derived from
// what it RETURNED — that is what per-successful-result billing means, and it
// is why a run that asked for an email and a mobile but got only the email
// releases the mobile hold.
//
// One credit per pool, not one per value, and that is the load-bearing part.
// Surfe's `emails` array is PLURAL: it returns every address it found for the
// subject, from the single lookup the run paid for. Counting them would bill
// a well-documented person more than a thinly-documented one for the same
// question, and would exceed the reservation the platform took — a number
// reconcile writes into actual_credits and poolUsedThisMonth sums against the
// customer's monthly ceiling, so the over-charge would silently eat budget
// later runs were counting on.
//
// The platform clamps this against the run's own reservation as well
// (integrations.reconcile): this function knows what the vendor answered, and
// only the ledger knows what was held.
func spendFor(p wireResult) map[provider.Pool]int {
	spend := map[provider.Pool]int{}
	for _, e := range p.Emails {
		if e.Email != "" {
			spend[poolEmail] = 1
			break
		}
	}
	for _, m := range p.MobilePhones {
		if m.MobilePhone != "" {
			spend[poolMobile] = 1
			break
		}
	}
	return spend
}

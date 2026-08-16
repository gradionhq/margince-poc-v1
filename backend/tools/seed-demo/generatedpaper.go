// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The paper a generated company holds: its contracts, and the account
// documents that belong to no contract.
//
// Contracts are built as demoContract values and pushed through the SAME
// ensureContract / driveContract the dataset's own use, rather than posting
// to /v1/contracts directly. That machinery already knows the things worth
// not relearning: a contract is born draft, cancelled is reached by recording
// a cancellation rather than asserting a status, and superseded exists only
// as the front half of a renewal that writes its successor in one
// transaction.
//
// The PDFs come free. seedDocuments walks every contract the installation
// holds, so a generated contract gets its page the moment it exists.

import (
	"fmt"
	"strings"
)

// seedGeneratedContracts files the agreements each profile calls for.
func seedGeneratedContracts(c *client, refs pipelineRefs, plan map[string]profile, mode runMode) (int, error) {
	created := 0
	for _, domain := range sortedDomains(plan) {
		p := plan[domain]
		if p.Pinned || len(p.Contracts) == 0 {
			continue
		}
		if _, ok := refs.orgsByDom[domain]; !ok {
			continue
		}
		contracts := generatedContractsFor(domain, refs, p)

		// renewContract reads its successor's TERMS out of contractsByRef,
		// which otherwise holds only demo.json's own — a generated renewal
		// would fail with "renews_into names X, which is not in this dataset".
		// Built fresh per company rather than appended in place, so one
		// company's contracts can never leak into the next one's lookup.
		local := refs
		local.contractsByRef = append(append([]demoContract(nil), refs.contractsByRef...), contracts...)

		ids := map[string]string{}
		for _, contract := range contracts {
			// A successor is NOT created here: renewing writes its own row,
			// and creating it first would leave two. Same rule seedContracts
			// applies to the dataset's own chain.
			if isGeneratedSuccessor(contracts, contract.Ref) {
				continue
			}
			id, isNew, err := ensureContract(c, contract, local, mode)
			if err != nil {
				return created, err
			}
			if isNew {
				created++
			}
			ids[contract.Ref] = id
		}
		// Driving runs after every contract of this company exists, because a
		// renewal names a successor that is later in the list.
		for _, contract := range contracts {
			if isGeneratedSuccessor(contracts, contract.Ref) {
				continue
			}
			if err := driveContract(c, contract, ids, local, mode); err != nil {
				return created, fmt.Errorf("contract for %s: %w", domain, err)
			}
		}
	}
	return created, nil
}

// isGeneratedSuccessor reports whether this ref is the far side of a renewal
// in the same set. Renewing writes the successor's row itself, so creating it
// beforehand would leave two contracts where the chain wants one.
func isGeneratedSuccessor(contracts []demoContract, ref string) bool {
	for _, contract := range contracts {
		if contract.RenewsInto == ref {
			return true
		}
	}
	return false
}

// generatedContractsFor turns a profile's wanted statuses into contracts.
//
// A renewal chain is a special shape: the profile asks for "superseded" and
// gets TWO entries, the predecessor plus the successor it renews into. The
// successor is never created directly — renewContract writes it — so it is
// listed only as the renews_into target.
func generatedContractsFor(domain string, refs pipelineRefs, p profile) []demoContract {
	orgID := refs.orgsByDom[domain]
	name := refs.orgNameByID[orgID]
	value := generatedAmount(domain)

	var out []demoContract
	for i, status := range p.Contracts {
		ref := fmt.Sprintf("gen-%s-%d", domain, i)
		contract := demoContract{
			Ref:              ref,
			Company:          domain,
			Title:            generatedContractTitle(name, status, i),
			ContractNumber:   fmt.Sprintf("V-%04d-%s", 1000+hashIndex("cnum:"+ref, 8999), strings.ToUpper(shortDomain(domain))),
			ValueMinor:       value,
			Currency:         "EUR",
			ValueBasis:       "annualized_12m",
			AutoRenew:        hashIndex("autorenew:"+domain, 2) == 0,
			NoticePeriodDays: 90,
			Status:           status,
		}
		switch status {
		case "active":
			contract.SignedInDays = -(30 + hashIndex("signed:"+ref, 300))
			contract.StartsInDays = contract.SignedInDays + 14
			contract.EndsInDays = contract.StartsInDays + 365
		case "expired":
			// Ended already: signed well over a year ago and run out.
			contract.SignedInDays = -(500 + hashIndex("signed:"+ref, 200))
			contract.StartsInDays = contract.SignedInDays + 14
			contract.EndsInDays = -(10 + hashIndex("ended:"+ref, 60))
		case "cancelled":
			contract.SignedInDays = -(200 + hashIndex("signed:"+ref, 150))
			contract.StartsInDays = contract.SignedInDays + 14
			contract.EndsInDays = contract.StartsInDays + 365
			// Cancelling is an EVENT, not a status: notice was given, and it
			// takes effect after the notice period.
			//
			// The effective date must land INSIDE the term. The product
			// refuses a cancellation that takes effect after the term already
			// ran out (contract_cancellation_within_term), and rightly: it
			// would extend a term by ending it. Notice is given part-way
			// through, and the effect lands between notice and the end.
			notice := contract.StartsInDays + (contract.EndsInDays-contract.StartsInDays)/2
			remaining := contract.EndsInDays - notice
			contract.Cancel = &demoCancelTerms{
				NoticeInDays:    notice,
				EffectiveInDays: notice + 1 + hashIndex("effective:"+ref, maxInt(remaining, 1)),
			}
		case "draft":
			// Unsigned: no signature date, and dates that sit ahead.
			contract.StartsInDays = 14 + hashIndex("start:"+ref, 45)
			contract.EndsInDays = contract.StartsInDays + 365
		case "superseded":
			// The predecessor of a renewal. `superseded` is NOT assertable —
			// the product refuses draft→superseded outright — so the record is
			// created ACTIVE and renews_into is what supersedes it, writing
			// the successor in the same transaction. Same shape demo.json's
			// own renewal chain uses.
			successorRef := fmt.Sprintf("gen-%s-%d-successor", domain, i)
			contract.Status = "active"
			contract.SignedInDays = -(400 + hashIndex("signed:"+ref, 200))
			contract.StartsInDays = contract.SignedInDays + 14
			contract.EndsInDays = contract.StartsInDays + 365
			contract.RenewsInto = successorRef
			out = append(out, contract, demoContract{
				Ref:            successorRef,
				Company:        domain,
				Title:          generatedContractTitle(name, "renewal", i),
				ContractNumber: contract.ContractNumber + "-R1",
				ValueMinor:     value + value/10, // a renewal reprices upward
				Currency:       "EUR",
				ValueBasis:     "annualized_12m",
				Status:         "active",
				SignedInDays:   contract.EndsInDays - 20,
				StartsInDays:   contract.EndsInDays + 1,
				EndsInDays:     contract.EndsInDays + 366,
			})
			continue
		}
		out = append(out, contract)
	}
	return out
}

func generatedContractTitle(company, status string, index int) string {
	switch status {
	case "draft":
		return company + " — Rahmenvertrag (Entwurf)"
	case "renewal":
		return company + " — Rahmenvertrag, Verlängerung"
	default:
		if index > 0 {
			return fmt.Sprintf("%s — Rahmenvertrag %d", company, index+1)
		}
		return company + " — Rahmenvertrag"
	}
}

// shortDomain is the domain without its TLD, for building a contract number
// that reads as belonging to somebody.
func shortDomain(domain string) string {
	name := domain
	if i := strings.Index(name, "."); i > 0 {
		name = name[:i]
	}
	name = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return r
		}
		return -1
	}, name)
	if len(name) > 4 {
		name = name[:4]
	}
	if name == "" {
		return "XXXX"
	}
	return name
}

// looseDocumentTitles is what an account document is called, per type. These
// are documents that belong to the COMPANY rather than to any contract — the
// distinction the Documents card exists to show.
var looseDocumentTitles = map[string]struct {
	Title    string
	Category string
	Body     []string
}{
	"nda": {
		Title:    "Geheimhaltungsvereinbarung",
		Category: "legal",
		Body: []string{
			"Gegenseitige Geheimhaltungsvereinbarung (NDA)",
			"Laufzeit: 3 Jahre ab Unterzeichnung",
			"Gegenstand: Austausch technischer und kaufmaennischer Informationen",
		},
	},
	"price_list": {
		Title:    "Preisliste",
		Category: "other",
		Body: []string{
			"Preisliste, gueltig fuer das laufende Geschaeftsjahr",
			"Alle Preise netto zzgl. gesetzlicher Umsatzsteuer",
			"Staffelrabatte ab 50 Lizenzen auf Anfrage",
		},
	},
	"dpa": {
		Title:    "Auftragsverarbeitungsvertrag",
		Category: "legal",
		Body: []string{
			"Auftragsverarbeitungsvertrag nach Art. 28 DSGVO",
			"Technische und organisatorische Massnahmen als Anlage 1",
			"Unterauftragsverarbeiter als Anlage 2",
		},
	},
	"order_form": {
		Title:    "Bestellformular",
		Category: "other",
		Body: []string{
			"Bestellformular fuer zusaetzliche Lizenzen",
			"Abrechnung anteilig bis zum Ende der laufenden Periode",
		},
	},
}

// seedLooseDocuments uploads the account documents a profile calls for.
//
// These carry NO contract_id, and that is the whole point: an attachment with
// one is filed under its contract, and an attachment without one belongs to
// the account and shows on the Documents card. A demo where every document
// hangs off a contract cannot show the difference.
func seedLooseDocuments(c *client, refs pipelineRefs, plan map[string]profile, mode runMode) (int, error) {
	uploaded := 0
	for _, domain := range sortedDomains(plan) {
		p := plan[domain]
		if p.Pinned || len(p.LooseDocs) == 0 {
			continue
		}
		orgID, ok := refs.orgsByDom[domain]
		if !ok {
			continue
		}
		for _, docType := range p.LooseDocs {
			spec, known := looseDocumentTitles[docType]
			if !known {
				return uploaded, fmt.Errorf("profile for %s wants document type %q, which has no template", domain, docType)
			}
			if mode == modeDryRun {
				uploaded++
				continue
			}
			company := refs.orgNameByID[orgID]
			title := company + " — " + spec.Title
			present, err := organizationHasDocument(c, orgID, title)
			if err != nil {
				return uploaded, err
			}
			if present {
				continue
			}
			page := pdfPage{
				Title: title,
				Lines: append([]string{company, ""}, spec.Body...),
			}
			filename := sanitizeFilename(docType+"-"+shortDomain(domain)) + ".pdf"
			attachmentID, err := c.upload("/v1/attachments", filename, renderPDF(page), map[string]string{
				"entity_type": "organization",
				"entity_id":   orgID,
			})
			if err != nil {
				return uploaded, fmt.Errorf("uploading %s for %s: %w", docType, domain, err)
			}
			metadata := jsonBody{
				"category":  spec.Category,
				"title":     title,
				"doc_state": "current",
			}
			if err := c.patch("/v1/attachments/"+attachmentID+"/metadata", metadata, nil); err != nil {
				return uploaded, fmt.Errorf("setting metadata on %s for %s: %w", docType, domain, err)
			}
			uploaded++
		}
	}
	return uploaded, nil
}

// organizationHasDocument reports whether this account already carries a
// document with that title, so a re-run uploads nothing twice.
func organizationHasDocument(c *client, orgID, title string) (bool, error) {
	docs, err := organizationAttachments(c, orgID)
	if err != nil {
		return false, err
	}
	for _, doc := range docs {
		if doc.Title == title {
			return true, nil
		}
	}
	return false, nil
}

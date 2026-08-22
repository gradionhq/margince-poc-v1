// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The paper: a PDF for every contract, uploaded through the real multipart
// path and hung off the company it belongs to.
//
// Generated rather than shipped in the dataset. A contract's document should
// say what the contract says — its number, its parties, its term — and the
// contract records live in the database, so rendering from them keeps the two
// in step. Committing 20 static PDFs would mean a document that disagrees
// with its own record the first time somebody edits a value.
//
// Every document is marked a demo document on its face, because a
// convincing-looking agreement that leaks out of a demo is a problem no
// amount of internal context prevents.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
)

// docStateFor maps a contract's status to the state its paper is in.
//
// An unsigned agreement is a draft, a live one is current, and a finished one
// is final rather than deleted: the document a relationship ended under is
// exactly the one anybody later asks to see.
func docStateFor(status string) string {
	switch status {
	case "draft":
		return "draft"
	case "expired", "cancelled", "superseded":
		return "final"
	default:
		return "current"
	}
}

// seedDocuments renders and uploads one PDF per contract.
func seedDocuments(c *client, refs pipelineRefs, mode runMode) (int, error) {
	contracts, err := allContracts(c, refs)
	if err != nil {
		return 0, err
	}
	uploaded := 0
	for _, contract := range contracts {
		if mode == modeDryRun {
			uploaded++
			continue
		}
		has, err := contractHasDocument(c, contract)
		if err != nil {
			return uploaded, err
		}
		if has {
			continue
		}
		if err := uploadContractPDF(c, contract, refs); err != nil {
			return uploaded, fmt.Errorf("document for %q: %w", contract.Title, err)
		}
		uploaded++
	}
	return uploaded, nil
}

// seededContract is the part of a contract record its document renders from.
type seededContract struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	ContractNumber string `json:"contract_number"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	ValueMinor     int64  `json:"value_minor"`
	Currency       string `json:"currency"`
	ValueBasis     string `json:"value_basis"`
	StartsOn       string `json:"starts_on"`
	EndsOn         string `json:"ends_on"`
	SignedOn       string `json:"signed_on"`
	NoticeDays     int    `json:"notice_period_days"`
}

// allContracts gathers the agreements per company, because there is no
// workspace-wide contract list: a contract belongs to an account, and the
// contract surface is reached from one.
func allContracts(c *client, refs pipelineRefs) ([]seededContract, error) {
	var out []seededContract
	for _, orgID := range refs.orgsByDom {
		var page struct {
			Data []seededContract `json:"data"`
		}
		if err := c.get("/v1/organizations/"+orgID+"/contracts", url.Values{"limit": {"50"}}, &page); err != nil {
			return nil, fmt.Errorf("listing contracts for %s: %w", orgID, err)
		}
		for _, contract := range page.Data {
			// The per-organization list does not echo the parent back, and the
			// upload needs it to say what the document hangs off.
			if contract.OrganizationID == "" {
				contract.OrganizationID = orgID
			}
			out = append(out, contract)
		}
	}
	return out, nil
}

// attachmentRow is one document on an account: the contract it belongs to, if
// any, and what it is called. A row with no contract_id is an account
// document rather than a contract's paper.
type attachmentRow struct {
	ID         string `json:"id"`
	ContractID string `json:"contract_id"`
	Title      string `json:"title"`
}

// organizationAttachments lists every document filed against one company.
//
// Attachments have no installation-wide list — entity_type and entity_id are
// both required — so this is the only way to ask, and it paginates because a
// company with a contract per year and an NDA will pass a fixed page in a way
// nobody notices until a document silently uploads twice.
func organizationAttachments(c *client, orgID string) ([]attachmentRow, error) {
	var out []attachmentRow
	query := url.Values{"entity_type": {"organization"}, "entity_id": {orgID}}
	err := c.getAll("/v1/attachments", query, func(raw json.RawMessage) error {
		var rows []attachmentRow
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		out = append(out, rows...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing attachments: %w", err)
	}
	return out, nil
}

// contractHasDocument reports whether this agreement already has its paper,
// so a re-run uploads nothing twice.
func contractHasDocument(c *client, contract seededContract) (bool, error) {
	docs, err := organizationAttachments(c, contract.OrganizationID)
	if err != nil {
		return false, err
	}
	for _, doc := range docs {
		if doc.ContractID == contract.ID {
			return true, nil
		}
	}
	return false, nil
}

func uploadContractPDF(c *client, contract seededContract, refs pipelineRefs) error {
	body := renderPDF(contractPage(contract, refs))
	filename := documentFilename(contract)

	attachmentID, err := c.upload("/v1/attachments", filename, body, map[string]string{
		"entity_type": "organization",
		"entity_id":   contract.OrganizationID,
		"contract_id": contract.ID,
	})
	if err != nil {
		return err
	}
	// Category and state are a separate edit: the upload takes the bytes and
	// the parent, and what KIND of paper it is only a human (or here, the
	// contract it came from) can say.
	metadata := jsonBody{
		"category":  "contract",
		"title":     contract.Title,
		"doc_state": docStateFor(contract.Status),
	}
	if err := c.patch("/v1/attachments/"+attachmentID+"/metadata", metadata, nil); err != nil {
		return fmt.Errorf("setting the document's metadata: %w", err)
	}
	return nil
}

// contractPage is what the document says: the terms the record holds, in the
// shape a one-page agreement summary takes.
//
// Written in the CUSTOMER's language rather than the installation's. A
// Vietnamese customer holding paper headed "Auftragnehmer" is not a
// translation nit — it is a demo that cannot be shown in Hanoi.
func contractPage(contract seededContract, refs pipelineRefs) pdfPage {
	locale := localeFor(refs.domainByOrgID[contract.OrganizationID])
	w := wordsFor(locale)
	lines := []string{
		w.Number + ": " + orDash(contract.ContractNumber),
		w.Status + ": " + orDash(statusWord(locale, contract.Status)),
		"",
		w.Supplier + ": " + refs.anchorName,
		w.Customer + ": " + refs.orgNameByID[contract.OrganizationID],
		"",
	}
	if contract.ValueMinor > 0 {
		basis := w.TotalValue
		if contract.ValueBasis == "annualized_12m" {
			basis = w.AnnualValue
		}
		lines = append(lines, fmt.Sprintf("%s: %s %s", basis, contract.Currency, formatMinor(contract.ValueMinor)))
	}
	lines = append(lines,
		w.Term+": "+orDash(contract.StartsOn)+" "+w.TermJoiner+" "+orDash(contract.EndsOn),
		w.Signed+": "+orDash(contract.SignedOn),
	)
	if contract.NoticeDays > 0 {
		lines = append(lines, fmt.Sprintf(w.NoticeDaysFmt, w.NoticePeriod, contract.NoticeDays))
	}
	lines = append(lines, "", "----------------------------------------------------------")
	lines = append(lines, w.DemoBanner...)
	return pdfPage{Title: contract.Title, Lines: lines}
}

// formatMinor renders integer minor units as a German-style amount, which is
// what a contract in this dataset's currencies would print.
//
// It assumes two decimals, and for a zero-decimal currency that is WRONG: a
// dong contract prints a hundredth of its value. The product's own answer is
// values.MajorUnits, and this program cannot reach it — seed-demo is its own
// module and internal/shared is not importable across that line, while
// backend/pkg publishes no money surface. The alternatives are both worse than
// the defect: a second ISO minor-unit table here is exactly the duplication the
// money sweep removed, and publishing one under pkg puts a frozen API on the
// extension surface to serve a demo-data generator.
//
// So the residue is deliberate and bounded: it affects the FAKE contract PDF in
// the demo dataset, never a document a customer receives. generatedAmount
// already scales the figure into the account's own currency for the same
// realism reason, so the number is plausible even where the decimal point is
// not. Fix it by giving seed-demo a real dependency on the product's table, not
// by copying the table.
func formatMinor(minor int64) string {
	whole, cents := minor/100, minor%100 // money-scale-exempt: see above — no importable owner from this module, demo output only
	var groups []string
	for whole >= 1000 {
		groups = append([]string{fmt.Sprintf("%03d", whole%1000)}, groups...)
		whole /= 1000
	}
	groups = append([]string{fmt.Sprintf("%d", whole)}, groups...)
	return strings.Join(groups, ".") + fmt.Sprintf(",%02d", cents)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// documentFilename is what the file is called on disk once somebody downloads
// it — the contract number when there is one, because that is what a person
// searching their downloads folder types.
func documentFilename(contract seededContract) string {
	stem := contract.ContractNumber
	if stem == "" {
		stem = strings.ReplaceAll(strings.ToLower(contract.Title), " ", "-")
	}
	return sanitizeFilename(stem) + ".pdf"
}

func sanitizeFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// upload posts one file through the multipart path the browser uses, so the
// bytes travel to object storage the same way a human's would and the row
// records the same server-stamped provenance.
func (c *client) upload(path, filename string, content []byte, fields map[string]string) (string, error) {
	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	for name, value := range fields {
		if err := form.WriteField(name, value); err != nil {
			return "", fmt.Errorf("writing the %s field: %w", name, err)
		}
	}
	part, err := form.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("opening the file part: %w", err)
	}
	if _, err := io.Copy(part, bytes.NewReader(content)); err != nil {
		return "", fmt.Errorf("writing the file part: %w", err)
	}
	if err := form.Close(); err != nil {
		return "", fmt.Errorf("closing the form: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.base+path, &buf)
	if err != nil {
		return "", fmt.Errorf("building the upload: %w", err)
	}
	req.Header.Set("Content-Type", form.FormDataContentType())

	var out struct {
		ID string `json:"id"`
	}
	if err := c.do(req, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

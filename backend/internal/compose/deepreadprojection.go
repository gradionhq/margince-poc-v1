// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/gradionhq/margince/backend/internal/modules/people"
)

func deepReadFields(fields []evidencedField) []people.DeepReadField {
	out := make([]people.DeepReadField, len(fields))
	for i, field := range fields {
		out[i] = people.DeepReadField{
			Field: field.Field, Value: field.Value, EvidenceSnippet: field.EvidenceSnippet,
			SourceURL: field.SourceURL, Confidence: field.Confidence,
		}
	}
	return out
}

func siteReadPeople(found []sitePerson) []people.SiteReadPerson {
	out := make([]people.SiteReadPerson, len(found))
	for i, person := range found {
		out[i] = people.SiteReadPerson{
			Name: person.Name, Role: person.Role, PublishedEmail: person.PublishedEmail,
			LinkedinURL: person.LinkedinURL, EvidenceSnippet: person.EvidenceSnippet, SourceURL: person.SourceURL,
		}
	}
	return out
}

func siteReadPages(pages []crawlPage) []people.SiteReadPage {
	out := make([]people.SiteReadPage, len(pages))
	for i, page := range pages {
		out[i] = people.SiteReadPage{URL: page.URL, Kind: string(page.Kind)}
	}
	return out
}

func siteReadProposalHash(fields []people.DeepReadField, facts []people.DeepReadFact, found []people.SiteReadPerson, entities []people.SiteReadLegalEntity) (string, error) {
	raw, err := json.Marshal(struct {
		Fields   []people.DeepReadField       `json:"fields"`
		Facts    []people.DeepReadFact        `json:"facts"`
		People   []people.SiteReadPerson      `json:"people"`
		Entities []people.SiteReadLegalEntity `json:"legal_entities"`
	}{Fields: fields, Facts: facts, People: found, Entities: entities})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

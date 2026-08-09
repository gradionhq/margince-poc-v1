// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package claims is the one grounding filter and the one claim vocabulary the
// generated surfaces share — the account brief, the company dossier and the
// growth-fit assessment (DOSS-FORM-1).
//
// It is one implementation on purpose. Three copies of a grounding rule is
// three chances for one of them to be lenient, and a lenient one renders a
// sentence nobody can check as though it had been checked. The rule is the
// product's whole answer to "how do I know this is true", so it has exactly one
// spelling.
//
// What lives here is the part that does not depend on WHICH surface is asking:
// the claim natures, the citation shape, and the decision to keep or drop a
// sentence given the records the assembler actually supplied. Building that
// supplied set is each surface's own job — the brief knows about deals and
// tasks, the dossier about profile fields and facts — and neither knows about
// the other's inputs.
package claims

import (
	"regexp"
	"strings"
)

// Evidence points at one record a sentence rests on. It names a record the
// READER can already open: every surface here assembles under the reader's own
// row scope, so a citation can never name a row they would be refused.
type Evidence struct {
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
}

// Sentence is one claim plus the records it was written from.
type Sentence struct {
	Text string `json:"text"`
	// What KIND of claim this is (DOSS-PARAM-1). Empty means fact, which is the
	// only permitted compatibility default — a reader forgives a wrong opinion
	// and does not forgive a wrong fact, so the two that are NOT fact are the
	// ones that must be said out loud.
	Nature   string     `json:"nature,omitempty"`
	Evidence []Evidence `json:"evidence"`
}

// idInProse matches a record id written into a sentence, spelled ONCE for every
// surface and the tests that gate them. It is the UUID shape every citable
// record carries, so a reply that pastes one anywhere in its prose is caught
// wherever in the clause it landed.
var idInProse = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// SpellsRecordID reports whether prose hands the reader a raw record id. It is
// exported because it is a claim a surface's own tests assert directly — every
// deterministic answer has to be readable prose, whichever writer produced it.
func SpellsRecordID(text string) bool {
	return idInProse.MatchString(text)
}

// Grounded reports whether one sentence may be rendered, given the records the
// assembler actually put in front of the model.
//
// A sentence is dropped WHOLE rather than trimmed. A sentence citing one real
// record and one invented one is a sentence whose claim may rest on the
// invented half, so keeping it with the good citation attached would present it
// as checked when it is not. An id in the prose is developer output whatever
// else the sentence says, and cutting the id out mid-clause leaves broken
// grammar the reader has to decode — every id the sentence needed is already in
// its evidence.
func Grounded(sentence Sentence, known map[Evidence]bool) bool {
	if strings.TrimSpace(sentence.Text) == "" || len(sentence.Evidence) == 0 {
		return false
	}
	if idInProse.MatchString(sentence.Text) {
		return false
	}
	for _, cited := range sentence.Evidence {
		// Keyed on the (kind, identity) PAIR, so a valid identity of the wrong
		// kind is still dropped.
		if !known[cited] {
			return false
		}
	}
	return true
}

// Keep filters a batch to the sentences that survive Grounded, normalises any
// nature the vocabulary does not define down to fact, and collapses repeated
// citations.
//
// An unknown or absent nature reduces to FACT rather than being forwarded: a
// surface that passed the model's own string through would render a label the
// contract does not define, and the strictest reading is the safe one.
//
// knownNature is passed in rather than fixed here because each surface derives
// it from its own contract enum — deriving beats re-spelling, and a rename
// upstream should fail to compile rather than launder a hand-typed string.
func Keep(sentences []Sentence, known map[Evidence]bool, knownNature map[string]bool, fact string) []Sentence {
	kept := make([]Sentence, 0, len(sentences))
	for _, sentence := range sentences {
		if !Grounded(sentence, known) {
			continue
		}
		if !knownNature[sentence.Nature] {
			sentence.Nature = fact
		}
		kept = append(kept, sentence)
	}
	return Dedupe(kept)
}

// Dedupe collapses repeated citations within each sentence, keeping first-seen
// order.
//
// The same record cited twice renders as two identical chips the reader cannot
// tell apart, and clicking either goes to the same place. Every writer exits
// through this, so the wire shape does not depend on which one wrote the answer.
func Dedupe(sentences []Sentence) []Sentence {
	out := make([]Sentence, 0, len(sentences))
	for _, sentence := range sentences {
		seen := make(map[Evidence]bool, len(sentence.Evidence))
		unique := make([]Evidence, 0, len(sentence.Evidence))
		for _, cited := range sentence.Evidence {
			if seen[cited] {
				continue
			}
			seen[cited] = true
			unique = append(unique, cited)
		}
		sentence.Evidence = unique
		out = append(out, sentence)
	}
	return out
}

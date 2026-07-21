// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package costestimate

import "github.com/gradionhq/margince/backend/internal/modules/ai"

// The work-shape floor: the cold-start estimate used when a workspace has no
// ai_call history to measure a real per-unit cost from. First connect has
// neither ai_call nor capture_backfill history, so most previews start here —
// which is why the floor derives from the actual prompt templates rather than a
// single magic per-message constant.
//
// The prompt-shape constants below are COPIED from their compose owners
// (captureclassify.go, captureenrich.go), not imported: costestimate is a
// subpackage of package compose, so importing the parent would form an import
// cycle the moment B6 wires this estimator from compose. Each constant cites its
// source; if the source pin moves, these copies must move with it.
const (
	// classifyBodyLimit mirrors compose/captureclassify.go's AIRT-PARAM-35 body
	// truncation: each classified message's body is cut to this many characters
	// before it enters the batch prompt.
	classifyBodyLimit = 1500
	// classifyBatchSize mirrors compose/captureclassify.go's AIRT-PARAM-35 batch
	// pin: ten messages per model call, so the batch's system+schema overhead is
	// amortized across ten messages.
	classifyBatchSize = 10
	// signatureLineCount mirrors compose/captureenrich.go's §2.9 input pin: the
	// trailing non-quoted signature lines fed to the per-person enrich prompt.
	signatureLineCount = 15

	// charsPerToken is the ~4-chars-per-token rule of thumb for the GPT/Gemini
	// family tokenizers this repo's cloud tiers run on — the labeled
	// approximation (recorded in ADR-0068) that turns a character-length prompt
	// shape into a token mean. A real ai_call history supersedes it the instant
	// one exists.
	charsPerToken = 4

	// The per-message classify shape: the truncated body in tokens, the batch
	// system/schema prompt amortized per message, and one short verdict out.
	classifySystemTokens  = 160 // classifySystem prompt (~640 chars) in tokens
	classifyVerdictTokens = 8   // one {id,label,confidence} verdict per message

	// The per-person enrich shape: the signature lines plus the extraction
	// prompt in, a small field bundle out.
	signatureLineTokens = 12  // mean tokens per trailing signature line
	enrichSystemTokens  = 120 // signatureEnrichSystem prompt (~480 chars) in tokens
	enrichFieldsTokens  = 40  // the extracted field bundle out

	// The per-entity embeddings shape: input-only (no output, no cache). Unlike
	// classify — which truncates each body to classifyBodyLimit (1500 chars)
	// BEFORE prompting — the embed lane embeds the entity's FULL text (for an
	// activity, concat_ws(' ', subject, body); search/embedgen.go, no truncation
	// bound in search/embedding.go's UpsertEmbedding). So the classifier's
	// 1500-char cut under-counts the embedded size. This floor uses a
	// representative full email instead: a short subject plus a realistic body,
	// in chars, converted at charsPerToken. Deliberately bounded (not a worst
	// case) — a real ai_call history supersedes it the instant one exists.
	embedSubjectChars = 60   // a representative email subject line
	embedBodyChars    = 2000 // a representative full email body (untruncated, unlike classify)
	embedItemTokens   = (embedSubjectChars + embedBodyChars) / charsPerToken
)

// defaultPersonsPerMsg is the cold-start sender density — distinct new
// correspondents worth enriching per captured message — used ONLY when a
// connection has no completed backfill to measure its own people/scanned ratio.
// It is the single honest heuristic constant this package carries.
//
// Derivation (not a magic literal): one classify batch of classifyBatchSize
// (=10) captured messages introduces on the order of one new correspondent, so
// ~0.1 persons per message — a deliberately conservative floor. A real backfill
// yield replaces it the moment one run completes.
const defaultPersonsPerMsg = 1.0 / classifyBatchSize

// workShapeFloor returns the per-UNIT token means for one task's floor estimate,
// derived from the real prompt shape above and held in backfillUnitRules.
// Deterministic and non-zero for every backfill task; a non-backfill task (e.g.
// summarize) carries no rule and so returns the zero Usage rather than a
// fabricated floor.
func workShapeFloor(task ai.Task) ai.Usage {
	return backfillUnitRules[task].floor
}

// unitsFloor is the built-in volume ratio used when a connection has no
// completed capture_backfill run to measure real yields from: classify fires ≈
// once per scanned message (captured ≈ scanned at connect), enrich once per
// expected new correspondent, and embeddings once per embedded ENTITY — a vector
// per captured message PLUS one per new person.
func unitsFloor(task ai.Task, scanned int64) int64 {
	switch task {
	case ai.TaskEnrich:
		return int64(float64(scanned) * defaultPersonsPerMsg)
	case ai.TaskEmbeddings:
		// The embed lane embeds one vector per entity: every captured message AND
		// every new person the enrich pass introduces (the observed path in
		// rules.go counts captured + people + orgs). The cold-start floor mirrors
		// that — the message floor plus the expected-person floor — so a
		// first-connect embed estimate is not systematically low by omitting the
		// person embeds. Organizations are left out: there is no defensible
		// org-density floor constant, so their (smaller) embed volume stays a
		// known, conservative underestimate rather than an invented number.
		persons := int64(float64(scanned) * defaultPersonsPerMsg)
		return scanned + persons
	default:
		// classify: captured ≈ scanned at first connect, one call per message.
		return scanned
	}
}

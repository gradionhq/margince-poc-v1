// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

// The certification stamp: what a record says it was scored against. It lives
// beside the runner rather than inside it because it drives nothing — it is a
// pure digest over the corpus a run consumed and the requests that corpus builds,
// and staleness is read off it long after that run is over.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// PromptVersion is a task's certification stamp: a digest of the exact SCENARIOS
// a run was scored against, and of the REQUESTS this build's own code builds
// from them.
//
// It is both halves because a record claims to describe what ships, and either
// half moving breaks that claim.
//
// The scenario is digested WHOLE. The rubric is read to the grader, the expected
// answer decides what "right" means, and the caps and bands decide what passes —
// each of them changes what a score means, and none of them reaches a request.
//
// The request is digested because the corpus does not hold it: a scenario
// carries the data a site is GIVEN, and the site's own code turns that into the
// prompt the model sees. A stamp over the scenario alone would leave every
// prompt in the product free to change under a record still claiming to certify
// it — the failure a fixture corpus otherwise reintroduces.
//
// What is digested is the FIRST request each case issues, built by driving the
// same Prepare/Run a paid run drives, so the stamp cannot be a second
// description of the request kept in sync by hand. The first request is the one
// a multi-call site builds before any reply exists, which is what makes it a
// pure function of the fixture and the code rather than of what a model said.
// Everything a call mints for itself is canonicalised away — the data boundary
// and the record ids a prompt identifies its data by — so the stamp moves when
// the wording moves and stays put when only one call's own identifiers do.
func PromptVersion(ctx context.Context, scenarios []Scenario, census *aitasks.Registry) (string, error) {
	if census == nil {
		return "", fmt.Errorf("aicert: stamp: no census supplied — a stamp covers the request each site's own code builds, and only the census says which case builds it")
	}
	ordered := make([]string, 0, len(scenarios))
	for _, sc := range scenarios {
		// Hash each scenario on its own, then order the digests: joining raw
		// fields would let text shift across a separator and collide.
		encoded, err := json.Marshal(sc)
		if err != nil {
			return "", fmt.Errorf("aicert: stamp: scenario %q cannot be digested: %w", sc.Name, err)
		}
		request, err := builtRequestDigest(ctx, sc, census)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(encoded)
		// Both halves are fixed-width hex, so concatenating them is unambiguous.
		ordered = append(ordered, hex.EncodeToString(sum[:])+request)
	}
	sort.Strings(ordered)
	sum := sha256.Sum256([]byte(strings.Join(ordered, "")))
	return "p" + hex.EncodeToString(sum[:16]), nil
}

// stampReply is what the stamp's completer answers with. No case reads it for
// content: the stamp is taken from the request a case builds BEFORE any reply,
// and a case that goes on to refuse this text has already built it.
const stampReply = "(a reply the stamp does not read)"

// stampCompleter is the model a case is driven through to have it build its
// request. It answers from memory — no router, no network, no spend — and keeps
// the first request it was handed, which is the one the stamp covers.
//
// The request is taken here rather than from the returned Trace because a case
// that cannot finish on stampReply may still return before filling one in, and
// the request it already issued is a fact either way.
type stampCompleter struct {
	first     model.Request
	firstSeen bool
}

func (c *stampCompleter) Complete(_ context.Context, req model.Request) (model.Response, error) {
	if !c.firstSeen {
		c.first, c.firstSeen = req, true
	}
	return model.Response{Text: stampReply}, nil
}

// builtRequestDigest is the hex digest of the first request sc's site builds
// from it. A site whose case cannot be prepared, or which reaches a reply
// without ever building a request, has nothing to stamp and is refused: a
// silently empty half would let the product's own code drift under a stamp that
// still matched.
func builtRequestDigest(ctx context.Context, sc Scenario, census *aitasks.Registry) (string, error) {
	factory, bound := census.CaseFor(ai.Task(sc.Task), sc.Site)
	if !bound {
		return "", fmt.Errorf("aicert: stamp: scenario %q names site %s/%s, which binds no certification case", sc.Name, sc.Task, sc.Site)
	}
	prepared, err := factory.Prepare(json.RawMessage(sc.Fixture), json.RawMessage(sc.Expect.Answer))
	if err != nil {
		return "", fmt.Errorf("aicert: stamp: scenario %q: preparing the case: %w", sc.Name, err)
	}
	completer := &stampCompleter{}
	// Whether the case REACHES a usable reply is not this function's
	// question, and stampReply is not one: a run that ends in the site's own
	// refusal still built the request that refusal came from. The only failure
	// the stamp can have is a case that built no request at all, which is what
	// the check below names — carrying the run's own error when there is one.
	_, runErr := prepared.Run(ctx, completer)
	if !completer.firstSeen {
		if runErr != nil {
			return "", fmt.Errorf("aicert: stamp: scenario %q: the case built no request to stamp: %w", sc.Name, runErr)
		}
		return "", fmt.Errorf("aicert: stamp: scenario %q: the case completed without building a request, so nothing it sends can be stamped", sc.Name)
	}
	return canonicalRequestDigest(completer.first)
}

// perCallID matches a canonical UUID, which is the shape of every identifier
// this codebase mints for one call: the row ids a prompt tells the model to
// answer per ("classify each by its id"), and the nonce inside a data-boundary
// marker. A hand-written prompt carries none — they arrive from ids.NewV7 —
// so replacing them is what makes two sends of the SAME prompt hash alike.
var perCallID = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// canonicalID stands in for one. It is deliberately not UUID-shaped, so a
// placeholder can never be read back as an identifier.
const canonicalID = "per-call-id"

// canonicalRequestDigest hashes what the model is shown — the system prompt, the
// messages, the tools and answer schema it must obey, and the answer ceiling —
// and nothing about the call that carried it. The served model, the workspace,
// and the credentials are a binding, not a prompt: they belong to the record's
// own identity fields, and folding them in here would make every record stale on
// a routing change that altered no wording.
//
// The data boundary is canonicalised the way the router canonicalises it for a
// cache key — through promptfence, which replaces only the marker the SYSTEM
// prompt declares, so captured text can neither choose what is treated as a
// boundary nor make two different payloads hash alike. The per-call ids are then
// swept from the whole material: a marker's nonce would fall to that sweep as
// well, but only because a marker happens to carry a UUID today, and the
// boundary's own canonicalisation is promptfence's to define, not this file's to
// infer from its spelling.
func canonicalRequestDigest(req model.Request) (string, error) {
	declaring := req.System
	messages := make([]model.Message, len(req.Messages))
	for i, m := range req.Messages {
		m.Content = promptfence.Canonicalize(declaring, m.Content)
		messages[i] = m
	}
	material, err := json.Marshal(struct {
		System         string          `json:"system"`
		Messages       []model.Message `json:"messages"`
		Tools          []model.ToolDef `json:"tools"`
		MaxTokens      int             `json:"max_tokens"`
		ResponseSchema json.RawMessage `json:"response_schema"`
	}{
		System:         promptfence.Canonicalize(declaring, declaring),
		Messages:       messages,
		Tools:          req.Tools,
		MaxTokens:      req.MaxTokens,
		ResponseSchema: req.ResponseSchema,
	})
	if err != nil {
		return "", fmt.Errorf("aicert: stamp: the built request cannot be digested: %w", err)
	}
	sum := sha256.Sum256(perCallID.ReplaceAll(material, []byte(canonicalID)))
	return hex.EncodeToString(sum[:]), nil
}

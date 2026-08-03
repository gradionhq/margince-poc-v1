// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// What a refused tool call says back to the model, and what it may not.
//
// A tool result is read by the model that wrote the arguments, and an agent
// run's transcript is CUMULATIVE — every later prompt of the run carries this
// text. So a refusal here has two obligations the REST twin does not: it must
// name only what the caller authored or what we authored ourselves, never the
// program in between (T2), and the caller-authored half must be bounded and
// escaped, because its author is the same model the text is fed back to.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// decodeArgs is the surface's input validation: strict JSON — unknown argument
// names are errors rather than silent drops, and the arguments are exactly ONE
// JSON value rather than the first of several.
//
// The unknown-key half is datasource.RejectNonCanonicalKeys, the same gate the
// REST body decode and the provider seam apply, for two reasons. It refuses a
// key that only case-FOLDS onto an argument, which encoding/json would otherwise
// accept — so `{"LIMIT":1}` is a field patch on no surface rather than on this
// one alone. And its refusal is a TYPE (datasource.UnknownFieldError), which is
// what lets the refusal below keep the caller's own key while masking the
// decoder's words about this program; encoding/json's own unknown-field message
// is prose only a string match could recognise.
//
// It does NOT settle whether a required uuid argument was supplied: `ids.UUID`
// zero-values an absent key without erroring, so that claim is made once for the
// whole surface at Registry.Invoke (requireDeclaredIDs) rather than in each
// handler — which is how thirteen handlers came to miss it.
func decodeArgs[T any](in json.RawMessage, into *T) error {
	if unknown := datasource.RejectNonCanonicalKeys(in, into); unknown != nil {
		// OURS, and it quotes the caller's own argument names and nothing else —
		// the most actionable thing this surface says, so it travels verbatim.
		return &BadArgsError{Cause: unknown}
	}
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		// Everything left is encoding/json or a value unmarshaler underneath it
		// describing THIS program — the Go struct being filled, the Go type of a
		// field, a library's `invalid UUID length: 6` — which an agent can
		// neither act on nor is entitled to read, and which lands in a run's
		// cumulative transcript. A shape we can name is restated as the argument
		// plus the form it takes; one we cannot is masked, and the words withheld
		// go to the operator's log instead of nowhere.
		safe, withheld := httperr.SafeDecodeError(err)
		if withheld {
			slog.Warn("unnamed tool-argument decode failure", "err", err)
		}
		return &BadArgsError{Cause: safe}
	}
	if dec.More() {
		// One value, the same boundary the REST body decode draws
		// (httperr.Decode): decoding the first and discarding the rest would
		// take a caller's second value — a correction, a merged retry — without
		// saying that anything was dropped, and a silent truncation is the one
		// refusal shape an agent cannot re-plan around. OURS, so it travels
		// verbatim.
		return &BadArgsError{
			Cause:    errors.New("trailing content after the first JSON value"),
			Guidance: "send exactly one JSON object carrying this tool's arguments",
		}
	}
	return nil
}

// maxBadArgsDetail bounds what a rejected tool call may say back. The
// unknown-key refusal quotes the caller's own argument names verbatim,
// the refusal becomes an observation, and an agent run's transcript is
// cumulative — so an unbounded message is an unbounded write into every
// later prompt of that run, by the one author that has already been shown
// the fence marker. The tool NAME is bounded for exactly this reason
// (runner.maxToolNameLen); this is the other field a model chooses freely.
// Long enough to name the offending key and what was wanted, short enough
// that the field cannot carry prose.
const maxBadArgsDetail = 200

// BadArgsError maps to a tool-call validation failure.
//
// The two members have opposite provenance, and that is the whole reason they
// are separate. Cause may quote the CALLER — the key refusal echoes the argument
// name it refused — so it is bounded and escaped. Guidance is OURS: a fixed
// vocabulary reflected off the contract, chosen by no caller.
type BadArgsError struct {
	Cause error
	// Guidance is server-authored text appended after the echo, and it is NOT
	// bounded. Bounding it with the echo is what made the accepted-field list
	// truncate mid-word on a long unknown key — cutting away the list the
	// message exists to teach, exactly when the caller most needed it. The
	// bound guards against an unbounded write into a run's transcript by the
	// model being prompted; our own strings were never that.
	Guidance string
}

func (e *BadArgsError) Error() string {
	msg := "arguments: " + echoSafe(e.Cause.Error(), maxBadArgsDetail)
	if e.Guidance == "" {
		return msg
	}
	return msg + "; " + e.Guidance
}
func (e *BadArgsError) Unwrap() error { return e.Cause }

// boundDetail caps a message at n bytes, cutting on a rune boundary so the
// result stays valid UTF-8 rather than ending mid-sequence.
func boundDetail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// echoSafe prepares caller-authored text for a tool result: bounded, and with
// every control character rendered as a visible escape.
//
// Bounding alone is not enough. A tool result lands in a transcript that later
// prompts of the same run read, and the author of these strings is the model
// being prompted — so a newline in a field name can open what reads as a new
// line of conversation, and an escape byte can move a terminal's cursor.
// Rendering them keeps what the caller actually wrote while taking away its
// ability to forge the frame around it.
func echoSafe(s string, n int) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return boundDetail(b.String(), n)
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension

// A unit's own vocabulary for the ways its background work fails, and the
// wrapper a job returns to speak it.
//
// THE PROBLEM THIS SOLVES is that a job failure reaches an operator through
// river_job.errors, a column with no workspace and so no RLS, which every
// workspace's admin reads. The job layer therefore refuses to persist a cause's
// own text and substitutes a sentence from a closed vocabulary — otherwise a
// provider that named the phone number it refused would have published it,
// fleet-wide, for as long as River retains the row.
//
// The cost was that a unit's classification did not survive the trip. zalo-oa
// computes `provider_unavailable` versus `token_rejected` versus
// `package_too_low` — three failures with three different people to go fix them
// — writes the class to its own connection row, and then returned a plain error
// the job layer could only report as unclassifiable. An operator reading
// Maintenance was told to go read a log, with no key to find the line by.
//
// So the vocabulary grows a COMPOSED HALF rather than the redaction being
// relaxed. A class is a token and two fixed sentences the unit WROTE, declared
// as inert data next to its tools and its jobs; nothing a remote party said can
// reach the column through it, because nothing a remote party said is in it.
//
// A NAME IS NOT A DISPOSITION, which is the second thing this file owns. Failure
// and Reschedule carry the same class and ask for opposite outcomes — one spends
// the job's attempts and becomes dead work an operator is shown, the other
// reschedules the same tick and shows nobody anything. A unit chooses between
// them by whether the failure needs a human at all, and a provider nobody can
// reach is the case that needs none.

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// failureClassGrammar is the class-token rule: lower snake_case, the same shape
// a job name takes. It is the token an operator sees on a screen and greps a log
// for, and it lands next to `last_error_class` on a unit's own rows — one
// spelling for one failure, or the two surfaces describe the same outage in two
// vocabularies.
var failureClassGrammar = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

// maxFailureClassLength bounds the token. It is compared by exact string match
// on a hot read path and rendered inside a fixed-width badge; a class longer
// than this is a sentence wearing a token's clothes, and Sentence is the field
// for that.
const maxFailureClassLength = 48

// maxFailureSentenceLength bounds each sentence. river_job.errors is retained
// per River's own schedule and read by every workspace's admin, so what a unit
// may write into it is capped: a unit that needs more words than this to say
// what happened is describing several failures and owes them separate classes.
const maxFailureSentenceLength = 200

// FailureClass is one named way a unit's background job can fail, in the unit's
// own vocabulary, with what an operator does about it.
//
// All three fields are FIXED STRINGS the unit authored. That is the whole
// security argument: this value is what reaches an unscoped, fleet-visible
// column, and it can carry nothing a provider, a caller or a record supplied
// because it is not built from any of them. A class is chosen by what the cause
// IS; the cause's own text stays in the process log, where the audience and the
// retention are the operator's own.
type FailureClass struct {
	// Class is the unit's token for this failure, lower snake_case. It is the
	// stable identifier: a screen filters on it, an alert matches it, and a unit
	// that also records a class on its own rows should use the SAME token here
	// so an operator comparing the two screens is reading one fact.
	Class string
	// Sentence says WHAT WENT WRONG, in a form an operator who does not know the
	// provider can read. It is not the provider's message and must not quote one
	// — a remote party's prose is not this installation's to publish.
	Sentence string
	// Remedy says WHAT TO DO, which is the half a failure list is useless
	// without. "The provider was unreachable" and "check the installation's
	// network reach to the provider; the poll catches up on its own" are
	// different pieces of information, and an operator reading a dead job at
	// 2am needs the second one.
	//
	// It is REQUIRED rather than optional. A class whose author could not say
	// what to do about it has not finished classifying: either the failure needs
	// no intervention, which is itself the remedy to write, or it is the
	// catch-all class and the remedy is where to look next.
	Remedy string
}

// Validate enforces the token grammar and the two sentences' presence and
// bounds. It does NOT check that a sentence reads well, which is a review
// concern; it checks the properties that make the value safe to persist into a
// column nothing else scopes.
func (f FailureClass) Validate() error {
	if !failureClassGrammar.MatchString(f.Class) {
		return fmt.Errorf("failure class %q is not a valid class token (lower snake_case, e.g. provider_unavailable)", f.Class)
	}
	if len(f.Class) > maxFailureClassLength {
		return fmt.Errorf("failure class %q is %d characters — a class is a token an operator greps and a screen badges, so it is capped at %d", f.Class, len(f.Class), maxFailureClassLength)
	}
	if err := validateFailureSentence(f.Class, "sentence", f.Sentence); err != nil {
		return err
	}
	return validateFailureSentence(f.Class, "remedy", f.Remedy)
}

// validateFailureSentence holds both sentences to the same rule, so a unit
// cannot supply a remedy the sentence check would have refused.
//
// The newline refusal is not cosmetic. These strings are rendered in a job
// failure list and stored in a JSON error column; a multi-line value breaks the
// one-failure-one-line reading the list depends on, and there is no failure that
// needs a paragraph to name.
func validateFailureSentence(class, field, s string) error {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return fmt.Errorf("failure class %q declares no %s — a class carries what happened AND what to do, or an operator reading it has no next step", class, field)
	}
	if trimmed != s {
		return fmt.Errorf("failure class %q has surrounding whitespace in its %s — the value is compared by exact match on read, so it is stored exactly as declared", class, field)
	}
	if strings.ContainsAny(s, "\r\n") {
		return fmt.Errorf("failure class %q has a line break in its %s — a failure renders as one line in a bounded list", class, field)
	}
	// EVERY NON-PRINTING RUNE IS REFUSED, and this is a security check rather
	// than a tidiness one.
	//
	// The registration that stops a unit claiming a core sentence or one of the
	// substitutes compares strings exactly, so a sentence differing from a core
	// one by a single invisible codepoint is not a collision to that check — and
	// it renders to an operator as the sentence it copied, now carrying a class
	// the real one can never carry. A non-breaking space is the realistic way in:
	// copying a sentence out of rendered prose to use as a template brings one
	// along, the author sees their own edit, and the gate sees a different string.
	//
	// unicode.IsPrint admits the ASCII space and no other space separator, and no
	// format or control rune at all, so one test closes NBSP, the zero-width
	// spaces, the bidi overrides and an ANSI escape sequence together. The last of
	// those matters for a second reason: these strings reach an operator's
	// terminal through psql and the process log unescaped.
	//
	// WHAT IT DOES NOT CLOSE, said plainly rather than left to be discovered: a
	// homoglyph is printable, so a Cyrillic lookalike still differs by bytes while
	// reading identically. Refusing that needs normalisation and folding, and it
	// only defends against a unit doing it deliberately — which is outside this
	// tier's threat model, where every composed unit is reviewed and a hostile one
	// has better roads than a spoofed sentence.
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("failure class %q has a non-printing character (%U) in its %s — the sentence is compared byte-for-byte against the core vocabulary, so an invisible rune would let it impersonate a sentence it must not claim", class, r, field)
		}
	}
	if utf8.RuneCountInString(s) > maxFailureSentenceLength {
		return fmt.Errorf("failure class %q has a %d-character %s — what a unit may publish into the fleet-visible error column is capped at %d, and a longer one is several failures owing several classes", class, utf8.RuneCountInString(s), field, maxFailureSentenceLength)
	}
	return nil
}

// ValidateFailureClasses holds one unit's declared set: every class valid, and
// no token declared twice.
//
// The duplicate check is the load-bearing one. Two entries under one token would
// resolve by map order at boot, so an operator would read one of two sentences
// for the same failure depending on which registration won — and the pair that
// disagree is exactly the pair somebody wrote deliberately.
func ValidateFailureClasses(classes []FailureClass) error {
	seen := make(map[string]struct{}, len(classes))
	for _, c := range classes {
		if err := c.Validate(); err != nil {
			return err
		}
		if _, dup := seen[c.Class]; dup {
			return fmt.Errorf("failure class %q is declared twice — one token names one failure", c.Class)
		}
		seen[c.Class] = struct{}{}
	}
	return nil
}

// Failure marks a cause as one of the declaring unit's DECLARED failure classes.
//
// A job returns this instead of a bare error so the job layer can find the
// classification the unit already computed. The cause travels underneath and
// stays reachable through errors.Is — the whole point of the job layer's fault
// type is that classification keeps working while the persisted text is fixed —
// so a caller wrapping a sentinel does not lose it here.
//
// It takes the CLASS VALUE rather than its token so that the value a unit hands
// over is the value the boot validated — the unit passes the same package-level
// value it declared, and the job layer compares what arrives against what was
// registered. A token alone would have made the two impossible to compare: the
// sentence is the field that reaches the operator, and a lookup by token would
// have accepted any sentence travelling under a declared name.
//
// Nothing here checks that the class is one the unit declared. It cannot: this is
// a plain constructor holding no view of the composed set, and a unit's tick is
// the last place that should discover a typo by panicking.
//
// SO THE JOB LAYER CHECKS BEFORE IT PUBLISHES, and that check is load-bearing
// rather than belt-and-braces. This constructor accepts ANY value — including one
// whose Sentence a unit formatted out of the cause, which is the accident the
// whole seam exists to prevent — so the sentence is persisted only when the
// installation registered exactly this value, all three fields, for the kind that
// is failing. An unregistered one falls through to the core sentinel vocabulary,
// and persists the unclassified substitute only when nothing there matches either;
// the cause goes to the log in both cases. Declaring a class is what makes it
// publishable, so forgetting to declare one costs the operator this unit's own
// wording and never a classification the failure would have had regardless.
//
// A nil cause answers nil: there is no failure to classify, and a wrapper that
// invented one would turn a successful tick into a dead row.
func Failure(class FailureClass, cause error) error {
	if cause == nil {
		return nil
	}
	return &classifiedFailure{class: class, cause: cause}
}

// Reschedule marks a cause as a declared class whose right disposition is to RUN
// THIS TICK AGAIN LATER rather than to fail.
//
// A provider that cannot be reached is the case it exists for, and the disposition
// matters as much as the classification does. Failing spends the child's attempts
// and discards the row, which at a poll's cadence manufactures one piece of dead
// work per tick for the length of an outage — each of them raising a banner that
// says the work will not happen without intervention, about work that needs none.
// The cursor did not move, so the next reachable tick walks the same region and
// nothing is lost.
//
// A UNIT CANNOT SPELL THIS ITSELF. The queue's own postponement is River's
// JobSnooze, and an extension unit is a separate module that may import only the
// allowlisted pkg/extension surface — so this is the tier-safe way to ask for it,
// and the job seam is what turns the request into the queue's own control return.
//
// `in` is how long to wait. It is a REQUEST like the class is: the seam bounds it
// before the queue sees it, because a tick that scheduled itself a year out would
// have removed its own work from every screen that shows live jobs without
// failing anything an operator could find.
//
// The class travels exactly as Failure's does, and is subject to the same rule:
// only a class this installation registered for the failing kind is honoured. A
// unit that forgot to declare one gets the ordinary failure path, which is the
// disposition it had before this existed.
//
// A nil cause answers nil, for the reason Failure's does: there is no failure to
// reschedule, and inventing one would postpone a tick that succeeded.
func Reschedule(class FailureClass, in time.Duration, cause error) error {
	if cause == nil {
		return nil
	}
	return &classifiedFailure{class: class, cause: cause, retryIn: in, reschedule: true}
}

// RescheduleAfter reports the delay a failure asked to be retried after, and
// whether it asked at all.
//
// It reads through wrapping for the reason FailureClassOf does — a tick adds
// context on the way out of a call stack — and it answers about the SAME
// outermost value FailureClassOf answers about, so a caller cannot honour one
// value's class and another value's disposition.
func RescheduleAfter(err error) (time.Duration, bool) {
	var classified *classifiedFailure
	if errors.As(err, &classified) && classified.reschedule {
		return classified.retryIn, true
	}
	return 0, false
}

// classifiedFailure carries the unit's class alongside its cause.
//
// Error() names the class token and the cause, which is what a process LOG
// should say — this string is not what gets persisted. The job layer replaces it
// with the class's declared sentence before River ever sees it, exactly as it
// replaces every other cause, and that substitution is why the cause's own text
// staying reachable here is safe.
type classifiedFailure struct {
	class FailureClass
	cause error
	// reschedule and retryIn are the DISPOSITION half, set only by Reschedule.
	// They are separate fields rather than a sentinel duration because zero is a
	// delay a unit may legitimately ask for — "as soon as the queue will have me"
	// — and reading it as "did not ask" would turn a postponement into a dead row
	// at exactly the moment the unit was being most explicit.
	reschedule bool
	retryIn    time.Duration
}

func (f *classifiedFailure) Error() string {
	return fmt.Sprintf("%s: %v", f.class.Class, f.cause)
}

func (f *classifiedFailure) Unwrap() error { return f.cause }

// FailureClassOf reports the class a job's error was marked with, and whether it
// was marked at all.
//
// It reads through wrapping: a unit's tick routinely adds context on the way out
// of a call stack, and a class that only survived at the outermost layer would be
// lost by the first fmt.Errorf above it.
//
// The OUTERMOST class wins. A tick that classified twice nested a specific
// failure inside a general one, and the outer call is the one that saw the whole
// operation — the same precedence errors.As gives, for the same reason.
func FailureClassOf(err error) (FailureClass, bool) {
	var classified *classifiedFailure
	if errors.As(err, &classified) {
		return classified.class, true
	}
	return FailureClass{}, false
}

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

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
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
// It takes the CLASS VALUE rather than its token, which is what lets the job
// layer render the operator sentence without holding a registry: the sentence
// arrives with the class. The unit passes the same package-level value it
// declared, so the string an operator reads and the string the boot validated
// are one value and cannot drift into two.
//
// Nothing here checks that the class is one the unit declared. It cannot: this
// is a plain constructor holding no view of the composed set, and a unit's tick
// is the last place that should discover a typo by panicking. An undeclared class
// is refused at boot, where the whole set can still be rejected together — and if
// one reaches the wire anyway it is reported as the same unvetted substitute a
// bypassed fault gets, so the mistake degrades to today's behaviour rather than
// to a sentence nobody reviewed.
//
// A nil cause answers nil: there is no failure to classify, and a wrapper that
// invented one would turn a successful tick into a dead row.
func Failure(class FailureClass, cause error) error {
	if cause == nil {
		return nil
	}
	return &classifiedFailure{class: class, cause: cause}
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

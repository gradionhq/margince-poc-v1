// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package jobs

// The composed half of the fault vocabulary: the failure classes this
// installation's extension units declare, and the read that turns a stored
// sentence back into the class and remedy an operator acts on.
//
// fault.go is the core half. The two are deliberately the same shape — a token,
// a sentence, a remedy — because they are rendered by one surface, and an
// operator should not be able to tell from a failure list which tier classified
// it.
//
// IT LIVES BESIDE THE COMPOSED KIND TABLE, not inside it, and the reason is the
// same one composed.go gives for existing at all: an extension's declarations
// cannot be compiled into this package. What is new is only that a unit
// declares a VOCABULARY as well as a set of kinds.
//
// WHAT IS NOT NEW is the wire format. A stored failure is still nothing but a
// vetted sentence — no class prefix, no envelope — so every row written before
// this file existed still vets, and a row written by a unit still vets on a build
// that dropped it. The class is DERIVED on read from the row's kind, which is
// what makes the sentence alone unambiguous: two units may name the same failure
// in the same words, and the kind says whose failure it was.

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// FailureDetail is one classified failure as a reader renders it: what class it
// was, what happened, and what to do about it.
//
// Sentence is carried as well as the class because the caller reads it back out
// rather than re-deriving it — the stored column IS the sentence, and a reader
// that rebuilt it from the class could disagree with the row it is describing.
type FailureDetail struct {
	// Class is the vocabulary token, core or composed. It is what an alert
	// matches and what a unit records on its own rows.
	Class string
	// Sentence is what went wrong, the exact text the row stores.
	Sentence string
	// Remedy is what the operator does about it.
	Remedy string
}

// composedClasses is this process's extension failure vocabulary, keyed by River
// kind and then by sentence.
//
// KEYED BY KIND because two units are entitled to the same class token: a
// provider being unreachable is one failure with one honest name, and the second
// unit to want it should not have to invent a worse one. The kind on the row says
// which unit's vocabulary to read, so the tokens never have to be globally
// unique — only unique within a kind, which is a rule about one unit's own
// declaration and so a rule that unit can keep by itself.
//
// KEYED THEN BY SENTENCE because the sentence is what the row stores, and a read
// starts from the row. See the file header for why the wire carries no class.
//
// Written once at boot and read on every failure list; the mutex guards that
// ordering across the boot/serve boundary, as composed.go's does for kinds.
var composedClasses struct {
	mu     sync.RWMutex
	byKind map[string]map[string]extension.FailureClass
	// declared holds each kind's classes in DECLARATION ORDER, which the
	// sentence-keyed table cannot: a map has no order, and the boot report reads
	// this list to say what a unit declared in the order its author wrote it.
	declared map[string][]extension.FailureClass
}

// RegisterComposedFailureClasses records the failure vocabulary of this
// installation's composed extension jobs, one entry per River kind. The
// composition root calls it once at boot.
//
// Validate-then-apply and REPLACE-rather-than-merge, for the reasons
// RegisterComposed states for kinds: a set with one bad entry registers none, so
// a refused boot cannot leave half a unit's vocabulary declared, and a process
// has one composed set settled at boot rather than a table a later caller can
// widen.
func RegisterComposedFailureClasses(byKind map[string][]extension.FailureClass) error {
	table := make(map[string]map[string]extension.FailureClass, len(byKind))
	ordered := make(map[string][]extension.FailureClass, len(byKind))
	for kind, classes := range byKind {
		if !strings.HasPrefix(kind, extension.NamespacePrefix) {
			return fmt.Errorf("jobs: %q is not an extension kind — the composed failure vocabulary declares the %s namespace and nothing else", kind, extension.NamespacePrefix)
		}
		if err := extension.ValidateFailureClasses(classes); err != nil {
			return fmt.Errorf("jobs: kind %q: %w", kind, err)
		}
		bySentence := make(map[string]extension.FailureClass, len(classes))
		for _, c := range classes {
			if err := refuseCoreCollision(kind, c); err != nil {
				return err
			}
			if prior, dup := bySentence[c.Sentence]; dup {
				return fmt.Errorf("jobs: kind %q declares one sentence for both %q and %q — the stored row carries the sentence alone, so two classes sharing one would be indistinguishable on read", kind, prior.Class, c.Class)
			}
			bySentence[c.Sentence] = c
		}
		table[kind] = bySentence
		ordered[kind] = slices.Clone(classes)
	}
	composedClasses.mu.Lock()
	defer composedClasses.mu.Unlock()
	composedClasses.byKind = table
	composedClasses.declared = ordered
	return nil
}

// refuseCoreCollision keeps the composed half from IMPERSONATING the core half.
//
// Two impersonations matter, and neither is hypothetical — both are one
// copy-paste away from a unit author who read the core vocabulary for examples:
//
//   - A unit whose sentence equals a core sentence would be read back as the
//     CORE class, so an operator would be told a provider outage was a version
//     skew, and every alert keyed on the core class would fire on a failure that
//     is not one. The core table is matched first and cannot be shadowed, so the
//     collision is refused rather than silently lost.
//   - A unit whose sentence equals the unclassified substitute would make a real
//     classified failure present as "this could not be classified", which is the
//     one sentence on this surface that must keep meaning exactly what it says:
//     nobody has named this failure yet.
//
// The class TOKEN is checked too, and against the core tokens only. A composed
// token equal to a core one would put two different failures behind one string
// an alert matches on, and an operator filtering for it would get both.
func refuseCoreCollision(kind string, c extension.FailureClass) error {
	if c.Sentence == unrecognised {
		return fmt.Errorf("jobs: kind %q class %q declares the unclassified substitute as its sentence — that sentence must keep meaning that nobody has classified the failure", kind, c.Class)
	}
	for _, known := range vocabulary {
		if c.Sentence == known.sentence {
			return fmt.Errorf("jobs: kind %q class %q declares the sentence the core class %q already owns — a stored sentence is read back through the core table first, so this failure would report as %q", kind, c.Class, known.class, known.class)
		}
		if c.Class == known.class {
			return fmt.Errorf("jobs: kind %q declares class %q, which the core vocabulary already owns — one token names one failure, and an alert matching it would fire on both", kind, c.Class)
		}
	}
	return nil
}

// ComposedFailureClasses names the classes THIS process declares for one kind,
// in declaration order, and is empty for a kind that declared none.
//
// It exists for the boot report and the census, which answer what an
// installation composed — the same question ComposedKinds answers for kinds. A
// caller gets a copy of the values; the table is this process's declaration and
// is not editable from outside the boot that settled it.
func ComposedFailureClasses(kind string) []extension.FailureClass {
	composedClasses.mu.RLock()
	defer composedClasses.mu.RUnlock()
	return slices.Clone(composedClasses.declared[kind])
}

// ComposedFailureKinds names every kind THIS process declared a vocabulary for,
// in kind order.
func ComposedFailureKinds() []string {
	composedClasses.mu.RLock()
	defer composedClasses.mu.RUnlock()
	return slices.Sorted(maps.Keys(composedClasses.byKind))
}

// VettedFailure resolves a stored failure into the class, sentence and remedy a
// surface renders — or reports that it could not, in which case the caller
// substitutes its own fixed text and shows no class at all.
//
// THE CORE TABLE IS CONSULTED FIRST, and the composed one only for a sentence it
// does not answer. That order is what refuseCoreCollision defends, and stating it
// in both places is deliberate: the check is only correct because of this
// precedence, and the precedence is only safe because of the check.
//
// The comparison is EXACT, never a prefix or a contains, for the reason
// VettedSentence gives: a worker that bypassed the fault seam and returned its
// raw cause may have EMBEDDED a vetted sentence, and matching on the part that
// happens to line up would carry the rest of that cause — the address, the record
// — onto the wire on the strength of it.
func VettedFailure(kind, stored string) (FailureDetail, bool) {
	if stored == "" {
		return FailureDetail{}, false
	}
	for _, known := range vocabulary {
		if stored == known.sentence {
			return FailureDetail{Class: known.class, Sentence: known.sentence, Remedy: known.remedy}, true
		}
	}
	composedClasses.mu.RLock()
	defer composedClasses.mu.RUnlock()
	c, ok := composedClasses.byKind[kind][stored]
	if !ok {
		return FailureDetail{}, false
	}
	return FailureDetail{Class: c.Class, Sentence: c.Sentence, Remedy: c.Remedy}, true
}

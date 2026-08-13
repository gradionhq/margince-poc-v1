// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Reading a human's name off an email header.
//
// A counterparty arrives as at most two strings: the RFC 5322 display name, and
// the address. Neither is a name. The display name is whatever the sender's
// client was configured with — "Lienesch, André" surname-first, quotes the
// header syntax left behind, a company suffixed after a pipe — and when it is
// absent the only thing left is the local part, which is a mailbox identifier
// that HAPPENS to spell a name often enough to be worth reading.
//
// So this file answers two questions, and keeps them apart: what do we DISPLAY,
// and do we know the person's first and last name. The second is the one that
// must be allowed to say no. A local part is evidence, not a fact — `schluepmann`
// names a surname with no first name in sight, `info` names nobody at all, and
// filling first/last from either would put a guess where provenance belongs.
// Abstaining costs a null column; guessing wrong writes a person's name wrongly
// and every draft that greets them repeats it.

import (
	"strings"
	"unicode"
)

// ParsedName is what a header could be read as. First and Last are set only
// together, and only when Confident — a half-parse is not a name.
type ParsedName struct {
	// Honorific is the stripped salutation ("Dr.", "Prof."). It is deliberately
	// NOT person.title: that column holds a JOB title, and "Dr." is not one.
	Honorific string
	First     string
	Last      string
	// Full is always usable — person.full_name is NOT NULL. At worst it is the
	// cleaned-up token the parser refused to split.
	Full string
	// Confident reports whether First/Last are a real reading rather than a
	// guess. Only a confident parse may fill the split-name columns.
	Confident bool
}

// maxNameTokens bounds what reads as "first last". Three admits a middle name
// or a particle surname ("Ludwig van Beethoven"); beyond that the string is
// carrying something that is not a name — a department, a company, a title —
// and splitting it would invent a surname out of the tail.
const maxNameTokens = 3

// roleLocalParts are mailboxes an ORGANIZATION answers, not a person. They are
// the reason `mail@petereich.com` must not become a person called "mail".
//
// Capture keeps its own narrower list for a different question (transactional.go
// asks "is this machine-sent infrastructure"). This one asks "does this local
// part name a human", so it is wider — `info` and `support` are typed by real
// people all day and are still nobody's name — and it lives here because a
// module never imports a sibling.
var roleLocalParts = map[string]bool{
	"admin": true, "billing": true, "buchhaltung": true, "career": true,
	"careers": true, "contact": true, "hello": true, "help": true, "hi": true,
	"info": true, "invoice": true, "jobs": true, "kontakt": true, "mail": true,
	"marketing": true, "news": true, "newsletter": true, "office": true,
	"post": true, "presse": true, "press": true, "privacy": true, "recruiting": true,
	"sales": true, "service": true, "support": true, "team": true, "vertrieb": true,
	"welcome": true, "no-reply": true, "noreply": true, "donotreply": true,
	"do-not-reply": true, "bounce": true, "bounces": true, "mailer-daemon": true,
	"mailerdaemon": true, "postmaster": true, "notifications": true,
	"notification": true, "abuse": true, "security": true, "webmaster": true,
}

// honorifics are stripped off the front of a display name. Deliberately short:
// only forms that are unambiguously a salutation. "Ing" or "Mag" would swallow
// a real given name in some languages, so they stay out.
var honorifics = map[string]string{
	"dr": "Dr.", "dr.": "Dr.", "prof": "Prof.", "prof.": "Prof.",
	"herr": "Herr", "frau": "Frau", "mr": "Mr.", "mr.": "Mr.",
	"mrs": "Mrs.", "mrs.": "Mrs.", "ms": "Ms.", "ms.": "Ms.",
	"mx": "Mx.", "mx.": "Mx.",
}

// nameParticles stay lowercase inside a surname and never stand alone as one.
// "van der Berg" is one surname; capitalizing the particles would spell a name
// its owner does not write.
var nameParticles = map[string]bool{
	"van": true, "von": true, "der": true, "den": true, "de": true, "del": true,
	"della": true, "di": true, "da": true, "dos": true, "das": true, "le": true,
	"la": true, "du": true, "ter": true, "ten": true, "zu": true, "af": true,
	"av": true, "bin": true, "bint": true, "al": true,
}

// ParsePersonName reads the best name available from a header display name and
// an address, in that order of authority: what the sender calls themselves
// beats what their mailbox is called.
func ParsePersonName(displayName, email string) ParsedName {
	if parsed, ok := parseDisplayName(displayName); ok {
		return parsed
	}
	parsed := parseLocalPart(email)
	if parsed.Full == "" {
		// Neither header said anything usable. person.full_name is NOT NULL and
		// the caller has a record to write either way, so the address itself is
		// the last honest display string — and when even that is empty, a
		// placeholder beats failing the insert on a row we already accepted.
		if addr := strings.TrimSpace(email); addr != "" {
			parsed.Full = addr
		} else {
			parsed.Full = unknownPersonName
		}
	}
	return parsed
}

// unknownPersonName is what a counterparty with no display name and no address
// is called. It should be unreachable from mail (an activity has a sender), and
// it exists so that a path which somehow gets there writes a row a human can
// see and fix rather than failing a NOT NULL constraint deep in a transaction.
const unknownPersonName = "Unknown sender"

// parseDisplayName reads the RFC 5322 display name. It reports false when the
// header carries nothing usable, so the caller falls through to the address.
func parseDisplayName(displayName string) (ParsedName, bool) {
	name := stripQuotes(strings.TrimSpace(displayName))
	name = displayNameWithoutAffiliation(name)
	if name == "" {
		return ParsedName{}, false
	}
	name = uncommaName(name)
	honorific, rest := splitHonorific(name)
	tokens := strings.Fields(rest)
	if len(tokens) == 0 {
		// The display name was a bare honorific — "Dr." names nobody. Keep the
		// original as the display string rather than storing an empty full_name.
		return ParsedName{Full: name}, true
	}
	full := strings.Join(tokens, " ")
	parsed := ParsedName{Honorific: honorific, Full: full}
	if first, last, ok := splitFirstLast(tokens); ok {
		parsed.First, parsed.Last, parsed.Confident = first, last, true
	}
	return parsed, true
}

// parseLocalPart reads the mailbox identifier — the last evidence there is.
//
// It is far more willing to abstain than the display-name path: a local part is
// not a claim about a name, it is an address that sometimes contains one.
func parseLocalPart(email string) ParsedName {
	local, _, ok := strings.Cut(strings.TrimSpace(email), "@")
	if !ok || local == "" {
		return ParsedName{Full: strings.TrimSpace(email)}
	}
	// Plus-addressing is a routing tag the mailbox owner appended, never part
	// of their name: `anna.weber+crm@` is Anna Weber.
	if base, _, found := strings.Cut(local, "+"); found && base != "" {
		local = base
	}
	if roleLocalParts[strings.ToLower(local)] {
		return ParsedName{Full: local}
	}
	tokens := localPartTokens(local)
	if len(tokens) == 0 {
		return ParsedName{Full: local}
	}
	// One token is a surname, a handle, or a first name — the local part does
	// not say which, so it names the person without splitting them.
	if len(tokens) == 1 {
		return ParsedName{Full: titleCaseToken(tokens[0])}
	}
	cased := make([]string, 0, len(tokens))
	for _, token := range tokens {
		cased = append(cased, titleCaseToken(token))
	}
	full := strings.Join(cased, " ")
	parsed := ParsedName{Full: full}
	if first, last, ok := splitFirstLast(cased); ok {
		parsed.First, parsed.Last, parsed.Confident = first, last, true
	}
	return parsed
}

// localPartTokens splits a local part into name-shaped words: separators become
// boundaries, digits are dropped, and anything left that is not a word is
// refused. A token run like `trung314578` is a handle with a number stuck to
// it, so the digits go and `trung` remains; `2016` alone is not a name at all.
//
// The hyphen is deliberately NOT a boundary. It separates words in `anne-marie`
// exactly as it does in `first-last`, and the two are indistinguishable here —
// so it stays inside the token, where capitalizeParts still cases both halves.
// Splitting on it would turn "Anne-Marie O'Brien" into a three-token name and
// hand "Marie" to the surname.
func localPartTokens(local string) []string {
	fields := strings.FieldsFunc(local, func(r rune) bool {
		return r == '.' || r == '_'
	})
	tokens := make([]string, 0, len(fields))
	for _, field := range fields {
		word := strings.TrimFunc(field, unicode.IsDigit)
		if word == "" || !isWordLike(word) {
			continue
		}
		tokens = append(tokens, word)
	}
	if len(tokens) > maxNameTokens {
		return nil
	}
	return tokens
}

// isWordLike reports whether a token could be part of a name: letters, plus the
// apostrophes and hyphens real names carry. A token with a digit in the MIDDLE
// (`user2name`) is a handle, not a name.
func isWordLike(token string) bool {
	for _, r := range token {
		if unicode.IsLetter(r) || r == '\'' || r == '’' || r == '-' {
			continue
		}
		return false
	}
	return true
}

// splitFirstLast decides whether tokens read as a given name and a surname.
//
// A single token never does: it is a surname with no first name, or a handle.
// Particles bind to the surname that follows them, so "Ludwig van Beethoven"
// is Ludwig + "van Beethoven", not Ludwig van + Beethoven.
func splitFirstLast(tokens []string) (string, string, bool) {
	if len(tokens) < 2 || len(tokens) > maxNameTokens {
		return "", "", false
	}
	for _, token := range tokens {
		if !isWordLike(token) {
			return "", "", false
		}
	}
	// A leading particle means the string is a surname alone ("van Dijk"), and
	// a surname alone has no first name to report.
	if nameParticles[strings.ToLower(tokens[0])] {
		return "", "", false
	}
	first := tokens[0]
	last := strings.Join(tokens[1:], " ")
	if first == "" || last == "" {
		return "", "", false
	}
	return first, last, true
}

// uncommaName reverses the "Surname, Given" spelling Outlook and address books
// emit. Only ONE comma qualifies: "Weber, Anna, PhD" carries a credential the
// reversal would turn into a given name.
func uncommaName(name string) string {
	before, after, found := strings.Cut(name, ",")
	if !found || strings.Contains(after, ",") {
		return name
	}
	before, after = strings.TrimSpace(before), strings.TrimSpace(after)
	if before == "" || after == "" {
		return name
	}
	return after + " " + before
}

// splitHonorific lifts a leading salutation off the name.
func splitHonorific(name string) (string, string) {
	tokens := strings.Fields(name)
	if len(tokens) < 2 {
		return "", name
	}
	canonical, ok := honorifics[strings.ToLower(tokens[0])]
	if !ok {
		return "", name
	}
	return canonical, strings.Join(tokens[1:], " ")
}

// stripQuotes removes the quoting a header's own syntax left in the value —
// `"Lienesch, André"` is a quoted-string because it contains a comma, and the
// quotes are the encoding, not the name. Escaped inner quotes unescape.
func stripQuotes(name string) string {
	if len(name) >= 2 && strings.HasPrefix(name, `"`) && strings.HasSuffix(name, `"`) {
		name = name[1 : len(name)-1]
	}
	name = strings.ReplaceAll(name, `\"`, `"`)
	return strings.TrimSpace(name)
}

// affiliationSeparators mark where a display name stops naming the person and
// starts naming who they work for: "Sven Rittau | K5", "Tomas Vidal - TVPartner".
var affiliationSeparators = []string{" | ", " – ", " — ", " - ", " • ", " · ", " @ ", " / "}

// displayNameWithoutAffiliation keeps the part before the first affiliation
// marker. Everything after it is an employer, a team, or a tagline, and reading
// it as a surname is how "Rittau K5" gets stored as somebody's name.
//
// Distinct from domainpersonal.go's nameWithoutAffiliation, which answers a
// different question: that one feeds a SIMILARITY test, so it case-folds and
// strips accents, and it also cuts on a comma. Both would be wrong here — this
// result is STORED, and "André" must survive as "André", while a comma is the
// surname-first separator uncommaName still has to read.
func displayNameWithoutAffiliation(name string) string {
	trimmed := name
	for _, sep := range affiliationSeparators {
		if before, _, found := strings.Cut(trimmed, sep); found {
			trimmed = strings.TrimSpace(before)
		}
	}
	if trimmed == "" {
		return name
	}
	// A parenthesised aside is the same thing: "Andreas Stegmann (NFQ)".
	if before, _, found := strings.Cut(trimmed, "("); found {
		if cut := strings.TrimSpace(before); cut != "" {
			trimmed = cut
		}
	}
	return trimmed
}

// titleCaseToken capitalizes one word the way a name is written, and only when
// the word gives no opinion of its own. A token that already carries inner
// capitals is somebody's chosen spelling — McDonald, O'Brien, DeSantis — and
// rewriting it would be a correction nobody asked for.
func titleCaseToken(token string) string {
	if token == "" {
		return token
	}
	if hasInnerUpper(token) {
		return token
	}
	lower := strings.ToLower(token)
	if nameParticles[lower] {
		return lower
	}
	return capitalizeParts(lower)
}

// hasInnerUpper reports an uppercase letter after the first rune.
func hasInnerUpper(token string) bool {
	for i, r := range token {
		if i > 0 && unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

// capitalizeParts upper-cases the first letter of every hyphen- or
// apostrophe-separated part: "anne-marie" → "Anne-Marie", "o'brien" → "O'Brien".
func capitalizeParts(lower string) string {
	var out strings.Builder
	upcomingUpper := true
	for _, r := range lower {
		switch {
		case upcomingUpper && unicode.IsLetter(r):
			out.WriteRune(unicode.ToUpper(r))
			upcomingUpper = false
		case r == '-' || r == '\'' || r == '’':
			out.WriteRune(r)
			upcomingUpper = true
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

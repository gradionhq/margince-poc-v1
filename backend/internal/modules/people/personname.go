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
	// Both inputs are untrusted header text. Bidi controls are removed before
	// anything else: they are invisible, they survive every other transform,
	// and left in `full_name` they make the stored string render as a name it
	// is not: a right-to-left override before "htimS" makes it display as "Smith".
	displayName = stripBidiControls(displayName)
	if tooLongToBeAName(displayName) {
		displayName = ""
	}
	if parsed, ok := parseDisplayName(displayName); ok {
		return parsed
	}
	if tooLongToBeAName(email) {
		// An address this long is not a name to read. It still has to be
		// displayable, so it is truncated to something a human can see in a
		// list rather than dropped or stored whole.
		return ParsedName{Full: string([]rune(email)[:maxNameInputRunes])}
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
	if hasRoleToken(local) {
		return ParsedName{Full: local}
	}
	tokens, complete := localPartTokens(local)
	if len(tokens) == 0 || !complete {
		// Something in the address did not read as a word — a dropped field
		// would change WHICH human this is ("josé.maria.garcia" minus a
		// decomposed "josé" is a different person), so the whole reading is
		// refused rather than silently shortened.
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
// It reports complete=false when any field failed to read as a word. The caller
// must then abstain: dropping a field silently would rename the person.
func localPartTokens(local string) ([]string, bool) {
	fields := strings.FieldsFunc(local, func(r rune) bool {
		return r == '.' || r == '_'
	})
	tokens := make([]string, 0, len(fields))
	for _, field := range fields {
		word := strings.TrimFunc(field, unicode.IsDigit)
		if word == "" || !isWordLike(word) {
			return nil, false
		}
		tokens = append(tokens, word)
	}
	if len(tokens) > maxNameTokens {
		return nil, false
	}
	return tokens, true
}

// hasRoleToken reports whether a local part names an organizational mailbox
// rather than a human — the whole of it, or any of its dot/underscore parts.
// `support.eu@` and `sales.emea@` are the same kind of address as `support@`,
// and reading them as "Support Eu" invents two people who do not exist.
func hasRoleToken(local string) bool {
	if roleLocalParts[strings.ToLower(local)] {
		return true
	}
	for _, field := range strings.FieldsFunc(local, func(r rune) bool {
		return r == '.' || r == '_' || r == '-'
	}) {
		if roleLocalParts[strings.ToLower(field)] {
			return true
		}
	}
	return false
}

// isWordLike reports whether a token could be part of a name: letters, plus the
// apostrophes and hyphens real names carry. A token with a digit in the MIDDLE
// (`user2name`) is a handle, not a name.
func isWordLike(token string) bool {
	letters := 0
	for _, r := range token {
		switch {
		case unicode.IsLetter(r):
			letters++
		case unicode.IsMark(r):
			// A combining diacritic belongs to the letter before it: decomposed
			// "é" is e + U+0301, and refusing the mark would drop the field and
			// rename the person.
		case r == '\'' || r == '’' || r == '-':
		default:
			return false
		}
	}
	// Punctuation alone is not a word. Without this, "Alice -" reads as a
	// person whose surname is a hyphen.
	return letters > 0
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
	// A name that mixes Latin with a look-alike script is a homoglyph attempt,
	// not a multilingual name. It still DISPLAYS as given (hiding it would hide
	// the mail), but nothing derived from it may be claimed as known.
	if isMixedScriptSpoof(strings.Join(tokens, " ")) {
		return "", "", false
	}
	// Three tokens only read as first+last when the tail is a particle surname
	// ("Ludwig van Beethoven"). Otherwise the middle token is a middle name, a
	// second given name, or — in a family-name-first convention written without
	// a comma — the given name itself, and NOTHING in the string says which.
	// Two plausible readings mean the honest answer is that we do not know.
	if len(tokens) == maxNameTokens && !nameParticles[strings.ToLower(tokens[1])] {
		return "", "", false
	}
	first := tokens[0]
	last := strings.Join(tokens[1:], " ")
	if first == "" || last == "" {
		return "", "", false
	}
	return first, last, true
}

// credentialSuffixes are the post-nominals that follow a comma without making
// the string surname-first. "Anna Weber, PhD" is Anna Weber; reversing it would
// file her given name as "PhD".
var credentialSuffixes = map[string]bool{
	"phd": true, "ph.d": true, "ph.d.": true, "md": true, "m.d.": true,
	"mba": true, "msc": true, "m.sc.": true, "bsc": true, "b.sc.": true,
	"llm": true, "ll.m.": true, "jd": true, "j.d.": true, "cpa": true,
	"cfa": true, "pe": true, "esq": true, "esq.": true, "rn": true,
	"dds": true, "dvm": true, "edd": true, "psyd": true, "mpa": true, "mph": true,
	"jr": true, "jr.": true, "sr": true, "sr.": true,
	"ii": true, "iii": true, "iv": true,
	"dipl": true, "dipl.": true, "ing": true, "ing.": true,
	"mag": true, "mag.": true, "bakk": true, "bakk.": true, "msa": true,
}

// uncommaName reverses the "Surname, Given" spelling Outlook and address books
// emit — and ONLY that. Two other shapes wear the same comma and must not be
// reversed: a post-nominal credential ("Anna Weber, PhD"), and anything with a
// second comma, which is a list rather than a name.
func uncommaName(name string) string {
	before, after, found := strings.Cut(name, ",")
	if !found || strings.Contains(after, ",") {
		return name
	}
	before, after = strings.TrimSpace(before), strings.TrimSpace(after)
	if before == "" || after == "" {
		return name
	}
	// The tail is what would become the GIVEN name. A credential there means
	// the string was already "Given Surname" with a qualification appended, so
	// the honest reading is to drop the qualification and keep the order.
	if isCredentialTail(after) {
		return before
	}
	// A tail of several words is a given name at most ("Anna Maria"); more than
	// that is a title or a department the reversal would file as a person's
	// first name.
	if len(strings.Fields(after)) > 2 {
		return name
	}
	return after + " " + before
}

// isCredentialTail reports whether every word after the comma is a post-nominal.
func isCredentialTail(tail string) bool {
	fields := strings.Fields(tail)
	if len(fields) == 0 {
		return false
	}
	for _, field := range fields {
		if !credentialSuffixes[strings.ToLower(strings.TrimSuffix(field, "."))] &&
			!credentialSuffixes[strings.ToLower(field)] {
			return false
		}
	}
	return true
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

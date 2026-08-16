// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The merge-key vocabulary — which identity keys an ingress source vouches for —
// is part of the published extension surface.
//
//margince:extension-surface

package extension

// MergeKey is an identity key the person-resolution ladder resolves by. It is a
// declaration vocabulary rather than a flag, because the ladder is a ladder:
// naming the key a source vouches for says something a boolean cannot as the
// vocabulary grows.
//
// A source declares a key to CORROBORATE the human a record already names —
// never to name them. The distinction is the whole point: an address on a
// mail-shaped record IS that record's identity and needs no declaration, while
// an address riding alongside a channel identity is evidence the ladder may
// match on, and that is what a source has to vouch for.
type MergeKey string

// MergeKeyEmail is the counterparty's address, and it is the only key a captured
// record can carry today: Counterparty has no phone field, and no connector —
// core or unit — produces one. A second key is a value here plus a field on the
// record, not a different design.
//
// A fitness test outside this package holds this equal to the resolution
// ladder's own lane name, so the vocabulary cannot drift away from the ladder it
// claims to be quoting.
const MergeKeyEmail MergeKey = "email"

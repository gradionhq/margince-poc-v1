// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Store owns this module's tables (data-seam ownership, ADR-0014 Am.1);
// every write rides the storekit audit+outbox shape in one transaction.
type Store struct {
	// db binds the workspace this store runs for (ADR-0091 §9 step 3).
	db *database.DB
	// blob backs the attachment endpoints; nil in a role that stores no
	// objects, in which case the attachment handlers answer 501 rather than
	// nil-deref (WithBlobstore is how a role opts in).
	blob blobstore.Store
	// unsubscribe mints the recipient's RFC 8058 preference token for a
	// marketing send; nil means no unsubscribe header (WithUnsubscribe).
	// It lives on the STORE, not on Handlers, because the MCP send tool
	// calls the store directly — deliverability on one transport only is
	// deliverability the other transport silently drops.
	unsubscribe UnsubscribeLinker
	// publicBaseURL is the canonical scheme+host the tokenized unsubscribe
	// link resolves to — configured at boot, never taken from the request
	// (WithPublicBaseURL wires it).
	publicBaseURL string
	// signature is the sender's own sign-off, appended to every message they
	// send; nil means unsigned, which is what every role did before the
	// signature existed (WithSignature wires it).
	signature SignatureReader
	// senderName resolves the display name the From header carries; nil sends a
	// bare address (WithSenderName wires it).
	senderName SenderNameReader
	// sendAuthority pre-flights the credential behind the transport a send is
	// about to use; nil skips the pre-flight (WithSendAuthority wires it) and
	// the delivery path still refuses at transmission.
	sendAuthority SendAuthority
	// reachability answers which channel account the person behind a
	// conversation can be reached at; nil fails the channel send path CLOSED
	// (WithChannelReachability wires it), because a surface that cannot ask
	// must not send to a recipient it never resolved.
	reachability ChannelReachability
	// draftOutcome resolves the voice learning signal a served AI draft
	// opened; nil records nothing (WithDraftOutcome wires it). On the STORE
	// for the same reason unsubscribe is: the MCP send tool calls the store
	// directly, and a signal closed on one transport only is a corpus built
	// from half the sends.
	draftOutcome DraftOutcomeRecorder
	// recipients resolves an account-started send's addressees to people the
	// sender may read; nil fails that path CLOSED
	// (WithRecipientDirectory wires it). A reply never consults it — its
	// addressees come from the captured conversation it answers.
	recipients RecipientDirectory
	// heldNotifier puts a stopped scheduled message in the rep's approval
	// inbox; nil holds silently (WithHeldNotifier wires it).
	heldNotifier HeldNotifier
	// clock reads the current instant. Injected so the scheduling suites can
	// pin a due moment and a missed window without sleeping (T11).
	clock func() time.Time
}

// NewStore opens this module's store on a handle already bound to the
// workspace it serves.
func NewStore(db *database.DB) *Store {
	return &Store{db: db}
}

// WithHeldNotifier returns a store that tells a rep when their scheduled
// message stopped. It returns a copy so the base store stays unchanged.
func (s *Store) WithHeldNotifier(notifier HeldNotifier) *Store {
	clone := *s
	clone.heldNotifier = notifier
	return &clone
}

// WithClock returns a store reading time from the given function. It returns a
// copy so the base store stays unchanged.
func (s *Store) WithClock(now func() time.Time) *Store {
	clone := *s
	clone.clock = now
	return &clone
}

// now is the store's clock, defaulting to the wall clock for every caller that
// never injected one.
func (s *Store) now() time.Time {
	if s.clock == nil {
		return time.Now()
	}
	return s.clock()
}

// HasBlobstore reports whether this store can reach document bytes at all.
//
// It exists for one wiring test, and the bug it guards is invisible at runtime
// in the worst way: a store built without a blobstore answers every document
// read with "this installation stores no document bytes", which is a true
// sentence about the STORE and a false one about the installation — the bytes
// are there, and the role that was supposed to read them was assembled without
// a handle to them.
func (s *Store) HasBlobstore() bool { return s.blob != nil }

// WithBlobstore returns a store that backs the attachment endpoints with the
// given object store. It returns a copy so the base store stays unchanged.
func (s *Store) WithBlobstore(blob blobstore.Store) *Store {
	clone := *s
	clone.blob = blob
	return &clone
}

func (s *Store) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return s.db.Tx(ctx, fn)
}

// sprintf keeps SQL assembly lines readable; arguments are always
// placeholder indexes or clamped ints, never user input.
func sprintf(format string, a ...any) string { return fmt.Sprintf(format, a...) }

func uuidPtr(id *ids.UUID) *openapi_types.UUID {
	if id == nil {
		return nil
	}
	converted := openapi_types.UUID(*id)
	return &converted
}

// workspaceID types the tx-bound workspace GUC (storekit hands it out
// untyped) for the helpers that carry it as an entity parameter.
func workspaceID(ctx context.Context) ids.WorkspaceID {
	return ids.From[ids.WorkspaceKind](storekit.MustWorkspace(ctx))
}

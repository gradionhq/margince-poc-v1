// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// The governed admin entry points behind the mapping store (usermapadmin.go):
// crm.yaml's /overlay/user-map read/write pair and the /overlay/owners picker
// directory. RC-15 requires mirror_user_map to be managed from the same
// settings surface as the incumbent connection itself; without these verbs
// match_source='manual' — the escape hatch design.md §4.6 rule 4 defines for a
// reassigned or ambiguous owner email — is reachable only from the store, so a
// mis-matched or unmapped user has no remedy at all.

import (
	"context"
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ownerDirectoryCap bounds the directory one response carries. The Incumbent
// seam's Owners() is unpaginated, so this is the only place a very large
// directory can be bounded — and OwnerDirectory.Truncated reports when it
// applied, rather than letting a cut-off list imply completeness.
const ownerDirectoryCap = 500

// Why a listed user has no mapping. Derived at read time from the live
// directory rather than persisted: a stored reason column would go stale the
// moment an owner's email changed, and the admin would act on the stale one.
const (
	reasonNone         = "none"
	reasonNoEmailMatch = "no_email_match"
	reasonAmbiguous    = "ambiguous_email"
	reasonBlocked      = "blocked_by_admin"
	reasonNotYetSynced = "not_yet_synced"
	reasonNoDirectory  = "directory_unavailable"
)

// UserMapView is one UserMapEntry enriched with what an admin needs in order
// to act on it: the incumbent user's human-readable identity, why an unmapped
// user is unmapped, and whether a mapping has gone stale against the directory.
type UserMapView struct {
	UserMapEntry
	OwnerName      string
	OwnerEmail     string
	UnmappedReason string
	StaleOwnerRef  bool
}

// UserMapPage is one page of the admin mapping table.
type UserMapPage struct {
	Incumbent  string
	Entries    []UserMapView
	NextCursor string
}

// OwnerDirectory is the connected incumbent's user directory, capped at
// ownerDirectoryCap. Truncated says the cap applied.
type OwnerDirectory struct {
	Incumbent string
	Owners    []OwnerRef
	Truncated bool
}

// requireUserMapAdmin is the ONE gate every user-map operation takes, reads
// included. It demands ActionUpdate rather than ActionRead deliberately: every
// role holds overlay_connection read so a rep can see whether overlay mode is
// live (identity/internal/policy), but this surface carries every user's
// email, their incumbent mapping, and the incumbent's own directory — external
// PII no non-admin sees today. RequireHuman covers the reads that the
// contract's x-agent-access gate cannot: that gate only inspects mutating
// methods, so without this an admin-minted read-scoped passport would satisfy
// the object grant and enumerate who can see what.
func requireUserMapAdmin(ctx context.Context) error {
	if err := auth.Require(ctx, overlayConnectionObject, principal.ActionUpdate); err != nil {
		return err
	}
	return auth.RequireHuman(ctx)
}

// UserMap answers one page of this workspace's users with their mapping state
// and, for an unmapped user, why they have none.
//
// A directory read failure degrades the page rather than failing it (design.md
// §8): the mapping table is still the admin's only view of who is mapped, and
// the reasons that needed the directory go to directory_unavailable so nothing
// on the page is a guess. The failure is logged with its cause — a warning the
// admin's UI surfaces inline is not a substitute for the operator seeing why.
//
// A disconnect is NOT such a failure and is not degraded: the connection
// vanishing between the mode gate and the directory read leaves this workspace
// with no overlay at all, which is the mode_not_overlay 404 every /overlay verb
// answers — degrading it would render a settled mapping page for records the
// installation no longer mirrors.
func (s *Service) UserMap(ctx context.Context, cursor string, limit int) (UserMapPage, error) {
	if err := requireUserMapAdmin(ctx); err != nil {
		return UserMapPage{}, err
	}
	incumbent, err := s.resolveOverlayMode(ctx)
	if err != nil {
		return UserMapPage{}, err
	}
	entries, next, err := s.ms.ListUserMap(ctx, incumbent, cursor, limit)
	if err != nil {
		return UserMapPage{}, err
	}

	directory, dirErr := s.ownerDirectory(ctx, incumbent)
	if dirErr != nil {
		if errors.Is(dirErr, apperrors.ErrModeNotOverlay) || errors.Is(dirErr, ErrConnectionGone) {
			return UserMapPage{}, foldConnectionGone(dirErr)
		}
		s.log.WarnContext(ctx, "overlay user-map: reading the owners directory failed; owner identities and unmapped reasons are not derivable",
			"incumbent", incumbent, "err", dirErr)
	}
	// A TRUNCATED directory is as unusable for the absence-based derivations as
	// an unreadable one, and for the same reason: "no owner carries this email"
	// and "no owner holds this id" argue from absence, and absence from a list
	// that was cut off is not absence from the incumbent. Deriving those from it
	// would fabricate exactly the diagnoses directory_unavailable exists to
	// prevent, so the cut-off list is admitted only for the facts it can carry
	// positively: an owner that IS in it names a real owner, and userMapView
	// reads identities from it regardless.
	return UserMapPage{
		Incumbent:  incumbent,
		Entries:    userMapViews(entries, directory.Owners, dirErr == nil && !directory.Truncated),
		NextCursor: next,
	}, nil
}

// userMapViews derives each listed user's admin-facing state from the live
// directory. directoryComplete says owners is the incumbent's WHOLE directory:
// false means it is short — a failed fetch, or a list the cap cut off — so an
// owner missing from it may still exist, and "absent here" must not be reported
// as "absent at the incumbent".
func userMapViews(entries []UserMapEntry, owners []OwnerRef, directoryComplete bool) []UserMapView {
	byID := make(map[string]OwnerRef, len(owners))
	// Distinct owner ids per normalized email, never a raw occurrence count: a
	// paginated directory can list the same owner on two overlapping pages, and
	// counting that as two owners would report a legitimate single match as
	// ambiguous. This is the SAME ambiguity rule SeedUserMap seeds by
	// (usermapseed.go) — the reason shown to an admin has to be the reason the
	// sweep actually acted on, or the page explains a decision nobody made.
	ownersByEmail := make(map[string]map[string]struct{}, len(owners))
	for _, owner := range owners {
		email := normalizeEmail(owner.Email)
		if owner.ExternalID == "" {
			continue
		}
		byID[owner.ExternalID] = owner
		if email == "" {
			continue
		}
		if ownersByEmail[email] == nil {
			ownersByEmail[email] = make(map[string]struct{})
		}
		ownersByEmail[email][owner.ExternalID] = struct{}{}
	}

	views := make([]UserMapView, 0, len(entries))
	for _, entry := range entries {
		views = append(views, userMapView(entry, byID, ownersByEmail, directoryComplete))
	}
	return views
}

// userMapView derives one listed user's view. A mapped user reports the
// owner's identity (or a stale reference); an unmapped one reports the single
// most actionable reason it has none.
func userMapView(e UserMapEntry, byID map[string]OwnerRef, ownersByEmail map[string]map[string]struct{}, directoryComplete bool) UserMapView {
	v := UserMapView{UserMapEntry: e, UnmappedReason: reasonNone}
	if e.IncumbentUserID != "" {
		owner, listed := byID[e.IncumbentUserID]
		switch {
		case listed:
			v.OwnerName, v.OwnerEmail = owner.Name, owner.Email
		case directoryComplete:
			// The mapping points at an owner the incumbent no longer lists.
			// Reported, never auto-revoked: an email-sourced row in this state
			// is transient (revalidation drops it), so what survives here is a
			// human's manual override, and that stays sticky (design.md §4.6
			// rule 4).
			v.StaleOwnerRef = true
		}
		return v
	}
	switch {
	case e.Blocked:
		// A reason read out of this installation's own tables stands on its own:
		// the block comes from mirror_user_automap_block, so it is true whether
		// or not the incumbent's directory could be read, and it is the fact an
		// admin has to undo to change the outcome — it outranks both the email
		// matching and the completeness question below.
		v.UnmappedReason = reasonBlocked
	case !directoryComplete:
		// Every reason past this point argues from absence, and absence from a
		// directory we did not see whole is not absence at the incumbent — so
		// say "we could not look" rather than hand the admin a diagnosis they
		// would act on.
		v.UnmappedReason = reasonNoDirectory
	case len(ownersByEmail[normalizeEmail(e.Email)]) > 1:
		v.UnmappedReason = reasonAmbiguous
	case len(ownersByEmail[normalizeEmail(e.Email)]) == 1:
		// An owner does carry this email, so the sweep simply has not seeded
		// the pairing yet.
		v.UnmappedReason = reasonNotYetSynced
	default:
		v.UnmappedReason = reasonNoEmailMatch
	}
	return v
}

// Owners answers the connected incumbent's user directory — the population the
// mapping picker chooses from.
func (s *Service) Owners(ctx context.Context) (OwnerDirectory, error) {
	if err := requireUserMapAdmin(ctx); err != nil {
		return OwnerDirectory{}, err
	}
	incumbent, err := s.resolveOverlayMode(ctx)
	if err != nil {
		return OwnerDirectory{}, err
	}
	return s.ownerDirectory(ctx, incumbent)
}

// ownerDirectory builds a live incumbent adapter over THIS workspace's own
// connection — the same read the force-fresh resolver performs
// (compose.OverlayIncumbentResolver): the active connection names the region
// and an opaque credential ref, and the vault unseals the token behind it. A
// role wired without an incumbent factory or a vault surfaces that wiring gap
// explicitly, never a fabricated empty directory that would read as "this
// incumbent has no users".
func (s *Service) ownerDirectory(ctx context.Context, incumbent string) (OwnerDirectory, error) {
	if s.incumbent == nil || s.vault == nil {
		return OwnerDirectory{}, fmt.Errorf("overlay: no incumbent adapter is wired for this role; the %s owners directory cannot be read", incumbent)
	}
	conn, err := ActiveConnection(ctx, s.pool)
	if err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			// The mode gate passed and the connection is already gone: a
			// disconnect committed between the two reads. That is the same
			// "there is no overlay here" the whole /overlay cluster answers off
			// the mode, not a 404 that would read as "no such directory".
			return OwnerDirectory{}, apperrors.ErrModeNotOverlay
		}
		return OwnerDirectory{}, err
	}
	token, err := s.vault.Get(ctx, conn.Workspace, conn.CredentialRef)
	if err != nil {
		return OwnerDirectory{}, fmt.Errorf("overlay: unsealing the %s credential for the owners directory: %w", incumbent, err)
	}
	owners, err := s.incumbent(conn.Region, string(token)).Owners(ctx)
	if err != nil {
		return OwnerDirectory{}, fmt.Errorf("overlay: reading the %s owners directory: %w", incumbent, err)
	}
	truncated := false
	if len(owners) > ownerDirectoryCap {
		owners, truncated = owners[:ownerDirectoryCap], true
	}
	return OwnerDirectory{Incumbent: incumbent, Owners: owners, Truncated: truncated}, nil
}

// SetUserMap pins appUser to incumbentUserID as a human-vouched manual
// override, clearing any auto-map block in the same statement (the store's own
// upsert CTE), so a user is never left mapped and blocked at once.
//
// The store is fenced for this call. assertFence is a silent no-op on an
// unfenced store and the composition layer hands this service a bare one
// (compose.NewOverlayHandlers), so without WithFence a request racing a
// Disconnect would insert a mapping into a workspace whose teardown already
// purged the table. WithFence — the status-only fence — is the right strength
// for a bounded request window, matching RequestSweep.
//
// An empty incumbentUserID is refused as a row-scope miss: it names no
// incumbent user, and a mapping row carrying it would grant visibility over
// every mirrored record that has no owner at all. The transport answers 422
// for the missing field before reaching here (handlers_usermap.go); this is
// the backstop for every other caller, spelled as the same existence-hiding
// ErrNotFound the store answers for an unaddressable target.
//
// A NON-empty incumbentUserID is taken as given: the incumbent's directory is
// deliberately not consulted. An id no owner carries grants nothing — no
// mirrored record is owned by it, so recomputeForOwnerTx produces no
// visibility, and the surface renders the row as a stale mapping — whereas a
// directory read here would make every pin depend on the incumbent being
// reachable, which is precisely the moment this remedy is needed: automatic
// email matching has already failed. It is the stance the surface already
// takes for a mapping whose owner has since left the directory
// (stale_owner_ref): report it, never withhold the human's override.
func (s *Service) SetUserMap(ctx context.Context, appUser ids.UserID, incumbentUserID string) error {
	if err := requireUserMapAdmin(ctx); err != nil {
		return err
	}
	incumbent, err := s.resolveOverlayMode(ctx)
	if err != nil {
		return err
	}
	if incumbentUserID == "" {
		return fmt.Errorf("overlay: an empty incumbent user id names no incumbent user: %w", apperrors.ErrNotFound)
	}
	if err := s.ms.WithFence().SetManualUserMap(ctx, appUser, incumbent, incumbentUserID); err != nil {
		return foldConnectionGone(err)
	}
	return nil
}

// UnmapUser removes appUser's mapping, revokes the visibility grants it
// produced, and records that automatic email matching must not re-create it.
// Idempotent: unmapping an already-unmapped user still records the decision.
// Fenced for the same reason SetUserMap is.
func (s *Service) UnmapUser(ctx context.Context, appUser ids.UserID) error {
	if err := requireUserMapAdmin(ctx); err != nil {
		return err
	}
	incumbent, err := s.resolveOverlayMode(ctx)
	if err != nil {
		return err
	}
	if err := s.ms.WithFence().BlockAutoMap(ctx, appUser, incumbent); err != nil {
		return foldConnectionGone(err)
	}
	return nil
}

// foldConnectionGone translates the fence's abort signals into wire
// answers: neither is a shape a client may see. ErrConnectionGone gives
// the /overlay cluster's "this workspace has no overlay" — a request
// that lost the race with a disconnect is in exactly the state a
// request arriving one moment later would be. ErrMirrorFrozen is a
// pending flip holding the mirror still; that is a state conflict the
// caller can act on, so it says what to do about it rather than
// surfacing as an opaque 500. Every other error passes through.
func foldConnectionGone(err error) error {
	if errors.Is(err, ErrConnectionGone) {
		return apperrors.ErrModeNotOverlay
	}
	return foldMirrorFrozen(err)
}

// foldMirrorFrozen is the freeze half of the fold, shared by every wire
// boundary a fenced write can reach (the user-map service here and the
// write-back path's writePathError).
func foldMirrorFrozen(err error) error {
	if errors.Is(err, ErrMirrorFrozen) {
		return fmt.Errorf("the mirror is frozen for the overlay→native flip; it is released when the flip completes, or when a preflight finds the workspace not ready: %w", apperrors.ErrConflict)
	}
	return err
}

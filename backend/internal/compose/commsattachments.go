// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The transmit-time recheck of a delivery's attachments. It lives beside the
// send worker rather than inside it because both edges it joins cross a module
// boundary: identity owns the sender's live grants, activities owns the file.

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/comms"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// commsAttachments answers the transmit-time question about a delivery's files:
// are they all still clean, and may the SENDER still read them? Both edges
// cross a module boundary — identity owns the sender's live grants, activities
// owns the file — which is why the assembly is here rather than in comms.
type commsAttachments struct {
	authority *identity.Service
	files     *activities.Store
}

// NewSendAttachmentAuthority wires the recheck the send lane runs before it
// hands a message with files to a provider.
//
//nolint:ireturn // returns the comms.AttachmentAuthority seam by design: the concrete type is unexported and every caller holds the interface
func NewSendAttachmentAuthority(pool *pgxpool.Pool, blob blobstore.Store) comms.AttachmentAuthority {
	return commsAttachments{
		authority: identity.NewService(pool),
		// WithBlobstore is what lets ReadForSend open the objects. A worker
		// wired without one can still run the gate — that reads rows — and
		// fails at the read, which is the fault this argument exists to make
		// impossible to introduce by omission.
		files: activities.NewStore(InstallationDB(pool)).WithBlobstore(blob),
	}
}

// senderCtx rebuilds the sender's CURRENT authority on the worker's context.
//
// The worker holds no session of its own, and the delivery's own staging
// context is exactly what must not be trusted here — it recorded grants the
// sender may since have lost. So the grants are re-read now, and a sender who
// no longer resolves has no authority at all rather than empty authority.
func (a commsAttachments) senderCtx(ctx context.Context, userID ids.UserID) (context.Context, string, error) {
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return nil, "", errors.New("comms: rechecking a delivery's attachments outside workspace context")
	}
	rbac, err := a.authority.EffectiveRBAC(ctx, ws, userID.UUID)
	if errors.Is(err, apperrors.ErrNotFound) {
		return nil, "the sender's account is no longer active, so its right to send these files cannot be confirmed", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("comms: reading the sender's grants: %w", err)
	}
	seat, err := a.authority.SeatType(ctx, ws, userID.UUID)
	if err != nil {
		return nil, "", fmt.Errorf("comms: reading the sender's seat: %w", err)
	}
	return principal.WithActor(ctx, principal.Principal{
		Type:        principal.PrincipalHuman,
		ID:          "human:" + userID.String(),
		UserID:      userID.UUID,
		TeamIDs:     rbac.TeamIDs,
		SeatType:    seat,
		Permissions: rbac.Permissions,
	}), "", nil
}

// EnsureTransmittable asks the two questions that can change between staging and
// transmit, per file, and stops at the first no.
//
// A file the sender can no longer SEE and one that does not exist are the same
// answer here on purpose: GetAttachmentMeta existence-hides both as ErrNotFound,
// and telling them apart in a park reason would leak whether a document the
// sender lost access to still exists.
func (a commsAttachments) EnsureTransmittable(
	ctx context.Context, userID ids.UserID, attachmentIDs []ids.UUID,
) (bool, string, error) {
	senderCtx, reason, err := a.senderCtx(ctx, userID)
	if err != nil || reason != "" {
		return false, reason, err
	}
	for _, id := range attachmentIDs {
		meta, err := a.files.GetAttachmentMeta(senderCtx, id)
		if errors.Is(err, apperrors.ErrNotFound) {
			return false, "a file attached to this message is no longer available to the sender; it was archived, or their access to the record holding it was withdrawn", nil
		}
		if err != nil {
			return false, "", fmt.Errorf("comms: reading an attached file: %w", err)
		}
		if scanErr := activities.EnsureAttachmentScanClean(meta.ScanStatus); scanErr != nil {
			return false, fmt.Sprintf(
				"%q did not pass the malware scan in time to be sent: %s", meta.Filename, scanErr), nil
		}
	}
	return true, "", nil
}

// ReadForSend opens each attachment's bytes for the transmit, in the order
// asked, under the SENDER's own authority.
//
// OpenAttachment carries the scan gate as well as the row-scope one, so a file
// quarantined between staging and now cannot be read here even though
// EnsureTransmittable ran a moment earlier — two checks of the same fact, and
// the cheaper one is not trusted to have been recent enough.
//
// A read that fails returns the error rather than a short set: the dispatcher
// retries on error, and a message transmitted with fewer files than it claims
// is the outcome this whole path exists to prevent.
func (a commsAttachments) ReadForSend(
	ctx context.Context, userID ids.UserID, attachmentIDs []ids.UUID,
) ([][]byte, error) {
	if len(attachmentIDs) == 0 {
		return nil, nil
	}
	senderCtx, reason, err := a.senderCtx(ctx, userID)
	if err != nil {
		return nil, err
	}
	if reason != "" {
		return nil, fmt.Errorf("comms: reading files for a send: %s", reason)
	}
	out := make([][]byte, 0, len(attachmentIDs))
	total := int64(0)
	for _, id := range attachmentIDs {
		body, err := a.readOne(senderCtx, id)
		if err != nil {
			return nil, err
		}
		total += int64(len(body))
		if total > maxSendBytes {
			// The count is capped in the contract and the size per file at
			// upload, and neither bounds their PRODUCT — ten files at the
			// upload limit is a quarter of a gigabyte held at once, and the
			// base64 encoding below doubles it. Refusing here is what keeps a
			// worker that serves every send in the installation from dying of
			// one message.
			return nil, fmt.Errorf(
				"comms: this message's files total more than %d MiB, which is more than a send may carry",
				maxSendBytes>>20)
		}
		out = append(out, body)
	}
	return out, nil
}

// maxSendBytes caps what ONE message may carry in total.
//
// Below what mailbox providers accept (Gmail refuses past 25 MiB after
// encoding), so a message this passes is one the provider will take rather than
// one this product built and the wire refused.
const maxSendBytes = 20 << 20

// readOne reads one attachment fully, closing the object either way.
func (a commsAttachments) readOne(ctx context.Context, id ids.UUID) ([]byte, error) {
	_, rc, err := a.files.OpenAttachment(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("comms: opening an attached file: %w", err)
	}
	defer func() {
		//craft:ignore swallowed-errors the bytes are already read by the time this runs; failing a send because a reader would not close would refuse a message that is otherwise complete
		_ = rc.Close()
	}()
	body, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("comms: reading an attached file: %w", err)
	}
	return body, nil
}

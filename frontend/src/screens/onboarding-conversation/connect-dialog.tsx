import type { ReactNode } from "react";
import { useId } from "react";
import { Modal } from "../../design-system/atoms";
import { ProviderMark } from "../../design-system/provider-mark";
import { useT } from "../../i18n";

// The connect act's one dialog shell: a provider's ask, its own surface,
// never a card growing an inline panel underneath it. Built on the shared
// `Modal` atom (focus trap, Escape, focus return, `role="dialog"`) rather
// than a bespoke one — those guarantees already have their own passing
// tests, and a second implementation is a second place they can drift.
//
// The redirect that follows a real "allow" click leaves this dialog and the
// whole page behind; nothing here pretends otherwise. What the dialog owns is
// the ASK — what is about to be read, in plain words, before the reader
// leaves for the provider's own consent screen (or, for IMAP, before they
// hand over a credential at all).

export function ConnectDialog({
  open,
  onClose,
  providerMarkKey,
  headline,
  intro,
  children,
}: Readonly<{
  open: boolean;
  onClose: () => void;
  /** The key `ProviderMark` recognises — the badge above the headline. */
  providerMarkKey: string;
  headline: string;
  /**
   * The plain-words explanation of what connecting reads. Omitted where the
   * dialog's own content (LinkedIn's existing scope list) already carries
   * that disclosure — never both, which would say the same thing twice.
   */
  intro?: string;
  children: ReactNode;
}>) {
  const t = useT();
  const headingId = useId();
  return (
    <Modal open={open} onClose={onClose} labelledBy={headingId}>
      {/* Its own positioned frame, rather than `position: relative` on the
          shared `.modal` shell every dialog in the app uses — the close
          button's anchor is this dialog's business alone. */}
      <div className="ob-connect-dialog-frame">
        <button
          type="button"
          className="ob-connect-dialog-close"
          onClick={onClose}
          aria-label={t("ob.conv.connect.dialogClose")}
        >
          ×
        </button>
        <div className="ob-connect-dialog-mark" aria-hidden="true">
          <ProviderMark providerKey={providerMarkKey} />
        </div>
        <h2 id={headingId} className="ob-connect-dialog-title">
          {headline}
        </h2>
        {intro && <p className="ob-connect-dialog-intro">{intro}</p>}
        <div className="ob-connect-dialog-body">{children}</div>
      </div>
    </Modal>
  );
}

import { useMutation } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";
import { api } from "../api/client";
import { Button, Modal } from "../design-system/atoms";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import { problemMessage } from "./common";

// The admin-issued set-password link (ADR-0061 Amendment 1). On an installation
// with no outbound email, an invited member is created active with no password
// and no way to be told one — this is how the admin gets a link to hand over.
//
// The link is a LIVE account-takeover credential, so the DIALOG owns the
// request that mints it and the parent renders the dialog only while it is
// open. Closing therefore unmounts every copy at once — component state and
// TanStack's own mutation cache — instead of leaving the credential resident
// behind a hidden modal. It also settles the close-while-in-flight case for
// free: a response that lands after the close has nothing left to write to.
// A dismissed link is not recoverable, and need not be — the roster row mints
// a fresh one.

type PasswordLink = Readonly<{ url: string; expiresAt: string }>;

// PasswordLinkDialog mints a link for one member and shows it. Mount it to
// issue; unmount it to forget. Both entry points — the roster row and the
// post-invite flow — use this one component, so the invite has no privileged
// shortcut that could drift from what the row does.
export function PasswordLinkDialog({
  userId,
  memberName,
  onClose,
}: Readonly<{ userId: string; memberName: string; onClose: () => void }>) {
  const mint = useMutation({
    mutationFn: async (id: string): Promise<PasswordLink> => {
      const { data, error } = await api.POST("/users/{id}/password-link", {
        params: { path: { id } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return { url: data.set_password_url, expiresAt: data.expires_at };
    },
  });

  // Issue once per mount. The ref guard matters because each issue SUPERSEDES
  // the member's outstanding token: a double-fire (StrictMode's deliberate
  // double-invoke, or any re-render that re-ran this) would invalidate the
  // link it had just minted.
  const { mutate } = mint;
  const issued = useRef(false);
  useEffect(() => {
    if (issued.current) {
      return;
    }
    issued.current = true;
    mutate(userId);
  }, [mutate, userId]);

  return (
    <PasswordLinkModal
      onClose={onClose}
      memberName={memberName}
      link={mint.data ?? null}
      pending={mint.isPending}
      error={mint.error?.message ?? null}
      onRetry={() => mutate(userId)}
    />
  );
}

function PasswordLinkModal({
  onClose,
  memberName,
  link,
  pending,
  error,
  onRetry,
}: Readonly<{
  onClose: () => void;
  memberName: string;
  link: PasswordLink | null;
  pending: boolean;
  error: string | null;
  onRetry: () => void;
}>) {
  const t = useT();
  const headingId = useId();
  return (
    <Modal open onClose={onClose} labelledBy={headingId} size="wide">
      <h2
        id={headingId}
        className="t-h2"
        style={{ marginBottom: "var(--space-3)" }}
      >
        {t("users.link.title", { name: memberName })}
      </h2>
      {pending && <p className="t-small">{t("users.link.pending")}</p>}
      {error && (
        <>
          <p
            className="t-caption"
            role="alert"
            style={{ color: "var(--danger)" }}
          >
            {error}
          </p>
          {/* The member exists either way — only the link failed. Retry is the
              whole point of this branch: without it the admin is left with an
              account nobody can sign into and no visible way forward. */}
          <p className="t-small">{t("users.link.failed")}</p>
        </>
      )}
      {link && !pending && (
        <>
          <p className="t-small">{t("users.link.body")}</p>
          <CopyableLink url={link.url} />
          <Expiry iso={link.expiresAt} />
        </>
      )}
      <div className="actions">
        {error && (
          <Button variant="primary" onClick={onRetry} disabled={pending}>
            {t("users.link.retry")}
          </Button>
        )}
        <Button onClick={onClose}>{t("users.link.done")}</Button>
      </div>
    </Modal>
  );
}

// Expiry renders the deadline in the viewer's own timezone — an admin reading
// "expires 12/08/2026, 14:03" needs it in the wall clock they will quote to the
// member, not a fixed zone.
function Expiry({ iso }: Readonly<{ iso: string }>) {
  const t = useT();
  const { locale } = useLocale();
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  return (
    <p className="t-caption">
      <time dateTime={iso}>
        {t("users.link.expires", { when: formatDateTime(iso, locale, zone) })}
      </time>
    </p>
  );
}

function CopyableLink({ url }: Readonly<{ url: string }>) {
  const t = useT();
  const [copied, setCopied] = useState(false);
  const [copyFailed, setCopyFailed] = useState(false);
  return (
    <div className="users-link-row">
      {/* Read-only rather than plain text so the admin can still select and
          copy by hand when the clipboard API is unavailable (an insecure
          origin, or a browser that refuses the permission). */}
      <input
        className="input"
        readOnly
        value={url}
        aria-label={t("users.link.urlLabel")}
        onFocus={(e) => e.currentTarget.select()}
      />
      <Button
        small
        onClick={() => {
          // navigator.clipboard is UNDEFINED outside a secure context, and a
          // bare property access would throw synchronously — leaving the admin
          // with a dead button and no message. That is not an edge case here:
          // an email-less LAN installation on plain http is the deployment this
          // whole feature serves, and a bare origin over http is accepted.
          const clipboard = navigator.clipboard;
          if (!clipboard) {
            setCopied(false);
            setCopyFailed(true);
            return;
          }
          clipboard.writeText(url).then(
            () => {
              setCopyFailed(false);
              setCopied(true);
            },
            () => {
              setCopied(false);
              setCopyFailed(true);
            },
          );
        }}
      >
        {copied ? t("users.link.copied") : t("users.link.copy")}
      </Button>
      {copyFailed && (
        <span className="t-caption" role="alert" style={{ flexBasis: "100%" }}>
          {t("users.link.copyFailed")}
        </span>
      )}
    </div>
  );
}

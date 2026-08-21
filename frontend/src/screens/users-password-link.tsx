import { useCallback, useId, useRef, useState } from "react";
import { api } from "../api/client";
import { Button, Modal } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { formatDateTime } from "../format/format";
import { viewerZone } from "../format/timezone";
import { useLocale, useT } from "../i18n";
import { problemMessage } from "./common";
import "./users-admin.css";

// The admin-issued set-password link (ADR-0061 Amendment 1). On an installation
// with no outbound email, an invited member is created active with no password
// and no way to be told one — this is how the admin gets a link to hand over.
//
// The link is a LIVE account-takeover credential, so it lives in state this
// screen owns and nowhere else: `clear()` drops it with the dialog, and a
// response that arrives after its dialog closed is discarded rather than
// written back. That is what a shared query cache could not give it, and why
// the request belongs to the admin's click rather than to a mount.
//
// A dismissed link is not recoverable, and need not be — the roster row mints
// a fresh one.

type PasswordLink = Readonly<{ url: string; expiresAt: string }>;

type PasswordLinkState = Readonly<{
  pending: boolean;
  link: PasswordLink | null;
  error: string | null;
}>;

const idle: PasswordLinkState = { pending: false, link: null, error: null };

// usePasswordLink mints links and holds at most one at a time. Both entry
// points — the roster row and the post-invite flow — go through it, so the
// invite has no privileged shortcut that could drift from what the row does.
export function usePasswordLink() {
  const t = useT();
  const [state, setState] = useState<PasswordLinkState>(idle);
  // Which request the open dialog is waiting for. A response is accepted only
  // while it is still the latest: close mid-flight, open another member, or
  // reopen the SAME member, and the earlier credential is dropped on arrival
  // instead of landing in state nobody is looking at. It counts requests rather
  // than naming the member, because reopening one member makes two requests
  // whose member id is identical — the older one would otherwise still look
  // current and could clear the newer link or report its own stale failure.
  const latest = useRef(0);

  const clear = useCallback(() => {
    latest.current += 1;
    setState(idle);
  }, []);

  const mint = useCallback(
    async (userId: string) => {
      latest.current += 1;
      const request = latest.current;
      setState({ pending: true, link: null, error: null });
      try {
        const { data, error } = await api.POST("/users/{id}/password-link", {
          params: { path: { id: userId } },
        });
        if (latest.current !== request) {
          return;
        }
        setState(
          error
            ? { pending: false, link: null, error: problemMessage(error) }
            : {
                pending: false,
                error: null,
                link: {
                  url: data.set_password_url,
                  expiresAt: data.expires_at,
                },
              },
        );
      } catch {
        // An HTTP refusal arrives as `error` above; only a transport failure
        // rejects. Without this the rejection escapes and the dialog sits on
        // "Creating the link…" for good — the admin cannot tell a dead network
        // from a slow server, and has no way to retry.
        if (latest.current === request) {
          setState({
            pending: false,
            link: null,
            error: t("users.link.offline"),
          });
        }
      }
    },
    [t],
  );

  return { state, mint, clear };
}

export function PasswordLinkModal({
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
      {/* `modal-title` is the dialog heading's own interval, spelled once in
          atoms.css. It was an inline style here, which is a second author for a
          rhythm the design system already owns. */}
      <h2 id={headingId} className="t-h3 modal-title">
        {t("users.link.title", { name: memberName })}
      </h2>
      {pending && <p className="t-small">{t("users.link.pending")}</p>}
      {/* `danger`: the credential does not exist. The same vocabulary the
          roster and the invite form now use for a refused write, rather than a
          paragraph tinted by hand — the tint WAS the claim, spelled in an
          inline style, and nothing said which of the four tones it meant.

          The member exists either way — only the link failed. Retry is the
          whole point of this branch: without it the admin is left with an
          account nobody can sign into and no visible way forward. */}
      {error && (
        <Callout tone="danger" live="alert">
          <p>{error}</p>
          <p>{t("users.link.failed")}</p>
        </Callout>
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
  const zone = viewerZone();
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
      {/* `warn`, not `danger`: the link itself is fine and is on screen in the
          field beside this — what failed is the clipboard, and the way out is
          to select the field by hand, which is exactly why it is a read-only
          input rather than text. */}
      {copyFailed && (
        <Callout tone="warn" live="alert" className="users-formerror">
          {t("users.link.copyFailed")}
        </Callout>
      )}
    </div>
  );
}

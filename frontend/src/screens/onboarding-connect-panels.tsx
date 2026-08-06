import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowRight,
  CheckCircle2,
  Circle,
  Mail,
  ShieldCheck,
} from "lucide-react";
import { useEffect, useState } from "react";
import { api } from "../api/client";
import { Button } from "../design-system/atoms";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, throwProblem } from "./common";
import { imapErrorMessage } from "./imap-connect-form";
import { OnboardingBackread } from "./onboarding-backread";

// The provider connect panels: real inbox capture, one panel per provider.
// The conversational connect act renders them in the artifact panel behind
// the per-purpose consent turn; connecting stays value-before-permission
// and the panels never claim a connection the server did not confirm.
//
// A confirmed OAuth connection hands straight to the backread step: access
// granted is not history read, and the two are asked separately — the grant
// costs nothing, reading the history spends budget.

// The OAuth outcomes that no retry can clear: the provider refused the grant,
// or its API was never enabled for this deployment. Keyed off the server's
// outcome segment, and pointing at the same copy Settings renders so the two
// surfaces cannot drift apart.
const PERMANENT_FAILURE_BODY: Record<string, MessageKey | undefined> = {
  misconfigured: "connectors.oauthMisconfigured",
  rejected: "connectors.oauthRejected",
};

// The honest-failure banner the connect panels share.
function ConnectWarn({ title, body }: { title: string; body: string }) {
  return (
    <div className="readfail warn" style={{ maxWidth: 460, margin: "0 auto" }}>
      <span className="rfi">
        <Circle aria-hidden />
      </span>
      <div>
        <div className="rft">{title}</div>
        <p className="rfp">{body}</p>
      </div>
    </div>
  );
}

export type OAuthProvider = "gmail" | "graph";

const OAUTH_PROVIDERS: readonly OAuthProvider[] = ["gmail", "graph"];

// The consent return carries its provider as a route segment. A route segment
// is just text, so it is narrowed by membership in the known set — never
// asserted into the union. null means "no provider this build knows", which is
// NOT the same fact as the segment being absent: the caller keeps the two apart.
function asOAuthProvider(value: string | undefined): OAuthProvider | null {
  return OAUTH_PROVIDERS.find((p) => p === value) ?? null;
}

// A real "allow" click leaves the page entirely for the provider's own
// consent screen — this tab's sessionStorage is the only thing that survives
// that round trip, so it is what tells a genuine return apart from a stale
// or bookmarked `/connect/ok/...` URL replayed with no live attempt behind
// it. `sessionStorage` (not `localStorage`) is deliberate: the mark belongs
// to the ONE tab that started the trip, the same scope the redirect itself
// stays within.
const OAUTH_ATTEMPT_KEY = "ob.connect.oauthAttempt";

function markOAuthAttempt(provider: OAuthProvider): void {
  try {
    sessionStorage.setItem(OAUTH_ATTEMPT_KEY, provider);
  } catch {
    // Storage can be unavailable (private browsing, disabled): the redirect
    // still happens, and the return trip falls back to showing its result
    // inline rather than reopening a dialog it has no proof this tab opened.
  }
}

/** The provider a real attempt from THIS tab is returning for, or null if
 * none is recorded — read-only, so the caller decides when the mark is
 * actually spent (`clearOAuthAttempt`). */
export function peekOAuthAttempt(): OAuthProvider | null {
  try {
    return asOAuthProvider(
      sessionStorage.getItem(OAUTH_ATTEMPT_KEY) ?? undefined,
    );
  } catch {
    return null;
  }
}

/** Spends the mark so a reload of the same return URL reads as the stale
 * link it now is, not a second live attempt. */
export function clearOAuthAttempt(): void {
  try {
    sessionStorage.removeItem(OAUTH_ATTEMPT_KEY);
  } catch {
    // Nothing to clear if the write never landed.
  }
}

const OAUTH_COPY: Record<
  OAuthProvider,
  {
    btn: MessageKey;
    hint: MessageKey;
    unverified: MessageKey;
    failed: MessageKey;
  }
> = {
  gmail: {
    btn: "ob.s4.googleBtn",
    hint: "ob.s4.googleHint",
    unverified: "ob.s4.googleUnverified",
    failed: "ob.s4.googleFailed",
  },
  graph: {
    btn: "ob.s4.microsoftBtn",
    hint: "ob.s4.microsoftHint",
    unverified: "ob.s4.microsoftUnverified",
    failed: "ob.s4.microsoftFailed",
  },
};

// Pre-consent: the server mints the consent URL (and the signed state + CSRF
// cookie that guard the callback); the browser just goes. One panel serves
// every OAuth provider — only the copy and the POST path vary.
//
// This panel never signals completion itself: a real "allow" click leaves
// the page for the provider's own consent screen, and the connection is
// confirmed only once the redirect returns, by `OAuthReturnPanel`. `onDismiss`
// closes whatever is showing this panel WITHOUT deciding anything — the
// reader changed their mind about this one provider, not about connecting at
// all. Skipping the whole required step is a separate, more deliberate
// action the surface offers beside the provider choice, not a button buried
// inside one provider's own ask.
export function OAuthConnectPanel({
  provider,
  onDismiss,
  onPendingChange,
}: Readonly<{
  provider: OAuthProvider;
  onDismiss: () => void;
  /**
   * Told whenever the connect POST's in-flight state changes. The caller
   * wraps the dialog's own close handler with it: X, Escape, and backdrop
   * dismissal all resolve to that ONE handler (see `ConnectDialog`), so this
   * is the one place that lets them honor the same no-dismissal-during-submit
   * invariant the disabled "Not now" button below already enforces.
   */
  onPendingChange?: (pending: boolean) => void;
}>) {
  const t = useT();
  const copy = OAUTH_COPY[provider];
  const connect = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/connectors/{provider}/connect", {
        params: { path: { provider } },
        body: {},
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (data) => {
      if (data.authorize_url) {
        markOAuthAttempt(provider);
        globalThis.location.assign(data.authorize_url);
      }
    },
  });
  useEffect(() => {
    onPendingChange?.(connect.isPending);
  }, [connect.isPending, onPendingChange]);
  return (
    <>
      {connect.isError && (
        <ConnectWarn
          title={t(copy.failed)}
          body={problemMessageOf(connect.error, t)}
        />
      )}
      <p
        className="spoken-hint"
        style={{ maxWidth: 460, margin: "4px auto 0" }}
      >
        <ShieldCheck aria-hidden /> {t(copy.hint)}
      </p>
      <p className="t-small ob-google-unverified">{t(copy.unverified)}</p>
      <div className="ob-connect-dialog-actions">
        <Button
          variant="primary"
          disabled={connect.isPending}
          onClick={() => connect.mutate()}
        >
          {connect.isPending ? (
            <>
              <span className="ob-spinner" /> {t("ob.s4.connecting")}
            </>
          ) : (
            <>
              <Mail aria-hidden /> {t(copy.btn)}
            </>
          )}
        </Button>
        <button
          type="button"
          className="ob-connect-dialog-notnow"
          // Dismissal is a decision NOT to connect, so it must not be
          // available while the credential POST it would abandon is still
          // in flight: a success landing after the reader already backed out
          // would leave a mailbox connected against a "no" the panel already
          // promised.
          disabled={connect.isPending}
          onClick={onDismiss}
        >
          {t("ob.s4.notNow")}
        </button>
      </div>
    </>
  );
}

// Post-consent: the roster row IS the proof a connection happened — never a
// static claim the server hasn't confirmed. The import offered next belongs to
// the mailbox that just connected, so the returning provider is matched
// exactly: the roster is provider-ordered, and taking whichever OAuth row
// comes first would offer to import Gmail after a Microsoft consent.
export function OAuthReturnPanel({
  outcome,
  provider,
  onComplete,
  onConfirmedChange,
}: Readonly<{
  outcome?: string;
  /** The provider the consent returned for, from the deep-link route. */
  provider?: string;
  onComplete: (skipped: boolean) => Promise<void>;
  /**
   * Told whenever this trip's confirmation settles: true only once a live
   * mailbox for the returning provider is verified in the roster, false for
   * every other state (still loading, denied, unresolved, or verified
   * absent). The caller uses it to keep the honest skip/retry exit open
   * until a mailbox is actually confirmed — an unconfirmed return is not a
   * finished one, whatever this panel's own "enter" fallback offers.
   */
  onConfirmedChange?: (confirmed: boolean) => void;
}>) {
  const t = useT();
  const connections = useQuery({
    queryKey: ["connectors"],
    enabled: outcome === "ok",
    queryFn: async () => {
      const { data, error } = await api.GET("/connectors");
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  const returning = asOAuthProvider(provider);
  // A segment this build cannot resolve to a provider names no mailbox, and
  // falling back would offer the import for one the human did not just connect.
  // That is precisely the failure the exact match exists to prevent, so it lands
  // on the confirm-failure state instead of guessing. An ABSENT segment is a
  // different fact — a landing URL minted before the provider rode the route —
  // and the roster's first live OAuth mailbox is the best answer there.
  const unresolvedProvider = provider !== undefined && returning === null;
  // Computed unconditionally (ahead of every early return below) because the
  // `useEffect` below is a hook and must run on every render before any early
  // return, so `confirmed` (and the `live` value it derives from) has to be
  // computed here too, to keep hook order stable. Every outcome besides a
  // resolved "ok" is `undefined` roster data away from ever finding a live
  // row, so `live` stays safely undefined for them rather than throwing.
  const live = connections.data?.data.find((c) =>
    returning === null
      ? asOAuthProvider(c.provider) !== null && c.status === "connected"
      : c.provider === returning && c.status === "connected",
  );
  const confirmed =
    outcome === "ok" && !unresolvedProvider && live !== undefined;
  useEffect(() => {
    onConfirmedChange?.(confirmed);
  }, [confirmed, onConfirmedChange]);

  if (outcome === "denied") {
    return (
      <ConnectWarn
        title={t("ob.s4.connectDenied")}
        body={t("ob.s4.connectRetry")}
      />
    );
  }
  // Onboarding is the DEFAULT return surface, so it sees the same server
  // outcome enum Settings does and must handle all of it: an outcome only one
  // renderer knows about falls through to the other's generic advice. These two
  // failures are permanent, so neither may repeat connectRetry's "try again" —
  // they reuse the Settings wording rather than minting a second copy of it.
  // Object.hasOwn, not a bare index: a route segment like "constructor" would
  // otherwise resolve to an inherited member and render an empty banner.
  const permanentBody =
    outcome && Object.hasOwn(PERMANENT_FAILURE_BODY, outcome)
      ? PERMANENT_FAILURE_BODY[outcome]
      : undefined;
  if (permanentBody) {
    return (
      <ConnectWarn
        title={t("ob.s4.connectConfirmFailed")}
        body={t(permanentBody)}
      />
    );
  }
  if (outcome !== "ok" || unresolvedProvider) {
    return (
      <ConnectWarn
        title={t("ob.s4.connectConfirmFailed")}
        body={t("ob.s4.connectRetry")}
      />
    );
  }
  return (
    <div className="connect-result">
      <div className="cr-h">
        <CheckCircle2 aria-hidden /> {t("ob.s4.connectOkTitle")}
      </div>
      <p className="ob-sub" style={{ margin: "8px auto 0", maxWidth: 460 }}>
        {t("ob.s4.connectOkBody")}
      </p>
      {connections.isPending && (
        <p className="t-small" style={{ marginTop: "var(--space-3)" }}>
          {t("ob.s4.connectVerifying")}
        </p>
      )}
      {live && (
        <>
          <span className="trustpill" style={{ marginTop: "var(--space-3)" }}>
            <ShieldCheck aria-hidden /> {t("ob.s4.connectLive")}
          </span>
          {/* The mailbox is live, so the step is not finished yet: how far back
              to read it is the next question, and the backread owns the exit
              from here — its own leave controls finish onboarding, whether or
              not a read is running. */}
          <OnboardingBackread
            provider={live.provider}
            initial={live.backfill}
            onFinish={(skipped) => void onComplete(skipped)}
          />
        </>
      )}
      {!connections.isPending && !live && (
        <ConnectWarn
          title={t("ob.s4.connectConfirmFailed")}
          body={t("ob.s4.connectRetry")}
        />
      )}
      {!connections.isPending && live === undefined && (
        <Button
          variant="primary"
          style={{ marginTop: "var(--space-4)" }}
          onClick={() => void onComplete(false)}
        >
          {t("ob.s4.enterCrm")} <ArrowRight aria-hidden />
        </Button>
      )}
    </div>
  );
}

// IMAP: a standing connection, mirroring the Settings inline form's typed
// POST (imap-connect-form.tsx) — the same nested `{imap:{...}}` shape and the
// same two IMAP-specific error sentences, so onboarding and Settings can
// never drift onto two different ideas of what "connect" means for this
// provider. The connect call returns BEFORE any mail is read: there is no
// capture count to show here, honestly — only a live row (last_synced_at)
// that fills in a few minutes later, once the sweep runs.
const IMAP_DEFAULT_PORT = "993";

// `onDismiss` closes the dialog without connecting — see `OAuthConnectPanel`
// for why that is a distinct action from skipping the whole required step.
export function ImapConnectPanel({
  onComplete,
  onDismiss,
  onPendingChange,
}: Readonly<{
  onComplete: (skipped: boolean) => Promise<void>;
  onDismiss: () => void;
  /** See `OAuthConnectPanel`'s own `onPendingChange` for what this reports
   * and why the caller needs it. */
  onPendingChange?: (pending: boolean) => void;
}>) {
  const t = useT();
  const qc = useQueryClient();
  const [host, setHostVal] = useState("imap.gmail.com");
  const [port, setPort] = useState(IMAP_DEFAULT_PORT);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [mailbox, setMailbox] = useState("INBOX");
  const [max, setMax] = useState("30");

  const parsedPort =
    port.trim() === "" ? Number(IMAP_DEFAULT_PORT) : Number(port);

  const connect = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/connectors/{provider}/connect", {
        params: { path: { provider: "imap" } },
        body: {
          imap: {
            host: host.trim(),
            port: parsedPort,
            username: email.trim(),
            secret: password,
            mailbox: mailbox.trim() || "INBOX",
            max_messages: Number(max) || 30,
          },
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: () => {
      // The Settings connected-inboxes card shares this query key — a
      // connect made here (onboarding) must land there immediately, not
      // only on that card's next mount (DESIGN §5, one implementation at
      // runtime).
      void qc.invalidateQueries({ queryKey: ["connectors"] });
    },
    onError: () => {
      // The secret is never retained after a failed submit, matching the
      // Settings form's practice.
      setPassword("");
    },
  });
  useEffect(() => {
    onPendingChange?.(connect.isPending);
  }, [connect.isPending, onPendingChange]);

  const parsedMax = max.trim() === "" ? 30 : Number(max);
  const ready =
    host.trim() !== "" &&
    Number.isInteger(parsedPort) &&
    parsedPort >= 1 &&
    parsedPort <= 65535 &&
    email.trim() !== "" &&
    password !== "" &&
    Number.isInteger(parsedMax) &&
    parsedMax >= 1 &&
    parsedMax <= 200;

  if (connect.data?.connection) {
    return (
      <div className="connect-result">
        <div className="cr-h">
          <CheckCircle2 aria-hidden /> {t("ob.s4.capturedTitle")}
        </div>
        <p className="ob-sub" style={{ margin: "8px auto 0", maxWidth: 460 }}>
          {t("ob.s4.capturedBody")}
        </p>
        <Button
          variant="primary"
          style={{ marginTop: "var(--space-4)" }}
          onClick={() => void onComplete(false)}
        >
          {t("ob.s4.enterCrm")} <ArrowRight aria-hidden />
        </Button>
      </div>
    );
  }

  return (
    <>
      <div className="imap-form">
        <label className="field">
          {t("ob.s4.imapHost")}
          <input
            className="input"
            value={host}
            placeholder={t("ob.s4.imapHostPlaceholder")}
            onChange={(e) => setHostVal(e.target.value)}
          />
        </label>
        <label className="field">
          {t("ob.s4.imapPort")}
          <input
            className="input"
            type="number"
            min={1}
            max={65535}
            value={port}
            onChange={(e) => setPort(e.target.value)}
          />
        </label>
        <label className="field full">
          {t("ob.s4.imapEmail")}
          <input
            className="input"
            type="email"
            autoComplete="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
        </label>
        <label className="field full">
          {t("ob.s4.imapPassword")}
          <input
            className="input"
            type="password"
            autoComplete="off"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>
        <label className="field">
          {t("ob.s4.imapMailbox")}
          <input
            className="input"
            value={mailbox}
            onChange={(e) => setMailbox(e.target.value)}
          />
        </label>
        <label className="field">
          {t("ob.s4.imapMax")}
          <input
            className="input"
            type="number"
            min={1}
            max={200}
            value={max}
            onChange={(e) => setMax(e.target.value)}
          />
        </label>
      </div>

      <p
        className="spoken-hint"
        style={{ maxWidth: 460, margin: "12px auto 0" }}
      >
        <ShieldCheck aria-hidden /> {t("ob.s4.imapHint")}
      </p>

      {connect.isError && (
        <ConnectWarn
          title={t("ob.s4.connectFailed")}
          body={imapErrorMessage(connect.error, t)}
        />
      )}

      <div className="ob-connect-dialog-actions">
        <Button
          variant="primary"
          disabled={!ready || connect.isPending}
          onClick={() => connect.mutate()}
        >
          {connect.isPending ? (
            <>
              <span className="ob-spinner" /> {t("ob.s4.connecting")}
            </>
          ) : (
            <>
              <Mail aria-hidden /> {t("ob.s4.imapConnect")}
            </>
          )}
        </Button>
        <button
          type="button"
          className="ob-connect-dialog-notnow"
          // See `OAuthConnectPanel`'s own `onDismiss` for why the in-flight
          // POST has to finish before this becomes a real choice again.
          disabled={connect.isPending}
          onClick={onDismiss}
        >
          {t("ob.s4.notNow")}
        </button>
      </div>
    </>
  );
}

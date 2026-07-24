import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowRight,
  CheckCircle2,
  Circle,
  Mail,
  ShieldCheck,
  SkipForward,
} from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import { Button } from "../design-system/atoms";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { BackfillPanel } from "./backfill";
import { problemMessage, throwProblem } from "./common";
import { imapErrorMessage } from "./imap-connect-form";

// The provider connect panels: real inbox capture, one panel per provider.
// The conversational connect act renders them in the artifact panel behind
// the per-purpose consent turn; connecting stays value-before-permission
// and the panels never claim a connection the server did not confirm.

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

type OAuthProvider = "gmail" | "graph";

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
export function OAuthConnectPanel({
  provider,
  onComplete,
}: Readonly<{
  provider: OAuthProvider;
  onComplete: (skipped: boolean) => Promise<void>;
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
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (data) => {
      if (data.authorize_url) {
        globalThis.location.assign(data.authorize_url);
      }
    },
  });
  return (
    <>
      {connect.isError && (
        <ConnectWarn title={t(copy.failed)} body={connect.error.message} />
      )}
      <p
        className="spoken-hint"
        style={{ maxWidth: 460, margin: "4px auto 0" }}
      >
        <ShieldCheck aria-hidden /> {t(copy.hint)}
      </p>
      <p className="t-small ob-google-unverified">{t(copy.unverified)}</p>
      <div className="connect-acts">
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
        <Button onClick={() => void onComplete(true)}>
          <SkipForward aria-hidden /> {t("ob.s4.skipLater")}
        </Button>
      </div>
    </>
  );
}

// Post-consent: the callback route carries no provider, so this reads the
// roster and shows whichever OAuth mailbox is now live. The row IS the proof
// — never a static claim the server hasn't confirmed.
export function OAuthReturnPanel({
  outcome,
  onComplete,
}: Readonly<{
  outcome?: string;
  onComplete: (skipped: boolean) => Promise<void>;
}>) {
  const t = useT();
  const connections = useQuery({
    queryKey: ["connectors"],
    enabled: outcome === "ok",
    queryFn: async () => {
      const { data, error } = await api.GET("/connectors");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  const live = connections.data?.data.find(
    (c) =>
      (c.provider === "gmail" || c.provider === "graph") &&
      c.status === "connected",
  );

  if (outcome === "denied") {
    return (
      <ConnectWarn
        title={t("ob.s4.connectDenied")}
        body={t("ob.s4.connectRetry")}
      />
    );
  }
  if (outcome !== "ok") {
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
          <BackfillPanel provider={live.provider} />
        </>
      )}
      {!connections.isPending && !live && (
        <ConnectWarn
          title={t("ob.s4.connectConfirmFailed")}
          body={t("ob.s4.connectRetry")}
        />
      )}
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

// IMAP: a standing connection, mirroring the Settings inline form's typed
// POST (imap-connect-form.tsx) — the same nested `{imap:{...}}` shape and the
// same two IMAP-specific error sentences, so onboarding and Settings can
// never drift onto two different ideas of what "connect" means for this
// provider. The connect call returns BEFORE any mail is read: there is no
// capture count to show here, honestly — only a live row (last_synced_at)
// that fills in a few minutes later, once the sweep runs.
const IMAP_DEFAULT_PORT = 993;

export function ImapConnectPanel({
  onComplete,
}: Readonly<{ onComplete: (skipped: boolean) => Promise<void> }>) {
  const t = useT();
  const qc = useQueryClient();
  const [host, setHostVal] = useState("imap.gmail.com");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [mailbox, setMailbox] = useState("INBOX");
  const [max, setMax] = useState("30");

  const connect = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/connectors/{provider}/connect", {
        params: { path: { provider: "imap" } },
        body: {
          imap: {
            host: host.trim(),
            port: IMAP_DEFAULT_PORT,
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

  const parsedMax = max.trim() === "" ? 30 : Number(max);
  const ready =
    host.trim() !== "" &&
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
        <label className="field full">
          {t("ob.s4.imapHost")}
          <input
            className="input"
            value={host}
            placeholder={t("ob.s4.imapHostPlaceholder")}
            onChange={(e) => setHostVal(e.target.value)}
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
          body={imapErrorMessage(connect.error, t) ?? t("ob.s4.connectFailed")}
        />
      )}

      <div className="connect-acts">
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
        <Button onClick={() => void onComplete(true)}>
          <SkipForward aria-hidden /> {t("ob.s4.skipLater")}
        </Button>
      </div>
    </>
  );
}

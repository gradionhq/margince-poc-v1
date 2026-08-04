import { useQuery } from "@tanstack/react-query";
import { Check } from "lucide-react";
import type { ReactNode } from "react";
import { useState } from "react";
import { api } from "../../api/client";
import { Button } from "../../design-system/atoms";
import { ProviderMark } from "../../design-system/provider-mark";
import { useT } from "../../i18n";
import type { MessageKey } from "../../i18n/en";
import { problemMessage } from "../common";
import { ConnectDialog } from "./connect-dialog";

// The connect act's work surface: two sections of real-width cards — the
// required mailbox choice, and the optional network one beside it — each
// card opening its OWN dialog rather than growing an inline panel under
// itself. A card names the provider AND what connecting it grants (its
// accessible name carries both), so a reader never has to open it to learn
// what it is for.
//
// The four step-level consent guarantees render HERE, on the surface, in a
// real-width grid: they are substance about what this step does, and the
// rail narrates, it does not host a step's substance. Each provider's OWN
// disclosure (its OAuth hint, or LinkedIn's own scope list) lives one level
// deeper, inside that provider's dialog.

export type MailProvider = "google" | "microsoft" | "imap";

export type LinkedinStatus = "pending" | "connected" | "skipped";

// The mark key each provider card carries, straight from `ProviderMark`'s own
// vocabulary. `imap` has no brand of its own, so it takes the neutral mark the
// design system already renders for a provider it has no logo for.
const PROVIDER_MARKS: Readonly<Record<MailProvider, string>> = {
  google: "google",
  microsoft: "microsoft",
  imap: "imap",
};

const PROVIDER_COPY: Readonly<
  Record<
    MailProvider,
    {
      name: MessageKey;
      brings: MessageKey;
      auth: MessageKey;
      dialogHeadline: (t: ReturnType<typeof useT>) => string;
    }
  >
> = {
  google: {
    name: "ob.s4.provGoogle",
    brings: "ob.conv.connect.gmailBrings",
    auth: "ob.conv.connect.scopeGoogle",
    dialogHeadline: (t) =>
      t("ob.conv.connect.dialogHeadlineAccess", {
        name: t("ob.s4.provGoogle"),
      }),
  },
  microsoft: {
    name: "ob.s4.provMicrosoft",
    brings: "ob.conv.connect.microsoftBrings",
    auth: "ob.conv.connect.scopeMicrosoft",
    dialogHeadline: (t) =>
      t("ob.conv.connect.dialogHeadlineAccess", {
        name: t("ob.s4.provMicrosoft"),
      }),
  },
  imap: {
    name: "ob.s4.provImap",
    brings: "ob.conv.connect.imapBrings",
    auth: "ob.conv.connect.scopeImap",
    dialogHeadline: (t) => t("ob.conv.connect.dialogHeadlineImap"),
  },
};

export const MAIL_PROVIDERS: readonly MailProvider[] = [
  "google",
  "microsoft",
  "imap",
];

// The provider name this build's roster row carries for each card. `gcal` is
// a paired connector some OAuth grants also create and never gets its own
// card, so it is deliberately absent from this map.
const ROSTER_PROVIDER: Readonly<Record<MailProvider, string>> = {
  google: "gmail",
  microsoft: "graph",
  imap: "imap",
};

/**
 * Which mailboxes are already live, read fresh here rather than assumed from
 * the pre-consent `provider` selection: a reload can land on this step with a
 * mailbox already connected from an earlier session, and the cards have to
 * say so without the reader clicking anything. Shares the `["connectors"]`
 * query key with `OAuthReturnPanel` — react-query dedupes the two into one
 * request, so this costs nothing extra on the common path.
 */
function useConnectedMailProviders(): ReadonlySet<string> {
  const roster = useQuery({
    queryKey: ["connectors"],
    queryFn: async () => {
      const { data, error } = await api.GET("/connectors");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  const connected = roster.data?.data.filter((c) => c.status === "connected");
  return new Set((connected ?? []).map((c) => c.provider));
}

export function ConnectScene({
  eyebrow,
  provider,
  onPick,
  onDialogClose,
  dialogShowsResult,
  dialogPanel,
  returnPanel,
  onSkip,
  skipDisabled,
  showSkip,
  linkedinStatus,
  onLinkedinConnect,
  onLinkedinSkip,
  linkedinPending,
  linkedinError,
  onEnter,
}: Readonly<{
  eyebrow: string;
  /** The provider whose dialog is open; null while none is chosen. */
  provider: MailProvider | null;
  onPick: (provider: MailProvider) => void;
  /** Closes the open dialog via its own chrome (the X, Escape, backdrop). */
  onDialogClose: () => void;
  /**
   * True when `provider`'s dialog is showing a proven attempt's RESULT
   * (`returnPanel`) rather than the pre-consent ask (`dialogPanel`) — the
   * caller is the one place that can tell a genuine return from this tab
   * apart from a stale or bookmarked outcome URL, so it decides which.
   */
  dialogShowsResult: boolean;
  /** The chosen provider's pre-consent panel, rendered INSIDE its dialog. */
  dialogPanel: ReactNode;
  /**
   * The post-consent result (`OAuthReturnPanel` plus the backfill window
   * control) once a redirect has returned. Shown INSIDE the dialog the
   * reader left from when `dialogShowsResult` says this tab genuinely made
   * that trip; otherwise rendered inline on the surface — still a real
   * finding, just not one this tab can vouch for having just requested.
   */
  returnPanel: ReactNode;
  onSkip: () => void;
  skipDisabled: boolean;
  /** Once consent has returned, skipping is no longer a true option. */
  showSkip: boolean;
  linkedinStatus: LinkedinStatus;
  onLinkedinConnect: (profileUrl: string) => void;
  onLinkedinSkip: () => void;
  linkedinPending: boolean;
  linkedinError: string | null;
  /**
   * Present once the act itself has reached `cn.done` — mail is connected
   * (or its skip recorded) and there is nothing left to gate on. Absent
   * otherwise, so the pinned bar below has nothing to render while the
   * scene's real work (picking a provider, resolving LinkedIn) is still
   * open.
   */
  onEnter?: () => void;
}>) {
  const t = useT();
  const connectedProviders = useConnectedMailProviders();
  const anyMailConnected = MAIL_PROVIDERS.some((key) =>
    connectedProviders.has(ROSTER_PROVIDER[key]),
  );
  const openCopy = provider ? PROVIDER_COPY[provider] : null;

  return (
    <div className="ob-scene ob-connect">
      <div>
        <p className="ob-scene-eyebrow">{eyebrow}</p>
        <h2>{t("ob.conv.connect.sceneTitle")}</h2>
        <p className="ob-scene-sub">{t("ob.conv.connect.sceneSub")}</p>
      </div>

      <ConnectGuarantees />

      <div className="ob-connect-section-head">
        <h3>
          {t("ob.conv.connect.mailboxTitle")}
          <span className="ob-connect-pill ob-connect-pill-required">
            {t("ob.conv.connect.required")}
          </span>
        </h3>
        <p>{t("ob.conv.connect.mailboxHint")}</p>
      </div>

      <div className="ob-connect-grid">
        {MAIL_PROVIDERS.map((key) => {
          const copy = PROVIDER_COPY[key];
          const connected = connectedProviders.has(ROSTER_PROVIDER[key]);
          const blocked = !connected && anyMailConnected;
          return (
            <ConnectorCard
              key={key}
              markKey={PROVIDER_MARKS[key]}
              name={t(copy.name)}
              brings={
                blocked ? t("ob.conv.connect.blockedCard") : t(copy.brings)
              }
              auth={t(copy.auth)}
              state={connected ? "connected" : blocked ? "blocked" : "idle"}
              onOpen={() => onPick(key)}
            />
          );
        })}
      </div>

      {/* The inline fallback: a real finding either way, but shown here
          rather than inside a dialog because this tab has no proof it just
          requested it — see `dialogShowsResult` on why that distinction is
          the caller's to make. */}
      {!dialogShowsResult && returnPanel}

      <div className="ob-connect-section-head">
        <h3>
          {t("ob.conv.connect.networkTitle")}
          <span className="ob-connect-pill ob-connect-pill-recommended">
            {t("ob.conv.connect.recommended")}
          </span>
        </h3>
        <p>{t("ob.conv.connect.networkHint")}</p>
      </div>

      <LinkedinCard
        status={linkedinStatus}
        onConnect={onLinkedinConnect}
        onSkip={onLinkedinSkip}
        pending={linkedinPending}
        error={linkedinError}
      />

      {/* The escape from the whole step, offered only once both sections
          (mailbox, then network) have been seen — never partway down with a
          section still unread below it. Its own row above the continue bar
          rather than a quiet chip inside that bar: the two live in the same
          flex row there, which would read as a paired choice with skip and
          "Enter Margince" as equal alternatives. Kept one row up, in the
          small ghost button voice (bordered outline, no fill, no shadow)
          against the primary CTA below, skip stays the one a reader has to
          notice on purpose, not the one competing for their eye. */}
      {showSkip && (
        <p className="ob-connect-skip-row">
          <Button
            small
            variant="ghost"
            disabled={skipDisabled}
            onClick={onSkip}
          >
            {t("ob.conv.connect.skip")}
          </Button>
        </p>
      )}

      {provider && (
        <ConnectDialog
          open
          onClose={onDialogClose}
          providerMarkKey={PROVIDER_MARKS[provider]}
          // The result dialog names the provider plainly — `returnPanel`
          // (OAuthReturnPanel) carries its own heading for what happened,
          // so a second "access needed" headline above it would be both
          // redundant and wrong: nothing is being asked for any more.
          headline={
            dialogShowsResult
              ? openCopy
                ? t(openCopy.name)
                : ""
              : openCopy
                ? openCopy.dialogHeadline(t)
                : ""
          }
          intro={
            !dialogShowsResult && openCopy
              ? t("ob.conv.connect.dialogIntro", { brings: t(openCopy.brings) })
              : undefined
          }
        >
          {dialogShowsResult ? returnPanel : dialogPanel}
        </ConnectDialog>
      )}

      {/* The finish gate, pinned to the surface's own foot rather than a chip
          in the thread below: the reader is done choosing on THIS panel, so
          the action that leaves it belongs here too. Nothing left to gate on
          once mail is connected, so the bar carries the action alone. */}
      {onEnter && (
        <div className="ob-triage-continue">
          <p className="ob-triage-continue-status" role="status" />
          <Button variant="primary" onClick={onEnter}>
            {t("ob.enter.cta")}
          </Button>
        </div>
      )}
    </div>
  );
}

/**
 * The four step-level promises, as a real two-column grid rather than a
 * flowing paragraph forced into a rail's ~250px column — the wrapping that
 * made every cell there read as broken text. Every word survives from the
 * rail turn it replaces (`ob.s4.scope*`); only its column changed.
 */
function ConnectGuarantees() {
  const t = useT();
  const items: { lead: MessageKey; rest: MessageKey }[] = [
    { lead: "ob.s4.scope1Lead", rest: "ob.s4.scope1Rest" },
    { lead: "ob.s4.scope2Lead", rest: "ob.s4.scope2Rest" },
    { lead: "ob.s4.scope3Lead", rest: "ob.s4.scope3Rest" },
    { lead: "ob.s4.scope4Lead", rest: "ob.s4.scope4Rest" },
  ];
  return (
    <div className="ob-connect-guarantees">
      <p className="ob-connect-guarantees-head">
        {t("ob.conv.connect.guaranteesHeading")}
      </p>
      <ul className="ob-connect-guarantees-grid">
        {items.map((item) => (
          <li key={item.lead}>
            <Check aria-hidden className="ob-connect-guarantee-check" />
            <span>
              <b>{t(item.lead)}</b> {t(item.rest)}
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}

type CardState = "idle" | "connected" | "blocked";

/**
 * One provider tile: a mark, the name, one line of what it gives, and a
 * footer naming the auth mechanism plus the affordance for the tile's own
 * state. The whole tile is the button — its accessible name carries both the
 * provider and what connecting it grants, so a reader never has to open it to
 * find out.
 *
 * Only "idle" is clickable: a connected or blocked tile offers no further
 * action here — disconnecting a mailbox is Settings' job, and a blocked tile
 * names the mailbox already chosen instead of inviting a second one.
 */
function ConnectorCard({
  markKey,
  name,
  brings,
  auth,
  state,
  onOpen,
}: Readonly<{
  markKey: string;
  name: string;
  brings: string;
  auth: string;
  state: CardState;
  onOpen: () => void;
}>) {
  const t = useT();
  const disabled = state !== "idle";
  return (
    <button
      type="button"
      className="ob-connect-card"
      data-state={state}
      disabled={disabled}
      onClick={onOpen}
    >
      <span className="ob-connect-card-head">
        <span className="ob-connect-mark">
          <ProviderMark providerKey={markKey} />
        </span>
        {state === "connected" && (
          <span className="ob-connect-card-done" aria-hidden="true">
            <Check />
          </span>
        )}
      </span>
      <b>{name}</b>
      <small>{brings}</small>
      <span className="ob-connect-card-foot">
        <span className="ob-connect-card-auth">{auth}</span>
        {state !== "blocked" && (
          <span className="ob-connect-card-cta">
            {state === "connected"
              ? t("ob.conv.connect.connectedCta")
              : t("ob.conv.connect.connectCta")}
          </span>
        )}
      </span>
    </button>
  );
}

/**
 * The LinkedIn card: a brief payoff line and a Connect action while pending,
 * its own dialog once clicked, or a resolved state (connected / skipped)
 * once `linkedinStatus` says it is settled. Split out of ConnectScene so the
 * scene itself stays about composition.
 */
function LinkedinCard({
  status,
  onConnect,
  onSkip,
  pending,
  error,
}: Readonly<{
  status: LinkedinStatus;
  onConnect: (profileUrl: string) => void;
  onSkip: () => void;
  pending: boolean;
  error: string | null;
}>) {
  const t = useT();
  const [open, setOpen] = useState(false);

  return (
    <div className="ob-connect-grid ob-connect-grid-single">
      <ConnectorCard
        markKey="linkedin"
        name={t("ob.conv.connect.linkedinName")}
        brings={
          status === "connected"
            ? t("ob.conv.connect.linkedinConnected")
            : status === "skipped"
              ? t("ob.conv.connect.linkedinSkippedNote")
              : t("ob.conv.linkedin.cardBody")
        }
        auth={t("ob.conv.connect.linkedinAuth")}
        state={
          status === "connected"
            ? "connected"
            : status === "skipped"
              ? "blocked"
              : "idle"
        }
        onOpen={() => setOpen(true)}
      />

      {status === "pending" && open && (
        <ConnectDialog
          open
          onClose={() => setOpen(false)}
          providerMarkKey="linkedin"
          headline={t("ob.conv.connect.dialogHeadlineAccess", {
            name: t("ob.conv.connect.linkedinName"),
          })}
        >
          <LinkedinPanel
            onConnect={(url) => {
              onConnect(url);
              setOpen(false);
            }}
            onSkip={() => {
              onSkip();
              setOpen(false);
            }}
            pending={pending}
            error={error}
          />
        </ConnectDialog>
      )}
    </div>
  );
}

// What the live integration will request, named one by one. A member handing
// over their professional network deserves the list before they click, not a
// summary afterwards. This is LinkedIn's OWN disclosure — the step-level
// guarantees moved to `ConnectGuarantees`, but this list stays exactly where
// the reader authorizes: inside the LinkedIn dialog.
const linkedinScopes: { lead: MessageKey; rest: MessageKey }[] = [
  { lead: "ob.conv.linkedin.scope1Lead", rest: "ob.conv.linkedin.scope1Rest" },
  { lead: "ob.conv.linkedin.scope2Lead", rest: "ob.conv.linkedin.scope2Rest" },
  { lead: "ob.conv.linkedin.scope3Lead", rest: "ob.conv.linkedin.scope3Rest" },
  { lead: "ob.conv.linkedin.scope4Lead", rest: "ob.conv.linkedin.scope4Rest" },
];

function LinkedinPanel({
  onConnect,
  onSkip,
  pending,
  error,
}: Readonly<{
  onConnect: (profileUrl: string) => void;
  onSkip: () => void;
  pending: boolean;
  error: string | null;
}>) {
  const t = useT();
  const [profile, setProfile] = useState("");
  const trimmed = profile.trim();

  return (
    <div className="ob-connect-linkedin-panel">
      <div className="ob-conv-scopes">
        {linkedinScopes.map((scope) => (
          <p key={scope.lead}>
            <Check aria-hidden />
            {/* Lead and rest share ONE flex item so the row wraps as a
                single line of prose — two items each shrinking to their own
                width is what broke the bold lead across lines. */}
            <span>
              <b>{t(scope.lead)}</b> {t(scope.rest)}
            </span>
          </p>
        ))}
      </div>
      <p className="co-muted">{t("ob.conv.linkedin.neverContacts")}</p>
      <label className="ob-conv-field" htmlFor="linkedin-profile">
        {t("ob.conv.linkedin.profileLabel")}
        <input
          id="linkedin-profile"
          type="url"
          inputMode="url"
          placeholder={t("ob.conv.linkedin.profilePlaceholder")}
          value={profile}
          onChange={(event) => setProfile(event.target.value)}
        />
      </label>
      <p className="co-muted">{t("ob.conv.linkedin.profileWhy")}</p>
      <div className="ob-connect-dialog-actions">
        <Button
          variant="primary"
          disabled={trimmed === "" || pending}
          onClick={() => onConnect(trimmed)}
        >
          {t("ob.conv.linkedin.authorize")}
        </Button>
        <button
          type="button"
          className="ob-connect-dialog-notnow"
          onClick={onSkip}
        >
          {t("ob.conv.linkedin.skip")}
        </button>
      </div>
      {error !== null && (
        <p role="alert" className="co-error">
          {error}
        </p>
      )}
      <p className="co-muted">{t("ob.conv.linkedin.appPending")}</p>
    </div>
  );
}

import { Check } from "lucide-react";
import type { ReactNode } from "react";
import { useState } from "react";
import { Button } from "../../design-system/atoms";
import { ProviderMark } from "../../design-system/provider-mark";
import { useT } from "../../i18n";
import type { MessageKey } from "../../i18n/en";

// The connect act's work surface: the mailbox choice as provider cards (with
// the chosen provider's own panel underneath), and the LinkedIn card beside
// it. One scene, the same rule the company and voice acts follow — mail
// gates the act's finish, LinkedIn never does, and both are visible at once
// rather than sequenced through separate screens.

export type MailProvider = "google" | "microsoft" | "imap";

export type LinkedinStatus = "pending" | "connected" | "skipped";

// The logo key each mail-provider card carries. `imap` has no brand of its
// own — it is the "any other server" path — so it takes the neutral mark
// the design system already renders for a provider it has no logo for; a
// borrowed shape would be a wrong mark, not a missing one. LinkedIn is not a
// mail provider and carries its own literal key at its own card, below.
const PROVIDER_MARKS: Readonly<Record<MailProvider, string>> = {
  google: "google",
  microsoft: "microsoft",
  imap: "imap",
};

const PROVIDER_COPY: Readonly<
  Record<MailProvider, { name: MessageKey; scopes: MessageKey }>
> = {
  google: { name: "ob.s4.provGoogle", scopes: "ob.conv.connect.scopeGoogle" },
  microsoft: {
    name: "ob.s4.provMicrosoft",
    scopes: "ob.conv.connect.scopeMicrosoft",
  },
  imap: { name: "ob.s4.provImap", scopes: "ob.conv.connect.scopeImap" },
};

export const MAIL_PROVIDERS: readonly MailProvider[] = [
  "google",
  "microsoft",
  "imap",
];

// What the live integration will request, named one by one. A member handing
// over their professional network deserves the list before they click, not a
// summary afterwards.
const linkedinScopes: { lead: MessageKey; rest: MessageKey }[] = [
  { lead: "ob.conv.linkedin.scope1Lead", rest: "ob.conv.linkedin.scope1Rest" },
  { lead: "ob.conv.linkedin.scope2Lead", rest: "ob.conv.linkedin.scope2Rest" },
  { lead: "ob.conv.linkedin.scope3Lead", rest: "ob.conv.linkedin.scope3Rest" },
  { lead: "ob.conv.linkedin.scope4Lead", rest: "ob.conv.linkedin.scope4Rest" },
];

export function ConnectScene({
  eyebrow,
  provider,
  onPick,
  panel,
  footNote,
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
  /** The provider whose panel is open; null while none is chosen. */
  provider: MailProvider | null;
  onPick: (provider: MailProvider) => void;
  /** The chosen provider's connect panel, or the consent-return panel. */
  panel: ReactNode;
  footNote: string;
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
  return (
    <div className="ob-scene ob-connect">
      <div>
        <p className="ob-scene-eyebrow">{eyebrow}</p>
        <h2>{t("ob.conv.connect.sceneTitle")}</h2>
        <p className="ob-scene-sub">{t("ob.conv.connect.sceneSub")}</p>
      </div>

      <p className="ob-connect-section">
        <span>{t("ob.conv.connect.mailSection")}</span>
        <b>{t("ob.conv.connect.required")}</b>
      </p>

      <div className="ob-connect-grid">
        {MAIL_PROVIDERS.map((key) => (
          <button
            key={key}
            type="button"
            className="ob-connect-card"
            aria-pressed={provider === key}
            onClick={() => onPick(key)}
          >
            <span className="ob-connect-mark">
              <ProviderMark providerKey={PROVIDER_MARKS[key]} />
            </span>
            <b>{t(PROVIDER_COPY[key].name)}</b>
            <small>{t(PROVIDER_COPY[key].scopes)}</small>
          </button>
        ))}
      </div>

      {panel}

      <p className="ob-connect-section">
        <span>{t("ob.conv.connect.linkedinSection")}</span>
        <b className="is-recommended">{t("ob.conv.connect.recommended")}</b>
      </p>

      <LinkedinCard
        status={linkedinStatus}
        onConnect={onLinkedinConnect}
        onSkip={onLinkedinSkip}
        pending={linkedinPending}
        error={linkedinError}
      />

      <div className="ob-voice-foot">
        <div>
          <p>{footNote}</p>
          <p className="ob-connect-linkedin-foot">
            {t("ob.conv.connect.linkedinFoot")}
          </p>
        </div>
        {showSkip && (
          <Button
            small
            variant="ghost"
            disabled={skipDisabled}
            onClick={onSkip}
          >
            {t("ob.conv.connect.skip")}
          </Button>
        )}
      </div>

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
 * The LinkedIn card: a brief payoff line and a Connect action while pending,
 * an expanding panel with the authorization form once clicked, or a resolved
 * state (connected / skipped) once linkedinStatus says it is settled. Split
 * out of ConnectScene so the scene itself stays about composition.
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
    <div className="ob-connect-linkedin">
      <div className="ob-connect-card ob-connect-linkedin-row">
        <span className="ob-connect-mark">
          <ProviderMark providerKey="linkedin" />
        </span>
        <div className="ob-connect-linkedin-body">
          <b>{t("ob.conv.connect.linkedinName")}</b>
          <small>{t("ob.conv.linkedin.cardBody")}</small>
        </div>
        <LinkedinAction
          status={status}
          open={open}
          onOpen={() => setOpen(true)}
        />
      </div>

      {status === "pending" && open && (
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
      )}
    </div>
  );
}

// The card's right-hand action: Connect while nothing is decided yet, or the
// resolved state once it is — a card that stayed clickable after resolving
// would invite re-authorizing (or re-skipping) something already settled.
function LinkedinAction({
  status,
  open,
  onOpen,
}: Readonly<{ status: LinkedinStatus; open: boolean; onOpen: () => void }>) {
  const t = useT();
  if (status === "connected") {
    return (
      <span className="ob-connect-linkedin-done">
        <Check aria-hidden /> {t("ob.conv.connect.linkedinConnected")}
      </span>
    );
  }
  if (status === "skipped") {
    return (
      <span className="ob-connect-linkedin-done">
        {t("ob.conv.connect.linkedinSkippedNote")}
      </span>
    );
  }
  return (
    <Button small disabled={open} onClick={onOpen}>
      {t("ob.conv.connect.linkedinConnect")}
    </Button>
  );
}

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
            <Check aria-hidden /> <b>{t(scope.lead)}</b> {t(scope.rest)}
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
      <div className="ob-conv-chips">
        <Button
          small
          variant="primary"
          disabled={trimmed === "" || pending}
          onClick={() => onConnect(trimmed)}
        >
          {t("ob.conv.linkedin.authorize")}
        </Button>
        <Button small variant="ghost" onClick={onSkip}>
          {t("ob.conv.linkedin.skip")}
        </Button>
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

import { api, QueryStates, throwProblem } from "@margince/frontend/api";
import {
  formatDateTime,
  useCan,
  useCanWrite,
  useLocale,
  useT,
} from "@margince/frontend/app";
import {
  Badge,
  Button,
  Callout,
  Card,
  type Fact,
  FactList,
  SectionHeader,
} from "@margince/frontend/design-system";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { CaptureCard } from "./capturecard";
import {
  CONNECTION_OBJECT,
  type Connection,
  isHeld,
  STATUS_KEY,
  useConnectionStatus,
} from "./connection";

// #/ext/zalo-personal — the one screen a member uses to connect THEIR OWN
// personal Zalo account, by scanning a QR code with their phone.
//
// THE CONSENT PANEL IS THE PRODUCT HERE, not decoration around the button. The
// credential this screen mints can read the member's entire personal chat life
// — their family, their doctor, their other employer — so the things they have
// to know before they scan are stated as prose they read once: that nothing is
// captured until they choose which conversations to allow, that this is their
// own account and they can withdraw it whenever they like, and what they do and
// do not get on the timeline.
//
// AND TWO DISCLOSURES OUTLIVE IT ({@link ConnectionLimits}), which is why they are
// not paragraphs in that panel: a constraint that only appears while somebody is
// deciding to connect is a constraint they meet for the first time after it has
// already cost them something. The browser conflict is the one a connected rep
// walks into on an ordinary Tuesday, and a disclaimer that vanishes once you have
// agreed to it is a disclaimer nobody read. Both are therefore on the card in
// EVERY state, and they are two separate notices because they have nothing to do
// with each other — one is a thing to avoid doing, the other is what this
// connector is.
//
// AND THE CHOOSER IS WHERE THAT PROMISE IS KEPT — in capturecard.tsx, which owns
// the second card and the whole argument about what goes into the CRM. This file
// owns the shell and the CONNECTION: the QR login, what the account is, and how a
// member withdraws it.
//
// WHAT THE SCREEN DELIBERATELY DOES NOT SHOW: any credential. The status
// operation reports `session_deposited` as a yes-or-no and returns nothing else
// about the session, so there is nothing here that renders one, masked or
// otherwise, and nothing that asks for one back — the member's phone is the
// only place their Zalo login is ever typed.
//
// Everything is about the CALLER's own account: the four operations take no
// member argument at all, so there is no version of this screen that starts,
// watches or withdraws a colleague's login.

/** The locale type, derived from the hook — the core publishes the hook, not the type. */
type Locale = ReturnType<typeof useLocale>["locale"];

/**
 * How long the screen waits before asking again how the QR login is going.
 *
 * Short, because the WAIT lives on the server: one connect/status call spends a
 * few seconds inside Zalo's own long-poll before answering, so this interval is
 * the gap between those calls rather than the polling cadence itself. A member
 * standing with a phone in their hand should see "scanned" as soon as the
 * server knows it.
 *
 * Exported because the screen's own suite drives this poll on a fake clock, and
 * an advance measured against a hand-copied second spelling would UNDER-ADVANCE
 * the moment either moved: the timer would not fire, the case would assert on a
 * state that never changed, and it would pass while proving nothing.
 */
export const CONNECT_POLL_MS = 1_000;

/** How wide the QR renders. Only the width, so the provider's own image keeps its aspect. */
const QR_WIDTH = 240;

/**
 * The states a QR login can be in, as the contract declares them.
 *
 * A Set rather than a union used only at the type level, because the screen has
 * to decide at RUN TIME whether a body it was handed is one of them: an
 * unrecognised state rendered as "still waiting" would leave a member watching
 * a code that is already dead.
 */
const HANDSHAKE_STATES = new Set([
  "waiting",
  "scanned",
  "confirmed",
  "declined",
  "expired",
]);

type HandshakeState =
  | "waiting"
  | "scanned"
  | "confirmed"
  | "declined"
  | "expired";

function isHandshakeState(value: unknown): value is HandshakeState {
  return typeof value === "string" && HANDSHAKE_STATES.has(value);
}

/** Whether the login is over — either way, the code is spent and a new one is needed. */
function isOver(state?: HandshakeState): boolean {
  return state === "declined" || state === "expired";
}

/** Whether there is nothing left to ask: the server has deleted the handshake. */
function isSettled(state?: HandshakeState): boolean {
  return state === "confirmed" || isOver(state);
}

export default function ZaloPersonalScreen() {
  const t = useT();
  return (
    <div className="wrap narrow">
      {/* level 1: the app shell yields the page's name to a composed unit, so
          the screen's own top header IS the page's h1. */}
      <SectionHeader
        title={t("extZaloPersonal.title")}
        sub={t("extZaloPersonal.sub")}
        level={1}
      />
      <ConnectionCard />
      <CaptureCard />
    </div>
  );
}

function ConnectionCard() {
  const t = useT();
  const { locale } = useLocale();
  // The READER's own zone: "connected on" is only useful next to the clock on
  // the wall behind them, and nothing about a member's own account belongs to a
  // workspace-configured zone.
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const queryClient = useQueryClient();
  // Read decides whether this card has anything to say; update decides whether
  // a login can be started; delete decides whether the account can be
  // withdrawn. Three grants because they are three decisions an operator makes.
  const canRead = useCan(CONNECTION_OBJECT, "read");
  const canConnect = useCanWrite(CONNECTION_OBJECT, "update");
  const canDisconnect = useCanWrite(CONNECTION_OBJECT, "delete");
  const status = useConnectionStatus(canRead);

  const disconnect = useMutation({
    mutationFn: async () => {
      const { error, response } = await api.DELETE(
        "/ext/zalo-personal/disconnect",
      );
      if (error || !response.ok) {
        throwProblem(error);
      }
    },
    // onSettled rather than onSuccess: a request that failed did not
    // necessarily fail to DISCONNECT — a response lost on the way back leaves
    // the session deleted while the client sees an error. The screen re-reads
    // rather than asserting an outcome it cannot know.
    onSettled: async () => {
      await queryClient.invalidateQueries({ queryKey: STATUS_KEY });
    },
  });

  if (!canRead) {
    return (
      <Card>
        <p>{t("extZaloPersonal.noGrant")}</p>
      </Card>
    );
  }

  const connection = status.data?.connection;
  const held = isHeld(connection);
  // A session on deposit with NO usable connection behind it. The server seals
  // the credential before it writes the row, so a network or database failure
  // in between leaves a fully valid Zalo login held by this installation and
  // nothing on screen that admits to it. Withdrawing is the member's own
  // decision to make about their own private life, so the screen has to know
  // this state exists rather than reporting it as "not connected".
  const stranded = status.data?.sessionDeposited === true && !held;

  return (
    <Card>
      <SectionHeader
        title={t("extZaloPersonal.connection.title")}
        sub={t("extZaloPersonal.connection.sub")}
      />
      {/* Through the query gate, not off `status.data` directly: data is
          undefined both while the read is in flight and when it failed, and
          rendering either as "not connected" states something about the
          member's account that the read did not establish. */}
      <QueryStates query={status}>
        {held ? (
          <ConnectionFacts
            connection={connection}
            locale={locale}
            zone={zone}
          />
        ) : (
          <>
            <p>
              <Badge tone="warn">
                {t("extZaloPersonal.connection.absent")}
              </Badge>
            </p>
            {stranded ? (
              <p>{t("extZaloPersonal.connection.stranded")}</p>
            ) : null}
          </>
        )}
      </QueryStates>

      {/* In EVERY state, and above the invitation to scan rather than inside it:
          see the note on ConnectionLimits. */}
      <ConnectionLimits />

      {/* Only once the read has ANSWERED ONCE, and only while the connection is
          not working: an invitation to scan drawn before anything established
          the account's state is an invitation to replace a session that is
          already capturing. `status.data` stays undefined until a read
          succeeds, and afterwards react-query keeps the last good answer
          through a failing poll — a poll that stopped answering has not
          disconnected anybody. */}
      {status.data && !status.data.connected && canConnect ? (
        <>
          <ConsentPanel />
          <ConnectFlow locale={locale} zone={zone} />
        </>
      ) : null}

      {/* `.card-actions` rather than a bare Button: this verb follows prose and
          a fact list, neither of which carries space of its own. */}
      {/* `stranded` as well as `held`: disconnect deletes the sealed session
          whether or not a row points at it, so it is the ONLY control that can
          clear a credential the write never got round to recording. Gated on
          `held` alone, the one state that most needs it is the one state that
          does not offer it. */}
      {canDisconnect && (held || stranded) ? (
        <>
          <div className="card-actions">
            <Button
              variant="danger"
              disabled={disconnect.isPending}
              onClick={() => disconnect.mutate()}
            >
              {t("extZaloPersonal.connection.disconnect")}
            </Button>
          </div>
          {/* role="alert": a mutation failure appears AFTER the press that
              caused it, so a screen reader that has moved off the button
              announces nothing and the member is left believing the account was
              withdrawn. */}
          {disconnect.isError ? (
            <p role="alert" className="co-error">
              {t("extZaloPersonal.connection.disconnectFailed")}
            </p>
          ) : null}
        </>
      ) : null}
    </Card>
  );
}

/**
 * The two things about this connector that are true whether or not anybody is
 * connected right now.
 *
 * THE BROWSER CONFLICT IS THE OPERATIONAL FACT OF THIS WHOLE UNIT, and it was
 * measured in both directions inside ten minutes: our QR login bounced the
 * member's open Zalo Web tab back to the sign-in page, and the member signing
 * back in on that tab killed our session six minutes into its life. Zalo admits
 * one browser-shaped session per account and this connector IS one, so the two
 * evict each other — and every eviction costs a QR re-scan, because nothing
 * re-establishes a personal credential without the human and their phone.
 *
 * The PHONE AND ZALO PC DO NOT CONFLICT, and that half is load-bearing rather
 * than a softener: "stop using Zalo" would be false, a rep would find it false
 * within a day, and they would then discount everything else this screen says.
 * So the notice is written as what to do — keep using the two clients that work
 * — with the browser named as the one exception.
 *
 * THE DISCLAIMER CLAIMS ONLY WHAT WE CAN SUPPORT. Zalo publishes no interface
 * for personal accounts and this speaks the web client's own protocol, so it can
 * stop working when Zalo changes something, and it is not endorsed by them.
 * Nothing here says anything about account bans or terms of service: we have no
 * evidence in either direction, and a frightening claim we cannot substantiate
 * would be worse than the silence.
 *
 * `warn` for the first and `info` for the second, which is the tones' published
 * meaning rather than a choice about emphasis: one says something will go wrong
 * if you do nothing, the other carries no urgency at all. Neither is `live` —
 * both render with the page rather than in response to anything, and a notice
 * that interrupts a reader for a standing fact teaches them to ignore notices.
 */
function ConnectionLimits() {
  const t = useT();
  return (
    <>
      <Callout tone="warn" title={t("extZaloPersonal.limits.browserTag")}>
        {t("extZaloPersonal.limits.browser")}
      </Callout>
      <Callout tone="info" title={t("extZaloPersonal.limits.unofficialTag")}>
        {t("extZaloPersonal.limits.unofficial")}
      </Callout>
    </>
  );
}

/**
 * What a member sees about the account they connected: which one it is, whether
 * anything is being captured from it, and since when.
 *
 * `needs_reconnect` is the one state they have to act on, and the only honest
 * remedy is another scan on their phone — a session Zalo stopped accepting
 * cannot be repaired from here.
 */
function ConnectionFacts({
  connection,
  locale,
  zone,
}: Readonly<{ connection: Connection; locale: Locale; zone: string }>) {
  const t = useT();
  const needsReconnect = connection.status === "needs_reconnect";
  // Rows assembled as an array so an absent fact is dropped rather than drawn
  // empty: a blank value claims we know it and it is nothing.
  const facts: Fact[] = [
    {
      key: "account",
      term: t("extZaloPersonal.connection.account"),
      value: connection.zalo_uid,
    },
    {
      key: "capture",
      term: t("extZaloPersonal.connection.capture"),
      value: connection.capture_enabled
        ? t("extZaloPersonal.connection.captureOn")
        : t("extZaloPersonal.connection.captureOff"),
      note: t("extZaloPersonal.connection.captureNote"),
    },
  ];
  if (connection.connected_at) {
    facts.push({
      key: "since",
      term: t("extZaloPersonal.connection.since"),
      value: formatDateTime(connection.connected_at, locale, zone),
    });
  }
  return (
    <>
      <p>
        {needsReconnect ? (
          <Badge tone="warn">
            {t("extZaloPersonal.connection.needsReconnect")}
          </Badge>
        ) : (
          <Badge tone="success">
            {t("extZaloPersonal.connection.connected")}
          </Badge>
        )}{" "}
        {connection.display_name}
      </p>
      <FactList facts={facts} />
      {needsReconnect ? (
        <p>{t("extZaloPersonal.connection.needsReconnectNote")}</p>
      ) : null}
    </>
  );
}

/**
 * What the member is agreeing to, before they are offered a code to scan.
 *
 * Prose rather than a bulleted notice, and three short paragraphs rather than
 * one: a salesperson reads this once, standing up, with a phone in their hand.
 * Each paragraph is one fact they would otherwise learn the expensive way — by
 * finding a private conversation on a colleague's screen, or by opening the
 * timeline expecting last month and finding today.
 *
 * THE BROWSER CONFLICT USED TO BE A FOURTH PARAGRAPH HERE and is now a notice on
 * the card itself, because this panel is drawn only while nobody is connected:
 * the fact it states is one a connected rep needs on the day they open Zalo in a
 * browser, which is exactly the day this panel is not on screen.
 */
function ConsentPanel() {
  const t = useT();
  return (
    <Card inset>
      <SectionHeader title={t("extZaloPersonal.consent.title")} level={3} />
      <p>{t("extZaloPersonal.consent.capture")}</p>
      <p>{t("extZaloPersonal.consent.noHistory")}</p>
      <p>{t("extZaloPersonal.consent.personal")}</p>
    </Card>
  );
}

/**
 * The QR login: start it, then ask how it is going until it is over.
 *
 * The attempt counter is in the query key rather than the query being reset,
 * because a finished login is a DIFFERENT login from the next one: without it a
 * member who starts over after a declined scan would keep reading the cached
 * "declined" — the poll stops on a terminal state, so nothing would ever
 * replace it.
 */
function ConnectFlow({
  locale,
  zone,
}: Readonly<{ locale: Locale; zone: string }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [attempt, setAttempt] = useState(0);

  const start = useMutation({
    mutationFn: async () => {
      const { data, error, response } = await api.PUT(
        "/ext/zalo-personal/connect/start",
        { body: {} },
      );
      if (error || !response.ok) {
        throwProblem(error);
      }
      // No code is no login: rendering an empty <img> would leave a member
      // waiting to scan something that is not on the screen.
      if (typeof data?.qr_image !== "string") {
        throw new Error("the login carried no `qr_image`");
      }
      return { image: data.qr_image, expiresAt: data.expires_at };
    },
    onSuccess: () => setAttempt((n) => n + 1),
  });

  const handshake = useQuery({
    enabled: start.isSuccess,
    queryKey: ["ext", "zalo-personal", "connect", attempt],
    // The interval stops itself once the login is over: a terminal state means
    // the server has already deleted the handshake, so asking again would only
    // produce a 404 on a timer.
    refetchInterval: (query) =>
      isSettled(query.state.data?.state) ? false : CONNECT_POLL_MS,
    refetchOnWindowFocus: false,
    queryFn: async () => {
      const { data, error, response } = await api.POST(
        "/ext/zalo-personal/connect/status",
        { body: {} },
      );
      if (error || !response.ok) {
        throwProblem(error);
      }
      // A state this screen does not recognise is an error rather than a shrug:
      // treated as "still waiting" it would leave a member watching a dead code.
      if (!isHandshakeState(data?.state)) {
        throw new Error("the login status carried no known `state`");
      }
      return { state: data.state, displayName: data.display_name };
    },
  });

  const state = handshake.data?.state;
  useEffect(() => {
    if (state !== "confirmed") {
      return;
    }
    // The connection row exists the moment the scan is confirmed, so the card
    // re-reads it and this whole flow gives way to the connected state. The
    // promise has no second reader — QueryStates reports a failed re-read on
    // its own, and the 20s poll behind it arrives either way.
    void queryClient.invalidateQueries({ queryKey: STATUS_KEY });
  }, [state, queryClient]);

  return (
    <>
      {start.isSuccess ? (
        <HandshakeView
          code={start.data}
          state={state}
          displayName={handshake.data?.displayName}
          failed={handshake.isError}
          locale={locale}
          zone={zone}
        />
      ) : null}
      <div className="card-actions">
        <Button
          disabled={start.isPending}
          onClick={() => start.mutate()}
          variant={start.isSuccess ? "ghost" : "primary"}
        >
          {t(
            start.isSuccess
              ? "extZaloPersonal.connect.again"
              : "extZaloPersonal.connect.start",
          )}
        </Button>
      </div>
      {start.isError ? (
        <p role="alert" className="co-error">
          {t("extZaloPersonal.connect.startFailed")}
        </p>
      ) : null}
    </>
  );
}

/**
 * The code, and where the login has got to.
 *
 * The QR disappears once the login is over, because a code that Zalo has
 * already spent is a code nobody should be scanning — leaving it on screen
 * beside "this code expired" is an instruction and a contradiction at once.
 */
function HandshakeView({
  code,
  state,
  displayName,
  failed,
  locale,
  zone,
}: Readonly<{
  code: { image: string; expiresAt?: string };
  state?: HandshakeState;
  displayName?: string;
  failed: boolean;
  locale: Locale;
  zone: string;
}>) {
  const t = useT();
  return (
    <>
      {isOver(state) ? null : (
        <>
          <p>{t("extZaloPersonal.connect.how")}</p>
          {/* The provider's own data URL, rendered as it arrived. This unit
              encodes no QR of its own. */}
          <img
            src={code.image}
            alt={t("extZaloPersonal.connect.qrAlt")}
            width={QR_WIDTH}
          />
          {code.expiresAt ? (
            <p>
              {t("extZaloPersonal.connect.expires", {
                at: formatDateTime(code.expiresAt, locale, zone),
              })}
            </p>
          ) : null}
        </>
      )}
      {/* role="status": the login advances while the member is looking at their
          phone rather than at this element, so each step has to announce
          itself. */}
      <p role="status">{t(handshakeMessage(state, failed))}</p>
      {state === "scanned" && displayName ? (
        <p>{t("extZaloPersonal.connect.scannedAs", { name: displayName })}</p>
      ) : null}
    </>
  );
}

/**
 * What the member is told at each step.
 *
 * `declined` and `expired` are kept apart rather than folded into one "that did
 * not work": one of them means somebody pressed no on the phone and the other
 * means nobody pressed anything in time, and only the first is worth a second
 * thought before scanning again.
 */
function handshakeMessage(
  state: HandshakeState | undefined,
  failed: boolean,
): `extZaloPersonal.connect.${string}` {
  if (failed) {
    return "extZaloPersonal.connect.checkFailed";
  }
  switch (state) {
    case "scanned":
      return "extZaloPersonal.connect.scanned";
    case "confirmed":
      return "extZaloPersonal.connect.confirmed";
    case "declined":
      return "extZaloPersonal.connect.declined";
    case "expired":
      return "extZaloPersonal.connect.expired";
    default:
      return "extZaloPersonal.connect.waiting";
  }
}

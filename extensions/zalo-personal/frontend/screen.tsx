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
  Card,
  type Fact,
  FactList,
  SectionHeader,
} from "@margince/frontend/design-system";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";

// #/ext/zalo-personal — the one screen a member uses to connect THEIR OWN
// personal Zalo account, by scanning a QR code with their phone.
//
// THE CONSENT PANEL IS THE PRODUCT HERE, not decoration around the button. The
// credential this screen mints can read the member's entire personal chat life
// — their family, their doctor, their other employer — so the three things they
// have to know before they scan are stated as prose they read once: that
// nothing is captured until they choose which conversations to allow, that this
// is their own account and they can withdraw it whenever they like, and that
// Zalo WEB and this connection evict each other (their phone does not, which is
// the half that makes the warning actionable rather than frightening).
//
// AND IT SAYS "FROM NOW ON", never "your history". Personal Zalo has no history
// API: capture runs forward from the moment they connect, plus whatever is
// still sitting in Zalo's own offline queue, a depth nobody has measured. A
// member who connects expecting last month's conversation and finds three days
// has been misled by this screen, and the bug they file is a real one.
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

/** The RBAC object every operation on this screen gates on. */
const CONNECTION_OBJECT = "ext_zalo_personal_connection";

const STATUS_KEY = ["ext", "zalo-personal", "status"];

/**
 * How often the connection status re-reads while the screen is open.
 *
 * The row it reads changes when a capture run finds a dead session, which is
 * minutes apart at best; faster would spend requests on a fact that does not
 * move.
 */
const STATUS_POLL_MS = 20_000;

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

/**
 * One member's connection, as the MERGED contract declares it.
 *
 * DERIVED from the read rather than restated. A hand-written copy of the schema
 * is a second spelling that drifts in silence — a renamed field still compiles
 * against the copy and renders undefined — and it invites exactly what the
 * first draft of this file did: declaring `last_polled_at` because the contract
 * has one, while nothing on the screen ever read it.
 */
type Connection = NonNullable<
  NonNullable<ReturnType<typeof useConnectionStatus>["data"]>["connection"]
>;

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
    </div>
  );
}

/**
 * `enabled` is the caller's read grant rather than a convenience: without it an
 * ungranted seat fires a request the server answers 403 — and then fires it
 * again every {@link STATUS_POLL_MS}, because this query polls. What that seat
 * should see is "you were not granted this", not a failed read on a timer.
 */
function useConnectionStatus(enabled: boolean) {
  return useQuery({
    enabled,
    refetchInterval: STATUS_POLL_MS,
    queryKey: STATUS_KEY,
    queryFn: async () => {
      const { data, error, response } = await api.GET(
        "/ext/zalo-personal/status",
      );
      if (error || !response.ok) {
        throwProblem(error);
      }
      // The declared field or an error. `data.connected` absent is undefined,
      // which is falsey — so a body this screen could not read would render
      // "not connected", which is a claim about the member's own account made
      // from a read that produced nothing, and what it invites is a second scan
      // over a connection that is already working.
      if (typeof data?.connected !== "boolean") {
        throw new Error("the connection status carried no `connected` field");
      }
      // `session_deposited` is judged as strictly, and for a sharper reason: it
      // is how this screen learns that a credential is on deposit with no
      // usable connection behind it. Read as false when the body did not carry
      // it, a member with a STRANDED session would be shown "not connected" and
      // offered no way to withdraw the access this installation is holding —
      // which is the one state where being wrong costs them their privacy
      // rather than a click.
      if (typeof data.session_deposited !== "boolean") {
        throw new Error(
          "the connection status carried no `session_deposited` field",
        );
      }
      return {
        connected: data.connected,
        sessionDeposited: data.session_deposited,
        connection: data.connection,
      };
    },
  });
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
  // `!= null` rather than `!== undefined`, and it is the difference between a
  // screen that reports and a screen that dies: a body carrying an explicit
  // `"connection": null` passes an undefined-only guard, and `.status` then
  // throws mid-render — so the WHOLE card fails, taking the withdraw verb with
  // it, in a state whose honest reading is simply "not connected". The handler
  // omits the field today; resting on that is a producer detail, not a
  // property, and the rest of this read validates the body rather than
  // trusting it.
  const held = connection != null && connection.status !== "disconnected";
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
 * Prose rather than a bulleted notice, and four short paragraphs rather than
 * one: a salesperson reads this once, standing up, with a phone in their hand.
 * Each paragraph is one fact they would otherwise learn the expensive way — by
 * finding a private conversation on a colleague's screen, by losing their Zalo
 * Web session mid-conversation, or by opening the timeline expecting last
 * month and finding today.
 */
function ConsentPanel() {
  const t = useT();
  return (
    <Card inset>
      <SectionHeader title={t("extZaloPersonal.consent.title")} level={3} />
      <p>{t("extZaloPersonal.consent.capture")}</p>
      <p>{t("extZaloPersonal.consent.noHistory")}</p>
      <p>{t("extZaloPersonal.consent.personal")}</p>
      <p>{t("extZaloPersonal.consent.zaloWeb")}</p>
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

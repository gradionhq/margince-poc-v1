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
  EmptyState,
  type Fact,
  FactList,
  Field,
  SectionHeader,
  Select,
  type SelectOption,
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
// AND THE CHOOSER IS WHERE THAT PROMISE IS KEPT. The first version of this
// screen told a member "nothing is captured until you choose which
// conversations to allow" and then offered nothing to choose with — which, on a
// connector whose whole consent story is that the member is in control, is the
// opposite of what it says. The second card below is that control: their own
// contacts, the verdict each one currently carries, and a save. Default deny is
// the mechanism — a contact with no verdict is not captured — and the block
// verdict is a refinement on top of it rather than the thing doing the work.
//
// AND IT SAYS "FROM NOW ON", never "your history". Personal Zalo has no history
// API: capture runs forward from the moment they SAVE, plus whatever is still
// sitting in Zalo's own offline queue, a depth nobody has measured. A member who
// connects expecting last month's conversation and finds three days has been
// misled by this screen, and the bug they file is a real one.
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

const ROSTER_KEY = ["ext", "zalo-personal", "contacts"];

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

/**
 * What the roster can say about one contact, and what a member can CHOOSE, which
 * the contract deliberately makes two different sets.
 *
 * The read carries `none` — undecided, and therefore not captured. The save
 * carries only `allow` and `block`: leaving somebody out of the document is what
 * "undecided" means to the server, so there is no value that puts a contact back
 * into it. The screen follows that rather than papering over it — a member moves
 * a contact OUT of undecided, and takes capture back by blocking, which is a
 * decision rather than an erasure of one.
 *
 * Sets rather than unions used only at the type level, because both arrive over
 * the wire: a verdict this screen does not recognise must not be drawn as one it
 * does — "not chosen" rendered over a stored `block` would tell a member they
 * have not decided about somebody they deliberately shut out.
 */
const CONTACT_MODES = new Set(["allow", "block", "none"]);

const VERDICTS = new Set(["allow", "block"]);

type ContactMode = "allow" | "block" | "none";

type Verdict = "allow" | "block";

function isContactMode(value: unknown): value is ContactMode {
  return typeof value === "string" && CONTACT_MODES.has(value);
}

function isVerdict(value: unknown): value is Verdict {
  return typeof value === "string" && VERDICTS.has(value);
}

/**
 * One contact as the chooser draws it.
 *
 * `name` is what a reader sees and `displayName` is what Zalo actually said, and
 * they are kept apart because only the second may be SENT: the name falls back
 * to the channel id so a row is never blank, and posting that fallback back to
 * the server would store an id as somebody's name — which is exactly the value
 * the degraded list then reads people by.
 */
type Contact = Readonly<{
  id: string;
  name: string;
  displayName?: string;
  mode: ContactMode;
}>;

/** One verdict as the save sends it. */
type SavedVerdict = Readonly<{
  channel_user_id: string;
  mode: Verdict;
  display_name?: string;
}>;

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
      <CaptureCard />
    </div>
  );
}

/**
 * Whether this installation holds a connection worth acting on.
 *
 * `!= null` rather than `!== undefined`, and it is the difference between a
 * screen that reports and a screen that dies: a body carrying an explicit
 * `"connection": null` passes an undefined-only guard, and `.status` then throws
 * mid-render — so the WHOLE card fails, taking the withdraw verb with it, in a
 * state whose honest reading is simply "not connected". The handler omits the
 * field today; resting on that is a producer detail, not a property, and the
 * rest of the read validates the body rather than trusting it.
 *
 * Shared by both cards rather than spelled twice: the chooser and the withdraw
 * verb have to agree about what "there is an account here" means, and two copies
 * of that judgement are two chances to disagree.
 */
function isHeld(
  connection: Connection | null | undefined,
): connection is Connection {
  return connection != null && connection.status !== "disconnected";
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
      // The count is judged for its TYPE and not for its presence, which is the
      // one place this read is deliberately less strict than the two above.
      // Absent, it is a fact the server did not report and the chooser draws no
      // row for it; defaulted to zero it would be a claim — "nothing of yours is
      // armed" — made from a read that established nothing, and a member who
      // believes it goes looking for the choices they already saved. A count
      // that arrives as something other than a number is a malformed body, and
      // failing the read is how that gets noticed rather than rendered.
      // Read through `unknown` on purpose: the contract makes the field
      // required, so the generated type says `number` and nothing here would
      // narrow — while a real body can still arrive without it, and this is the
      // one field whose absence the screen survives.
      const count: unknown = data.allowed_count;
      if (count !== undefined && typeof count !== "number") {
        throw new Error("the connection status carried a non-numeric count");
      }
      return {
        connected: data.connected,
        sessionDeposited: data.session_deposited,
        allowedCount: count,
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
 * The member's own contacts, and their verdict on each.
 *
 * DEFAULT DENY is the mechanism, so the list is drawn as three verdicts rather
 * than as checkboxes: `none` is not an absent choice waiting to be tidied up, it
 * is the resting state in which nothing about that contact is captured. `block`
 * sits beside it as a refinement — a member who wants to be sure about one
 * person can say so — and neither of them is what does the work.
 *
 * The roster arrives from the server already merged: whatever Zalo reported,
 * plus every contact this member has already ruled on. So a roster call that
 * failed upstream leaves this card with the stored entries alone, and it still
 * works — the verdicts are editable and savable without Zalo answering at all,
 * which is the state a member must not be locked out of. That is also why a
 * missing `display_name` falls back to the channel id rather than to an empty
 * row: an entry nobody can name is still an entry somebody chose.
 *
 * `roster_available` is how the server SAYS which of those two answers this is,
 * and the screen repeats it rather than inferring: an empty degraded list and an
 * account with no contacts look identical from here and mean opposite things.
 */
function useContactRoster() {
  return useQuery({
    queryKey: ROSTER_KEY,
    queryFn: async () => {
      const { data, error, response } = await api.GET(
        "/ext/zalo-personal/contacts",
      );
      if (error || !response.ok) {
        throwProblem(error);
      }
      if (!Array.isArray(data?.contacts)) {
        throw new Error("the contact list carried no `contacts` array");
      }
      // Judged, because the contract makes it the difference between two things
      // a member must not be told interchangeably: "you know nobody" and "we
      // could not ask Zalo". Read as true when the body did not carry it, an
      // empty degraded list would read as an empty contact list, and a member
      // would go looking for people the server never got to name.
      if (typeof data.roster_available !== "boolean") {
        throw new Error("the contact list carried no `roster_available` field");
      }
      return {
        rosterAvailable: data.roster_available,
        contacts: data.contacts.map(readContact),
      };
    },
  });
}

/**
 * One roster entry, validated rather than trusted.
 *
 * A bad entry fails the whole read instead of being dropped, and the asymmetry
 * is deliberate: dropping one would show a member FEWER verdicts than they hold,
 * and the one that vanishes is as likely to be a block as an allow — a screen
 * quietly omitting somebody they shut out is worse than a screen that admits it
 * could not read the list.
 */
function readContact(
  entry: Readonly<{
    channel_user_id: string;
    display_name?: string;
    mode: string;
  }>,
): Contact {
  if (entry.channel_user_id === "") {
    throw new Error("a contact carried no `channel_user_id`");
  }
  if (!isContactMode(entry.mode)) {
    throw new Error("a contact carried no known `mode`");
  }
  return {
    id: entry.channel_user_id,
    name: entry.display_name ?? entry.channel_user_id,
    displayName: entry.display_name,
    mode: entry.mode,
  };
}

/**
 * The chooser: which of the member's conversations may be captured.
 *
 * It appears only once there is an account to choose FOR. Drawn beside the
 * invitation to scan it would ask somebody to rule on a contact list that does
 * not exist yet, and drawn while the status read is still in flight it would
 * claim an account this screen has not established.
 */
function CaptureCard() {
  const t = useT();
  const canRead = useCan(CONNECTION_OBJECT, "read");
  // The same grant that starts a login: choosing what is captured is an update
  // to the member's own connection, and the server judges it the same way.
  const canChoose = useCanWrite(CONNECTION_OBJECT, "update");
  // The same query key the connection card reads, so this is the same request
  // and not a second one.
  const status = useConnectionStatus(canRead);
  if (!canRead || !isHeld(status.data?.connection)) {
    return null;
  }
  const armed = status.data?.allowedCount;
  return (
    <Card>
      <SectionHeader
        title={t("extZaloPersonal.capture.title")}
        sub={t("extZaloPersonal.capture.sub")}
      />
      <p>{t("extZaloPersonal.capture.defaultDeny")}</p>
      <p>{t("extZaloPersonal.capture.forward")}</p>
      {/* BOTH DIRECTIONS, and the paragraph above is what binds it to the save:
          a member reading "your replies are captured" as "your past replies are
          captured" is the misreading this whole card is shaped to avoid. It
          matters because the consent panel actively recommends the phone app
          over Zalo Web — so the path the product recommends is the path whose
          answers would otherwise be missing from an allowed conversation. And
          it says how: Zalo delivers the message here as it delivers it to the
          member's other devices. Nothing in this CRM reads their phone. */}
      <p>{t("extZaloPersonal.capture.bothSides")}</p>
      {armed === undefined ? null : (
        <FactList
          facts={[
            {
              key: "armed",
              term: t("extZaloPersonal.capture.armed"),
              value: String(armed),
              note:
                armed === 0
                  ? t("extZaloPersonal.capture.armedNone")
                  : undefined,
            },
          ]}
        />
      )}
      <ContactChooser canChoose={canChoose} />
    </Card>
  );
}

/**
 * The list, the pending changes, and the save.
 *
 * The draft holds ONLY what the member has touched, and what gets sent is the
 * difference between it and what the server last reported. That is what makes a
 * lost response harmless: the re-read arrives, the draft now agrees with it, the
 * difference is empty, and the screen stops claiming there is anything unsaved —
 * without this component ever having to decide whether the save it did not hear
 * back from landed.
 */
function ContactChooser({ canChoose }: Readonly<{ canChoose: boolean }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const roster = useContactRoster();
  const [draft, setDraft] = useState<Readonly<Record<string, Verdict>>>({});

  const save = useMutation({
    // The verdicts arrive as a VARIABLE rather than off render state: the press
    // belongs to the render that drew the button, so what it carries cannot be
    // older than the list the member was looking at.
    mutationFn: async (entries: SavedVerdict[]) => {
      const { error, response } = await api.PUT(
        "/ext/zalo-personal/allowlist",
        { body: { entries } },
      );
      if (error || !response.ok) {
        throwProblem(error);
      }
    },
    // onSettled rather than onSuccess, and both keys: the save is what switches
    // capture on, so the connection's own card is stale too — and a request that
    // failed did not necessarily fail to SAVE.
    onSettled: async () => {
      await queryClient.invalidateQueries({ queryKey: ROSTER_KEY });
      await queryClient.invalidateQueries({ queryKey: STATUS_KEY });
    },
  });

  const contacts = roster.data?.contacts ?? [];
  const pending = pendingVerdicts(contacts, draft);

  return (
    <>
      <QueryStates query={roster}>
        <VerdictList
          contacts={contacts}
          degraded={roster.data?.rosterAvailable === false}
          draft={draft}
          // Frozen while a save is carrying these very verdicts: a control that
          // moved mid-flight would leave the member looking at a choice the
          // request in the air does not contain.
          disabled={!canChoose || save.isPending}
          onChoose={(id, mode) => setDraft(chooseIn(id, mode))}
        />
      </QueryStates>
      {pending.length === 0 ? null : (
        <p>{t("extZaloPersonal.capture.unsaved")}</p>
      )}
      {canChoose && contacts.length > 0 ? (
        <div className="card-actions">
          <Button
            disabled={pending.length === 0 || save.isPending}
            onClick={() => save.mutate(pending)}
          >
            {t(
              save.isPending
                ? "extZaloPersonal.capture.saving"
                : "extZaloPersonal.capture.save",
            )}
          </Button>
        </div>
      ) : null}
      {/* role="alert": the failure appears after the press that caused it, and a
          member who believes a save landed stops watching for the messages that
          are not arriving. */}
      {save.isError ? (
        <p role="alert" className="co-error">
          {t("extZaloPersonal.capture.saveFailed")}
        </p>
      ) : null}
    </>
  );
}

/**
 * The rows themselves: who, and what is decided about them.
 *
 * Its own component because the list has three honest shapes and the chooser
 * around it has three states of its own — a member's contacts, an account with
 * none yet, and a list Zalo did not answer for, against a draft, a save in
 * flight and a save that failed. Read as one function that was the moment the
 * degraded case arrived.
 */
function VerdictList({
  contacts,
  degraded,
  draft,
  disabled,
  onChoose,
}: Readonly<{
  contacts: readonly Contact[];
  degraded: boolean;
  draft: Readonly<Record<string, Verdict>>;
  disabled: boolean;
  onChoose: (id: string, mode: string) => void;
}>) {
  const t = useT();
  if (contacts.length === 0) {
    // The two empty lists are not the same claim: "you have no contacts" and
    // "Zalo did not answer" send a member to different places, and telling them
    // the first when the second is true invites a scan they do not need.
    return (
      <EmptyState>
        {t(
          degraded
            ? "extZaloPersonal.capture.emptyNoRoster"
            : "extZaloPersonal.capture.empty",
        )}
      </EmptyState>
    );
  }
  return (
    <>
      {/* The degraded list, admitted rather than presented as the whole truth:
          Zalo did not answer, so what is on screen is only what this member
          already chose — and the one thing they must not lose is the ability to
          change it. */}
      {degraded ? <p>{t("extZaloPersonal.capture.noRoster")}</p> : null}
      {contacts.map((contact) => (
        <Field key={contact.id} label={contact.name}>
          {(control) => (
            <Select
              {...control}
              options={verdictOptions(t)}
              value={draft[contact.id] ?? contact.mode}
              // The resting face of an UNDECIDED contact. `none` matches no
              // option, deliberately: it is where every contact starts and not a
              // state a member can choose their way back into, so the control
              // shows it and offers the two decisions instead.
              placeholder={t("extZaloPersonal.capture.modeNone")}
              disabled={disabled}
              onChange={(mode) => onChoose(contact.id, mode)}
            />
          )}
        </Field>
      ))}
    </>
  );
}

/**
 * What the save would send: the verdicts that DIFFER from what the server last
 * reported, and nothing else.
 *
 * An entry the document does not name is left exactly as it was, so sending only
 * the difference is what the contract asks for — and a member's untouched
 * contacts are not re-decided by a save they made about somebody else.
 */
function pendingVerdicts(
  contacts: readonly Contact[],
  draft: Readonly<Record<string, Verdict>>,
): SavedVerdict[] {
  const pending: SavedVerdict[] = [];
  for (const contact of contacts) {
    const chosen = draft[contact.id];
    if (chosen !== undefined && chosen !== contact.mode) {
      pending.push({
        channel_user_id: contact.id,
        mode: chosen,
        display_name: contact.displayName,
      });
    }
  }
  return pending;
}

/**
 * Record one verdict in the draft.
 *
 * A refused value THROWS rather than being ignored: the options come from this
 * file, so a value that is not one of them means the control and the vocabulary
 * have drifted apart — and a silent return would drop the member's choice while
 * leaving the row looking chosen.
 */
function chooseIn(id: string, mode: string) {
  if (!isVerdict(mode)) {
    throw new Error("the verdict picker offered an unknown mode");
  }
  return (current: Readonly<Record<string, Verdict>>) => ({
    ...current,
    [id]: mode,
  });
}

/**
 * The two decisions a member can make, in the order they read them.
 *
 * Undecided is not among them, because the contract has no way to say it: a save
 * carries `allow` or `block`, and leaving somebody out is what undecided means.
 * Offering a third option that could not be sent would be a control that quietly
 * failed — so the resting state is the control's FACE (see `placeholder`) and
 * these are the two ways out of it.
 */
function verdictOptions(t: ReturnType<typeof useT>): SelectOption[] {
  return [
    { value: "allow", label: t("extZaloPersonal.capture.modeAllow") },
    { value: "block", label: t("extZaloPersonal.capture.modeBlock") },
  ];
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

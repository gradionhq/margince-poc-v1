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
  Field,
  SectionHeader,
  TextInput,
} from "@margince/frontend/design-system";
import {
  type UseMutationResult,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { useState } from "react";

// #/ext/relay-probe — the one screen a member uses to connect their
// Relay account and watch the poll work.
//
// It lives in the UNIT's own tree as a pnpm workspace package, which is the
// supply-chain decision the tier already made for extensions/notes: unit-authored
// TSX is compiled into the SPA bundle, guarded by collectUnitFrontend (private
// package, correct name, react and react-query as PEERS so the host's single
// copy runs) and by check-ext-imports.sh (the core reachable only through the
// exports map).
//
// WHAT THE SCREEN DELIBERATELY DOES NOT SHOW: the token. No operation returns
// it, masked or otherwise, and there is nothing here that asks. What a stored
// credential can honestly produce is exactly what is rendered — that a
// connection exists, how far it has read, when it last ran, and whether the
// provider still accepts it.
//
// Everything is about the CALLER's own connection. The operations take no
// member argument at all, so there is no version of this screen that shows a
// colleague's inbox position.

/**
 * The locale type, derived from the hook rather than imported: the core's
 * exports map publishes the hook and not its type, and a unit inventing `string`
 * for it would compile here and fail at the formatter, which takes the closed
 * set.
 */
type Locale = ReturnType<typeof useLocale>["locale"];

/** The RBAC object every operation on this screen gates on. */
const CONNECTION_OBJECT = "ext_relay_probe_connection";

/**
 * How often the status re-reads while the screen is open.
 *
 * Slower than the poll's own cadence would leave a member watching a stale
 * screen after they connect; faster spends requests on a row that changes every
 * two minutes at most.
 */
const STATUS_POLL_MS = 20_000;

/**
 * What the token field shows when one is deposited.
 *
 * Bullets rather than a sentence, and a FIXED count rather than the token's
 * own length: the field's job here is to look like a filled field, and one
 * that grew with the secret would publish how long it is. The words that say
 * what this means are the field's hint, which is copy and translates; a row of
 * bullets is not language and does not.
 */
const STORED_TOKEN_MASK = "••••••••••••";

type Connection = {
  id: string;
  user_id: string;
  base_url: string;
  status: string;
  account_label?: string;
  provider_workspace_id?: string;
  high_water_mark: number;
  backfill_before?: number;
  last_polled_at?: string;
  last_error_class?: string;
  version: number;
};

export default function RelayProbeScreen() {
  const t = useT();
  return (
    <div className="wrap narrow">
      {/* level 1: the app shell yields the page's name to a composed unit, so
          the screen's own top header IS the page's h1. */}
      <SectionHeader
        title={t("extRelayProbe.title")}
        sub={t("extRelayProbe.sub")}
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
    queryKey: ["ext", "relay-probe", "status"],
    queryFn: async () => {
      const { data, error, response } = await api.GET(
        "/ext/relay-probe/status",
      );
      if (error || !response.ok) {
        throwProblem(error);
      }
      // The declared field or an error. `data.connected` absent is undefined,
      // which is falsey — so a body this screen could not read would render
      // "not connected", which is a claim about the member's account made from
      // a read that produced nothing, and it invites them to paste a token over
      // a connection that is already working.
      if (typeof data?.connected !== "boolean") {
        throw new Error("the connection status carried no `connected` field");
      }
      return {
        connected: data.connected,
        connection: data.connection as Connection | undefined,
      };
    },
  });
}

function ConnectionCard() {
  const t = useT();
  const { locale } = useLocale();
  // The READER's own zone: "last checked" is only useful next to the clock on
  // the wall behind them, and nothing about a member's own connection belongs
  // to a workspace-configured zone.
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const queryClient = useQueryClient();
  // Read decides whether this card has anything to say; update decides whether
  // a token can be deposited; delete decides whether it can be withdrawn. Three
  // separate grants because they are three separate decisions an operator
  // makes — a seat that may see the connection's state need not be able to
  // replace the credential behind it.
  const canRead = useCan(CONNECTION_OBJECT, "read");
  const canConnect = useCanWrite(CONNECTION_OBJECT, "update");
  const canDisconnect = useCanWrite(CONNECTION_OBJECT, "delete");
  const status = useConnectionStatus(canRead);

  const connect = useMutation({
    mutationFn: async (deposit: { baseURL: string; token: string }) => {
      const { error, response } = await api.PUT(
        "/ext/relay-probe/connect",
        {
          body: { base_url: deposit.baseURL, token: deposit.token },
        },
      );
      if (error || !response.ok) {
        throwProblem(error);
      }
    },
    // onSettled rather than onSuccess: a request that failed did not
    // necessarily fail to CONNECT — a response lost on the way back leaves the
    // credential deposited while the client sees an error. The screen re-reads
    // rather than asserting a rollback it cannot know about.
    onSettled: async () => {
      await queryClient.invalidateQueries({
        queryKey: ["ext", "relay-probe", "status"],
      });
    },
  });

  const disconnect = useMutation({
    mutationFn: async () => {
      const { error, response } = await api.DELETE(
        "/ext/relay-probe/disconnect",
      );
      if (error || !response.ok) {
        throwProblem(error);
      }
    },
    onSettled: async () => {
      await queryClient.invalidateQueries({
        queryKey: ["ext", "relay-probe", "status"],
      });
    },
  });

  if (!canRead) {
    return (
      <Card>
        <p>{t("extRelayProbe.noGrant")}</p>
      </Card>
    );
  }

  return (
    <Card>
      <SectionHeader
        title={t("extRelayProbe.connection.title")}
        sub={t("extRelayProbe.connection.sub")}
      />
      {/* Through the query gate, not off `status.data` directly: data is
          undefined both while the read is in flight and when it failed, and
          rendering either as "not connected" states something about the
          member's account that the read did not establish. */}
      <QueryStates query={status}>
        {status.data?.connected && status.data.connection ? (
          <ConnectionState
            connection={status.data.connection}
            locale={locale}
            zone={zone}
          />
        ) : (
          <p>
            <Badge tone="warn">
              {t("extRelayProbe.connection.absent")}
            </Badge>
          </p>
        )}
      </QueryStates>

      {/* Only once the read has ANSWERED ONCE. An empty deposit form drawn
          before then says "nothing is connected" before anything established
          it, and what that invites is pasting a token over a connection that is
          already working — so `status.data` is the gate, and it stays undefined
          until a read succeeds, whether the first one is still in flight or
          failed.

          After a success it is the LAST GOOD answer and react-query keeps it
          through a failing poll. That is deliberate rather than tolerated: a
          poll that stops answering has not withdrawn the member's connection,
          and blanking the form every twenty seconds on a flaky network is the
          same false claim in the other direction. What the member sees is the
          state that was last true, with QueryStates saying the read is
          failing. */}
      {canConnect && status.data ? (
        <>
          {/* Keyed by everything the form SEEDS FROM, not by the record's
              identity. The id is not enough: connect upserts on (workspace,
              member), so changing the deployment keeps the same row — a form
              keyed on the id alone would hold the URL it opened with while the
              stored one moved underneath it, and the next "Replace token" would
              submit the stale URL. That is not a cosmetic staleness: the
              server treats a different base_url as a DEPLOYMENT change and
              resets high_water_mark to 0, so the silent revert also wipes the
              member's read cursor and re-reads their history.

              Re-seeding through the key rather than deriving the fields at
              render, because a derived value would need a second rule for
              "edited" versus "as stored" and the two readings would disagree
              the first time a member cleared the field. */}
          <CredentialForm
            key={connectionSeed(status.data.connection)}
            connection={status.data.connection}
            connect={connect}
          />
          {/* role="alert", as QueryStates gives a read failure: a mutation
              failure appears AFTER the press that caused it, so a screen
              reader that is not on this element announces nothing and the
              member is left believing the account connected. */}
          {connect.isError ? (
            <p role="alert">
              {t("extRelayProbe.connection.connectFailed")}
            </p>
          ) : null}
        </>
      ) : null}

      {/* `.card-actions` rather than a bare Button: this verb follows the facts
          and the prose above it, neither of which carries space of its own, so
          without the row it sits against the last line of text. */}
      {canDisconnect && status.data?.connected ? (
        <>
          <div className="card-actions">
            <Button
              variant="danger"
              disabled={disconnect.isPending}
              onClick={() => disconnect.mutate()}
            >
              {t("extRelayProbe.connection.disconnect")}
            </Button>
          </div>
          {disconnect.isError ? (
            <p role="alert" className="co-error">
              {t("extRelayProbe.connection.disconnectFailed")}
            </p>
          ) : null}
        </>
      ) : null}
    </Card>
  );
}

/**
 * What a member fills in to connect, and what they see once something is
 * already deposited.
 *
 * A STORED TOKEN IS NEVER RENDERED — no operation returns it, masked or
 * otherwise — so the field says that one EXISTS rather than what it is, and it
 * is disabled while it does: an enabled empty box beside a working connection
 * reads as "no token set", which is the one thing it is not, and it invites
 * pasting over a credential that is already polling.
 *
 * Disabled rather than merely read-only because there is no partial edit to
 * make. `PUT /connect` requires `token` on every call, so the only change this
 * contract can express is a whole replacement — which is what the button says,
 * in the words of what it does.
 */
/**
 * The identity of what the deposit form seeds itself from.
 *
 * Every stored value the form takes a starting value from belongs here, so a
 * change to one of them remounts the form over the new record. Today that is
 * the deployment URL beside the row's own id; a field seeded from something
 * else later has to join it, or the form will keep a value the server no
 * longer holds.
 */
function connectionSeed(connection?: Connection): string {
  if (!connection) {
    return "absent";
  }
  return `${connection.id}:${connection.base_url}`;
}

function CredentialForm({
  connection,
  connect,
}: Readonly<{
  connection?: Connection;
  connect: UseMutationResult<void, Error, { baseURL: string; token: string }>;
}>) {
  const t = useT();
  // Seeded from the stored connection rather than left empty: a connected
  // account whose deployment URL renders as the example placeholder states
  // that nothing is set. The component is keyed on the connection, so this
  // initial value is re-taken whenever that record changes.
  const [baseURL, setBaseURL] = useState(connection?.base_url ?? "");
  const [token, setToken] = useState("");
  // Whether the member has asked to put a NEW credential in. A connection that
  // exists starts closed: the commonest reason to open this screen is to read
  // how far the poll has got, not to re-paste a token that works.
  const [replacing, setReplacing] = useState(false);
  const depositing = connection === undefined || replacing;

  const submit = () =>
    connect.mutate(
      { baseURL: baseURL.trim(), token: token.trim() },
      {
        // The token is cleared whatever the outcome, so a live credential is
        // never left sitting in a form field — the rule the parent's onSettled
        // used to carry. The form closes only on a SUCCESS, because a failure
        // the member can still read is a failure they can still act on.
        onSettled: () => setToken(""),
        onSuccess: () => setReplacing(false),
      },
    );

  return (
    <>
      <Field label={t("extRelayProbe.connection.baseUrlLabel")}>
        {(control) => (
          <TextInput
            {...control}
            value={baseURL}
            disabled={!depositing}
            placeholder="https://workspace.example.com"
            onChange={(event) => setBaseURL(event.target.value)}
          />
        )}
      </Field>
      <Field
        label={t("extRelayProbe.connection.tokenLabel")}
        hint={
          depositing
            ? undefined
            : t("extRelayProbe.connection.tokenStored")
        }
      >
        {(control) =>
          depositing ? (
            <TextInput
              {...control}
              type="password"
              value={token}
              onChange={(event) => setToken(event.target.value)}
            />
          ) : (
            // The mask is a fixed width and not the token's own length: a
            // field that grew with the secret would leak how long it is.
            <TextInput
              {...control}
              value={STORED_TOKEN_MASK}
              disabled
              readOnly
            />
          )
        }
      </Field>
      <div className="form-actions">
        {depositing ? (
          <Button
            disabled={
              baseURL.trim() === "" || token.trim() === "" || connect.isPending
            }
            onClick={submit}
          >
            {t("extRelayProbe.connection.connect")}
          </Button>
        ) : (
          <Button variant="ghost" onClick={() => setReplacing(true)}>
            {t("extRelayProbe.connection.replaceToken")}
          </Button>
        )}
      </div>
    </>
  );
}

/**
 * What a connected account looks like: whether the provider still accepts the
 * token, how far the poll has read, and when it last ran.
 *
 * The error class is rendered as this unit's own vocabulary through the copy
 * catalogue, never as the provider's message — a remote party's prose is not
 * this installation's to display, and a class has a translation while a message
 * does not.
 */
function ConnectionState({
  connection,
  locale,
  zone,
}: {
  connection: Connection;
  locale: Locale;
  zone: string;
}) {
  const t = useT();
  const parked = connection.status === "reauth_required";
  // Rows the reader scans, assembled as an array so an absent fact is dropped
  // rather than drawn empty.
  const facts: Fact[] = [
    {
      key: "readto",
      term: t("extRelayProbe.connection.readTo"),
      value: connection.high_water_mark,
    },
  ];
  if (connection.backfill_before) {
    // Only when there IS a gap: a member who saw "catching up" on every screen
    // would learn to ignore it.
    facts.push({
      key: "catchingup",
      term: t("extRelayProbe.connection.catchingUp"),
      value: connection.backfill_before,
    });
  }
  if (connection.last_polled_at) {
    facts.push({
      key: "polled",
      term: t("extRelayProbe.connection.lastPolled"),
      value: formatDateTime(connection.last_polled_at, locale, zone),
    });
  }
  return (
    <>
      <p>
        {parked ? (
          <Badge tone="warn">
            {t("extRelayProbe.connection.parked")}
          </Badge>
        ) : (
          <Badge tone="success">
            {t("extRelayProbe.connection.connected")}
          </Badge>
        )}{" "}
        {connection.account_label}
      </p>
      <FactList numeric facts={facts} />
      {connection.last_error_class ? (
        <p>{t(`extRelayProbe.error.${connection.last_error_class}`)}</p>
      ) : null}
    </>
  );
}

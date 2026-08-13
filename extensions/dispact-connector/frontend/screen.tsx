import { api, QueryStates, throwProblem } from "@margince/frontend/api";
import { formatDateTime, useCan, useCanWrite, useLocale, useT } from "@margince/frontend/app";
import {
  Badge,
  Button,
  Card,
  Field,
  SectionHeader,
  TextInput,
} from "@margince/frontend/design-system";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

// #/ext/dispact-connector — the one screen a member uses to connect their
// Dispact account and watch the poll work.
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
const CONNECTION_OBJECT = "ext_dispact_connector_connection";

/**
 * How often the status re-reads while the screen is open.
 *
 * Slower than the poll's own cadence would leave a member watching a stale
 * screen after they connect; faster spends requests on a row that changes every
 * two minutes at most.
 */
const STATUS_POLL_MS = 20_000;

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

export default function DispactConnectorScreen() {
  const t = useT();
  return (
    <div className="wrap narrow">
      {/* level 1: the app shell yields the page's name to a composed unit, so
          the screen's own top header IS the page's h1. */}
      <SectionHeader
        title={t("extDispactConnector.title")}
        sub={t("extDispactConnector.sub")}
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
    queryKey: ["ext", "dispact-connector", "status"],
    queryFn: async () => {
      const { data, error, response } = await api.GET("/ext/dispact-connector/status");
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
      return { connected: data.connected, connection: data.connection as Connection | undefined };
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
  const [baseURL, setBaseURL] = useState("");
  const [token, setToken] = useState("");

  const connect = useMutation({
    mutationFn: async () => {
      const { error, response } = await api.PUT("/ext/dispact-connector/connect", {
        body: { base_url: baseURL.trim(), token: token.trim() },
      });
      if (error || !response.ok) {
        throwProblem(error);
      }
    },
    // onSettled rather than onSuccess: a request that failed did not
    // necessarily fail to CONNECT — a response lost on the way back leaves the
    // credential deposited while the client sees an error. The screen re-reads
    // rather than asserting a rollback it cannot know about, and the token
    // input is cleared either way so a live credential is not left sitting in
    // a form field.
    onSettled: async () => {
      setToken("");
      await queryClient.invalidateQueries({ queryKey: ["ext", "dispact-connector", "status"] });
    },
  });

  const disconnect = useMutation({
    mutationFn: async () => {
      const { error, response } = await api.DELETE("/ext/dispact-connector/disconnect");
      if (error || !response.ok) {
        throwProblem(error);
      }
    },
    onSettled: async () => {
      await queryClient.invalidateQueries({ queryKey: ["ext", "dispact-connector", "status"] });
    },
  });

  if (!canRead) {
    return (
      <Card>
        <p>{t("extDispactConnector.noGrant")}</p>
      </Card>
    );
  }

  return (
    <Card>
      <SectionHeader
        title={t("extDispactConnector.connection.title")}
        sub={t("extDispactConnector.connection.sub")}
      />
      {/* Through the query gate, not off `status.data` directly: data is
          undefined both while the read is in flight and when it failed, and
          rendering either as "not connected" states something about the
          member's account that the read did not establish. */}
      <QueryStates query={status}>
        {status.data?.connected && status.data.connection ? (
          <ConnectionState connection={status.data.connection} locale={locale} zone={zone} />
        ) : (
          <p>
            <Badge tone="warn">{t("extDispactConnector.connection.absent")}</Badge>
          </p>
        )}
      </QueryStates>

      {canConnect ? (
        <>
          <Field label={t("extDispactConnector.connection.baseUrlLabel")}>
            {(control) => (
              <TextInput
                {...control}
                value={baseURL}
                placeholder="https://workspace.example.com"
                onChange={(event) => setBaseURL(event.target.value)}
              />
            )}
          </Field>
          <Field label={t("extDispactConnector.connection.tokenLabel")}>
            {(control) => (
              <TextInput
                {...control}
                type="password"
                value={token}
                onChange={(event) => setToken(event.target.value)}
              />
            )}
          </Field>
          <Button
            disabled={baseURL.trim() === "" || token.trim() === "" || connect.isPending}
            onClick={() => connect.mutate()}
          >
            {t("extDispactConnector.connection.connect")}
          </Button>
          {connect.isError ? <p>{t("extDispactConnector.connection.connectFailed")}</p> : null}
        </>
      ) : null}

      {canDisconnect && status.data?.connected ? (
        <>
          <Button
            variant="danger"
            disabled={disconnect.isPending}
            onClick={() => disconnect.mutate()}
          >
            {t("extDispactConnector.connection.disconnect")}
          </Button>
          {disconnect.isError ? (
            <p>{t("extDispactConnector.connection.disconnectFailed")}</p>
          ) : null}
        </>
      ) : null}
    </Card>
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
  return (
    <>
      <p>
        {parked ? (
          <Badge tone="warn">{t("extDispactConnector.connection.parked")}</Badge>
        ) : (
          <Badge tone="success">{t("extDispactConnector.connection.connected")}</Badge>
        )}{" "}
        {connection.account_label}
      </p>
      <dl>
        <dt>{t("extDispactConnector.connection.readTo")}</dt>
        <dd>{connection.high_water_mark}</dd>
        {connection.backfill_before ? (
          <>
            {/* Shown only when there IS a gap, because a member seeing
                "catching up" on every screen would learn to ignore it. */}
            <dt>{t("extDispactConnector.connection.catchingUp")}</dt>
            <dd>{connection.backfill_before}</dd>
          </>
        ) : null}
        {connection.last_polled_at ? (
          <>
            <dt>{t("extDispactConnector.connection.lastPolled")}</dt>
            <dd>{formatDateTime(connection.last_polled_at, locale, zone)}</dd>
          </>
        ) : null}
      </dl>
      {connection.last_error_class ? (
        <p>{t(`extDispactConnector.error.${connection.last_error_class}`)}</p>
      ) : null}
    </>
  );
}

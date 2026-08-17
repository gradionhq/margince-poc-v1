import { api, problemMessageOf, QueryStates, throwProblem } from "@margince/frontend/api";
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

// #/ext/zalo-oa — the administrator screen for this installation's Zalo Official
// Account.
//
// ONE FORM, because connecting is one call. The browser authorization that mints
// the first token pair is run once, outside the product, with a tool; what an
// administrator brings here is the pair it produced. connect.go carries why.
//
// THE REFUSALS ARE THE SERVER'S OWN WORDS, not a static string. That is the whole
// difference between a screen that helps and one that does not, and it is what
// this connector's refusals are written for: an account on the free package and
// an app missing a permission group both fail to connect, and one of them costs
// 2.500.000 đ a year while the other costs a click. A card that said "it did not
// work" for both would send an operator to buy something they already have.
//
// WHAT THE SCREEN DELIBERATELY DOES NOT SHOW: any credential. No operation
// returns the app secret or either token, and nothing here asks for one back.

/**
 * The locale type, derived from the hook rather than imported: the core's
 * exports map publishes the hook and not its type, and a unit inventing `string`
 * for it would compile here and fail at the formatter, which takes the closed
 * set.
 */
type Locale = ReturnType<typeof useLocale>["locale"];

/** The RBAC object every operation on this screen gates on. */
const CONNECTION_OBJECT = "ext_zalo_oa_connection";

/**
 * How often the status re-reads while the screen is open. Slower than the poll's
 * own cadence would leave an administrator watching a stale screen after they
 * connect; faster spends requests on a row that changes every two minutes at
 * most.
 */
const STATUS_POLL_MS = 20_000;

type Connection = {
  id: string;
  oa_id: string;
  app_id: string;
  authorized_by: string;
  status: string;
  account_label?: string;
  package_name?: string;
  package_valid_through?: string;
  access_token_expires_at?: string;
  high_water_mark: number;
  backfill_before?: number;
  last_polled_at?: string;
  last_error_class?: string;
  version: number;
  poll_request_budget: number;
};

const STATUS_KEY = ["ext", "zalo-oa", "status"];

export default function ZaloOaScreen() {
  const t = useT();
  return (
    <div className="wrap narrow">
      {/* level 1: the app shell yields the page's name to a composed unit, so
          the screen's own top header IS the page's h1. */}
      <SectionHeader title={t("extZaloOa.title")} sub={t("extZaloOa.sub")} level={1} />
      <ConnectionCard />
    </div>
  );
}

/**
 * `enabled` is the caller's read grant rather than a convenience: without it an
 * ungranted seat fires a request the server answers 403 — and then fires it again
 * every {@link STATUS_POLL_MS}, because this query polls. What that seat should
 * see is "you were not granted this", not a failed read on a timer.
 */
function useConnectionStatus(enabled: boolean) {
  return useQuery({
    enabled,
    refetchInterval: STATUS_POLL_MS,
    queryKey: STATUS_KEY,
    queryFn: async () => {
      const { data, error, response } = await api.GET("/ext/zalo-oa/status");
      if (error || !response.ok) {
        throwProblem(error);
      }
      // The declared field or an error. `data.connected` absent is undefined,
      // which is falsey — so a body this screen could not read would render "not
      // connected", which is a claim about the installation made from a read that
      // produced nothing, and it invites an administrator to connect over one
      // that is already working.
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
  // the wall behind them.
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const queryClient = useQueryClient();
  // Read decides whether this card has anything to say; update decides whether an
  // account can be connected; delete decides whether it can be withdrawn. Three
  // separate grants because they are three separate decisions.
  const canRead = useCan(CONNECTION_OBJECT, "read");
  const canConnect = useCanWrite(CONNECTION_OBJECT, "update");
  const canDisconnect = useCanWrite(CONNECTION_OBJECT, "delete");
  const status = useConnectionStatus(canRead);
  const [appID, setAppID] = useState("");
  const [appSecret, setAppSecret] = useState("");
  const [accessToken, setAccessToken] = useState("");
  const [refreshToken, setRefreshToken] = useState("");
  // Whether the deposit form is open over a connection that already exists. It
  // is shut by default, because an empty credential form under a working
  // connection reads as "nothing is set up" — which is the one thing it is not.
  const [replacing, setReplacing] = useState(false);

  const connect = useMutation({
    mutationFn: async () => {
      const { error, response } = await api.PUT("/ext/zalo-oa/connect", {
        body: {
          app_id: appID.trim(),
          app_secret: appSecret.trim(),
          access_token: accessToken.trim(),
          refresh_token: refreshToken.trim(),
        },
      });
      if (error || !response.ok) {
        throwProblem(error);
      }
    },
    // Every credential is cleared whatever happened, so none is left sitting in a
    // form field on an unattended screen — and the refresh token especially, since
    // it is single-use and the one on screen is spent the moment this succeeds.
    // The screen re-reads rather than asserting a rollback it cannot know about:
    // a response lost on the way back leaves an account connected while the
    // client sees an error.
    onSettled: async () => {
      setAppSecret("");
      setAccessToken("");
      setRefreshToken("");
      await queryClient.invalidateQueries({ queryKey: STATUS_KEY });
    },
    onSuccess: () => setReplacing(false),
  });

  const disconnect = useMutation({
    mutationFn: async () => {
      const { error, response } = await api.DELETE("/ext/zalo-oa/disconnect");
      if (error || !response.ok) {
        throwProblem(error);
      }
    },
    onSettled: async () => {
      // The connect failure is cleared too: a refusal left standing under a card
      // that now says "not connected" describes an attempt nobody can act on any
      // more.
      connect.reset();
      setReplacing(false);
      await queryClient.invalidateQueries({ queryKey: STATUS_KEY });
    },
  });

  if (!canRead) {
    return (
      <Card>
        <p>{t("extZaloOa.noGrant")}</p>
      </Card>
    );
  }

  const connected = status.data?.connection !== undefined;
  const ready =
    appID.trim() !== "" &&
    appSecret.trim() !== "" &&
    accessToken.trim() !== "" &&
    refreshToken.trim() !== "";

  return (
    <Card>
      <SectionHeader title={t("extZaloOa.connection.title")} sub={t("extZaloOa.connection.sub")} />
      {/* Through the query gate, not off `status.data` directly: data is
          undefined both while the read is in flight and when it failed, and
          rendering either as "not connected" states something about the
          installation that the read did not establish. */}
      <QueryStates query={status}>
        {status.data?.connection ? (
          <ConnectionState connection={status.data.connection} locale={locale} zone={zone} />
        ) : (
          <p>
            <Badge tone="warn">{t("extZaloOa.connection.absent")}</Badge>
          </p>
        )}
      </QueryStates>

      {/* A connection that exists is DESCRIBED, not re-offered. The deposit form
          is drawn only when there is nothing connected or somebody has asked to
          replace what is — four empty credential boxes under a live connection
          say "not set up", and the credentials they would collect are single-use,
          so an accidental submit costs a real token. */}
      {canConnect && connected && !replacing ? (
        <>
          <p>{t("extZaloOa.connect.stored")}</p>
          <Button variant="ghost" onClick={() => setReplacing(true)}>
            {t("extZaloOa.connect.replace")}
          </Button>
        </>
      ) : null}

      {canConnect && (!connected || replacing) ? (
        <>
          <SectionHeader
            title={t(replacing ? "extZaloOa.connect.replaceTitle" : "extZaloOa.connect.title")}
            sub={t("extZaloOa.connect.sub")}
          />
          <Field label={t("extZaloOa.connect.appId")}>
            {(control) => (
              <TextInput
                {...control}
                value={appID}
                onChange={(event) => setAppID(event.target.value)}
              />
            )}
          </Field>
          <Field label={t("extZaloOa.connect.appSecret")}>
            {(control) => (
              <TextInput
                {...control}
                type="password"
                value={appSecret}
                onChange={(event) => setAppSecret(event.target.value)}
              />
            )}
          </Field>
          <Field label={t("extZaloOa.connect.accessToken")}>
            {(control) => (
              <TextInput
                {...control}
                type="password"
                value={accessToken}
                onChange={(event) => setAccessToken(event.target.value)}
              />
            )}
          </Field>
          <Field label={t("extZaloOa.connect.refreshToken")}>
            {(control) => (
              <TextInput
                {...control}
                type="password"
                value={refreshToken}
                onChange={(event) => setRefreshToken(event.target.value)}
              />
            )}
          </Field>
          <Button disabled={!ready || connect.isPending} onClick={() => connect.mutate()}>
            {t(replacing ? "extZaloOa.connect.replaceSubmit" : "extZaloOa.connect.submit")}
          </Button>
          {replacing ? (
            <Button variant="ghost" onClick={() => setReplacing(false)}>
              {t("extZaloOa.connect.cancel")}
            </Button>
          ) : null}
          {/* role="alert", because a mutation failure appears AFTER the press that
              caused it: a screen reader that is not on this element announces
              nothing, and the administrator is left believing the account
              connected.

              The SERVER'S OWN SENTENCE, with this card's copy only as the
              fallback for a failure nobody phrased for a reader. Every refusal
              worth acting on here — the package, the free console toggle, an
              expired access token, a refresh token already spent — is a
              distinction the server drew and a static string would throw away. */}
          {connect.isError ? (
            <p role="alert">
              {problemMessageOf(connect.error, t, t("extZaloOa.connect.failed"))}
            </p>
          ) : null}
        </>
      ) : null}

      {canDisconnect && connected ? (
        <>
          <Button
            variant="danger"
            disabled={disconnect.isPending}
            onClick={() => disconnect.mutate()}
          >
            {t("extZaloOa.connection.disconnect")}
          </Button>
          {disconnect.isError ? (
            <p role="alert">
              {problemMessageOf(disconnect.error, t, t("extZaloOa.connection.disconnectFailed"))}
            </p>
          ) : null}
        </>
      ) : null}
    </Card>
  );
}

/**
 * What a connection looks like: whether it is working, which account it is, what
 * package that account is on, how far the poll has read, and when it last ran.
 *
 * The error class is rendered as this unit's own vocabulary through the copy
 * catalogue, never as Zalo's message — a remote party's prose is not this
 * installation's to display, and a class has a translation while a message does
 * not.
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
  const working = connection.status === "connected";
  return (
    <>
      <p>
        {working ? (
          <Badge tone="success">{t("extZaloOa.connection.connected")}</Badge>
        ) : (
          <Badge tone="warn">{t(`extZaloOa.state.${connection.status}`)}</Badge>
        )}{" "}
        {connection.account_label}
      </p>
      <dl>
        {/* FIRST, and unconditionally: it is the one number here that governs how
            much of a busy account each check can keep up with, and Zalo publishes
            no per-account rate limit in any response header — so a ceiling nobody
            can read is a ceiling nobody can choose. */}
        <dt>{t("extZaloOa.connection.requestBudget")}</dt>
        <dd>{connection.poll_request_budget}</dd>
        {connection.package_name ? (
          <>
            <dt>{t("extZaloOa.connection.package")}</dt>
            <dd>
              {connection.package_name}
              {connection.package_valid_through ? ` — ${connection.package_valid_through}` : ""}
            </dd>
          </>
        ) : null}
        {connection.last_polled_at ? (
          <>
            <dt>{t("extZaloOa.connection.lastPolled")}</dt>
            <dd>{formatDateTime(connection.last_polled_at, locale, zone)}</dd>
          </>
        ) : null}
        {connection.access_token_expires_at ? (
          <>
            <dt>{t("extZaloOa.connection.renewsBy")}</dt>
            <dd>{formatDateTime(connection.access_token_expires_at, locale, zone)}</dd>
          </>
        ) : null}
        {connection.backfill_before ? (
          <>
            {/* Shown only when there IS a gap, because an administrator seeing
                "catching up" on every screen would learn to ignore it. */}
            <dt>{t("extZaloOa.connection.catchingUp")}</dt>
            <dd>{formatDateTime(new Date(connection.backfill_before).toISOString(), locale, zone)}</dd>
          </>
        ) : null}
      </dl>
      {connection.last_error_class ? (
        <p>{t(`extZaloOa.error.${connection.last_error_class}`)}</p>
      ) : null}
    </>
  );
}

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

// #/ext/zalo-oa — the administrator screen for this installation's Zalo Official
// Account.
//
// IT IS A TWO-STEP SCREEN because the provider makes it one. Zalo has no
// client-credentials grant: an OA administrator has to open a permission URL in
// a browser and click Cho phép, and the redirect that follows carries a code
// that is single-use and dies in ten minutes. So step one mints the URL, the
// administrator leaves, and step two redeems what they came back with.
//
// The second step asks for the WHOLE redirect address rather than for three
// fields picked out of it. That is one paste instead of three, and it is the
// only version in which the `state` genuinely comes back from the browser: a
// screen that filled the state in from what it already knew would be checking
// its own answer.
//
// WHAT THE SCREEN DELIBERATELY DOES NOT SHOW: any credential. No operation
// returns the app secret or either token, masked or otherwise, and there is
// nothing here that asks for one back.

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
  oa_id?: string;
  app_id: string;
  redirect_uri: string;
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
};

type Started = { permission_url: string; code_challenge: string };

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
      // produced nothing, and it invites an administrator to start a second
      // authorization over one that is already working.
      if (typeof data?.connected !== "boolean") {
        throw new Error("the connection status carried no `connected` field");
      }
      return { connected: data.connected, connection: data.connection as Connection | undefined };
    },
  });
}

/**
 * Reads the three values Zalo puts on the redirect out of the address an
 * administrator pasted.
 *
 * It refuses rather than guesses. A pasted address missing any of them is a
 * redirect that did not complete — the administrator cancelled, or copied the
 * permission URL instead of the one they landed on — and sending a request built
 * from the pieces that ARE there would spend the ten-minute code on a call that
 * cannot succeed.
 */
export function redemptionFrom(pasted: string): { code: string; oa_id: string; state: string } | null {
  let query: URLSearchParams;
  try {
    query = new URL(pasted.trim()).searchParams;
  } catch {
    return null;
  }
  const code = query.get("code") ?? "";
  const oaID = query.get("oa_id") ?? "";
  const state = query.get("state") ?? "";
  if (code === "" || oaID === "" || state === "") {
    return null;
  }
  return { code, oa_id: oaID, state };
}

function ConnectionCard() {
  const t = useT();
  const { locale } = useLocale();
  // The READER's own zone: "last checked" is only useful next to the clock on
  // the wall behind them.
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const queryClient = useQueryClient();
  // Read decides whether this card has anything to say; update decides whether an
  // authorization can be started or completed; delete decides whether it can be
  // withdrawn. Three separate grants because they are three separate decisions.
  const canRead = useCan(CONNECTION_OBJECT, "read");
  const canConnect = useCanWrite(CONNECTION_OBJECT, "update");
  const canDisconnect = useCanWrite(CONNECTION_OBJECT, "delete");
  const status = useConnectionStatus(canRead);
  const [appID, setAppID] = useState("");
  const [appSecret, setAppSecret] = useState("");
  const [redirectURI, setRedirectURI] = useState("");
  const [redirected, setRedirected] = useState("");
  const [started, setStarted] = useState<Started | null>(null);

  const authorize = useMutation({
    mutationFn: async () => {
      const { data, error, response } = await api.POST("/ext/zalo-oa/authorize", {
        body: {
          app_id: appID.trim(),
          app_secret: appSecret.trim(),
          redirect_uri: redirectURI.trim(),
        },
      });
      if (error || !response.ok) {
        throwProblem(error);
      }
      if (typeof data?.permission_url !== "string" || typeof data?.code_challenge !== "string") {
        throw new Error("the authorization carried no permission URL");
      }
      return { permission_url: data.permission_url, code_challenge: data.code_challenge };
    },
    onSuccess: (result) => setStarted(result),
    // The secret is cleared whatever happened, so a live credential is not left
    // sitting in a form field — and the screen re-reads rather than asserting a
    // rollback it cannot know about, because a response lost on the way back
    // leaves the secret deposited while the client sees an error.
    onSettled: async () => {
      setAppSecret("");
      await queryClient.invalidateQueries({ queryKey: STATUS_KEY });
    },
  });

  const connect = useMutation({
    mutationFn: async () => {
      const redemption = redemptionFrom(redirected);
      if (redemption === null) {
        throw new Error("the pasted address carries no authorization code");
      }
      const { error, response } = await api.PUT("/ext/zalo-oa/connect", { body: redemption });
      if (error || !response.ok) {
        throwProblem(error);
      }
    },
    onSettled: async () => {
      setRedirected("");
      await queryClient.invalidateQueries({ queryKey: STATUS_KEY });
    },
  });

  const disconnect = useMutation({
    mutationFn: async () => {
      const { error, response } = await api.DELETE("/ext/zalo-oa/disconnect");
      if (error || !response.ok) {
        throwProblem(error);
      }
    },
    onSettled: async () => {
      setStarted(null);
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

      {canConnect ? (
        <>
          <SectionHeader title={t("extZaloOa.step1.title")} sub={t("extZaloOa.step1.sub")} />
          <Field label={t("extZaloOa.step1.appId")}>
            {(control) => (
              <TextInput
                {...control}
                value={appID}
                onChange={(event) => setAppID(event.target.value)}
              />
            )}
          </Field>
          <Field label={t("extZaloOa.step1.appSecret")}>
            {(control) => (
              <TextInput
                {...control}
                type="password"
                value={appSecret}
                onChange={(event) => setAppSecret(event.target.value)}
              />
            )}
          </Field>
          <Field label={t("extZaloOa.step1.redirectUri")}>
            {(control) => (
              <TextInput
                {...control}
                value={redirectURI}
                placeholder="https://crm.example.com/zalo-callback"
                onChange={(event) => setRedirectURI(event.target.value)}
              />
            )}
          </Field>
          <Button
            disabled={
              appID.trim() === "" ||
              appSecret.trim() === "" ||
              redirectURI.trim() === "" ||
              authorize.isPending
            }
            onClick={() => authorize.mutate()}
          >
            {t("extZaloOa.step1.start")}
          </Button>
          {/* role="alert", as QueryStates gives a read failure: a mutation
              failure appears AFTER the press that caused it, so a screen reader
              that is not on this element announces nothing and the administrator
              is left believing the authorization started. */}
          {authorize.isError ? <p role="alert">{t("extZaloOa.step1.failed")}</p> : null}

          {started ? <StartedInstructions started={started} /> : null}

          <SectionHeader title={t("extZaloOa.step2.title")} sub={t("extZaloOa.step2.sub")} />
          <Field label={t("extZaloOa.step2.redirected")}>
            {(control) => (
              <TextInput
                {...control}
                value={redirected}
                onChange={(event) => setRedirected(event.target.value)}
              />
            )}
          </Field>
          <Button
            disabled={redemptionFrom(redirected) === null || connect.isPending}
            onClick={() => connect.mutate()}
          >
            {t("extZaloOa.step2.finish")}
          </Button>
          {connect.isError ? <p role="alert">{t("extZaloOa.step2.failed")}</p> : null}
        </>
      ) : null}

      {canDisconnect && status.data?.connection ? (
        <>
          <Button
            variant="danger"
            disabled={disconnect.isPending}
            onClick={() => disconnect.mutate()}
          >
            {t("extZaloOa.connection.disconnect")}
          </Button>
          {disconnect.isError ? (
            <p role="alert">{t("extZaloOa.connection.disconnectFailed")}</p>
          ) : null}
        </>
      ) : null}
    </Card>
  );
}

/**
 * What the administrator does next, shown only after an authorization has been
 * started.
 *
 * The code challenge is rendered rather than hidden because the developer console
 * stores ONE challenge per application instead of one per request, so it has to
 * be pasted there before the permission URL is opened. That is the provider's
 * design; saying so is the only alternative to silently reusing a verifier, and a
 * PKCE verifier that never changes is not one.
 *
 * The permission URL is text and not a link: it carries this installation's app
 * id and a challenge, and it is meant to be opened by whoever administers the
 * Official Account, who is not always the person at this screen.
 */
function StartedInstructions({ started }: { started: Started }) {
  const t = useT();
  return (
    <dl>
      <dt>{t("extZaloOa.step1.challenge")}</dt>
      <dd>
        <code>{started.code_challenge}</code>
      </dd>
      <dt>{t("extZaloOa.step1.permissionUrl")}</dt>
      <dd>
        <code>{started.permission_url}</code>
      </dd>
    </dl>
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

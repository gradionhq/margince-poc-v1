// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  clearPendingAuthorize,
  stashPendingAuthorize,
} from "../app/pendingauthorize";
import { navigate } from "../app/router";
import { Button, Card, EmptyState } from "../design-system/atoms";
import { PassportSelect, ScopeChips } from "../design-system/passportselect";
import { formatDate } from "../format/format";
import { useLocale, useT } from "../i18n";
import { problemMessage, QueryGate, useMe } from "./common";

// The human hands an agent their own authority here — the one screen where
// that decision is made, and the only one: the api serves no HTML, so there is
// no other surface a consent decision can be taken on. GET /oauth/authorize
// arms a single-use nonce, sets an HttpOnly Path=/oauth/authorize cookie
// carrying its counterpart, and 302s to `/#/oauth-consent?…&consent=<nonce>`.
// The nonce is deliberately absent from the consent-request endpoint's
// response (the cookie never reaches it), so it is read out of the redirect
// fragment instead and POSTed back — the POST proves possession of both
// halves. Every one of these params must ride the POST unchanged, because
// the server re-validates the whole request against what the GET armed.
const AUTHORIZE_PARAMS = [
  "response_type",
  "client_id",
  "redirect_uri",
  "scope",
  "code_challenge",
  "code_challenge_method",
  "resource",
  "state",
] as const;

function fragmentParams(): URLSearchParams {
  return new URLSearchParams(globalThis.location.hash.split("?")[1] ?? "");
}

// The un-consented authorize query the human returns to after minting a
// passport — every fragment param EXCEPT the nonce. Replaying the nonce
// would defeat the point of it being single-use and cookie-bound: the mint
// trip navigates away from /oauth/authorize entirely, so re-entering it is
// the only way to arm a fresh one.
function reauthorizeUrl(params: URLSearchParams): string {
  const carried = new URLSearchParams();
  for (const key of AUTHORIZE_PARAMS) {
    const value = params.get(key);
    if (value !== null) {
      carried.set(key, value);
    }
  }
  return `/oauth/authorize?${carried.toString()}`;
}

// The hidden fields both the Authorise and the Cancel form share: the whole
// authorize request plus the nonce, carried through untouched.
function HiddenAuthorizeFields({
  params,
  consent,
}: Readonly<{ params: URLSearchParams; consent: string }>) {
  return (
    <>
      {AUTHORIZE_PARAMS.map((key) => {
        const value = params.get(key);
        return value === null ? null : (
          <input key={key} type="hidden" name={key} value={value} />
        );
      })}
      <input type="hidden" name="consent" value={consent} />
    </>
  );
}

type ConsentRequest = components["schemas"]["ConsentRequest"];

// A passport may carry no label at all (the server maps a NULL column to ""
// rather than failing the read) — on the one screen where knowing which
// credential you are about to lend is the entire point, a blank <option>
// makes two such passports indistinguishable. The id fragment is not
// decorative: it is the only thing left that still tells them apart.
function passportLabel(
  option: Readonly<{ id: string; label: string }>,
  t: ReturnType<typeof useT>,
): string {
  return option.label.trim() === ""
    ? t("consent.unnamedPassport", { id: option.id.slice(0, 8) })
    : option.label;
}

// A refusal with no forward action of its own — the recovery lives back at
// the client, not on this screen — so it gets the one thing every other
// state here already has: a way out of a rail-less screen that would
// otherwise be a dead end.
function ConsentErrorCard({
  title,
  body,
}: Readonly<{ title: string; body: string }>) {
  const t = useT();
  return (
    <Card>
      <h1>{title}</h1>
      <p>{body}</p>
      <Button variant="ghost" onClick={() => navigate({ screen: "home" })}>
        {t("consent.backToApp")}
      </Button>
    </Card>
  );
}

// I7: a guide, not a disabled button — there is no approve control at all
// to disable. The CTA is the only way forward, so no exit is offered here.
function ConsentGuide({
  clientName,
  params,
}: Readonly<{ clientName: string; params: URLSearchParams }>) {
  const t = useT();
  return (
    <Card>
      <h1>{t("consent.emptyTitle")}</h1>
      <p>{t("consent.emptyBody", { client: clientName })}</p>
      <Button
        variant="primary"
        onClick={() => {
          // I8: stash the re-entry URL (fresh nonce on return), not the
          // current one — the nonce this screen holds is spent the moment
          // the human leaves to mint a passport.
          stashPendingAuthorize({
            url: reauthorizeUrl(params),
            clientName,
          });
          navigate({ screen: "settings", id: "ai" });
        }}
      >
        {t("consent.emptyCta")}
      </Button>
    </Card>
  );
}

// The ordinary path: a passport list to lend from. Its own component (rather
// than an inline branch) so it can hold the one hook the I9 stash-clearing
// fix needs — a plain function called mid-render cannot.
function ConsentSelector({
  data,
  params,
  consent,
  errorCode,
  passportId,
  setPassportId,
}: Readonly<{
  data: ConsentRequest;
  params: URLSearchParams;
  consent: string;
  errorCode: string | null;
  passportId: string;
  setPassportId: (id: string) => void;
}>) {
  const t = useT();
  const { locale } = useLocale();

  // I9: the stash exists only to survive the round trip to mint a passport.
  // Reaching this screen with a usable list means that detour, if there was
  // one, is over — the stash must not outlive the request it represents, or
  // Settings goes on offering to "finish" a connection already decided.
  useEffect(() => {
    clearPendingAuthorize();
  }, []);

  const options = data.passports.map((option) => ({
    ...option,
    label: passportLabel(option, t),
  }));
  const selected =
    options.find((option) => option.id === passportId) ?? options[0];
  const effectiveId = passportId || selected.id;
  // Scopes the passport carries beyond what this client would actually get,
  // dimmed so the human sees the connection may receive less than the
  // passport allows — never hidden outright.
  const notGranted = new Set(
    selected.scopes.filter(
      (candidate) => !selected.granted.includes(candidate),
    ),
  );
  // The other half of that same disclosure: scopes the CLIENT asked for
  // that this passport cannot grant at all (never in its own scope set), so
  // "read" showing solid never reads as "the whole request was satisfied"
  // when the client also asked for "write".
  const requestedNotGranted = data.requested.filter(
    (scope) => !selected.granted.includes(scope),
  );

  return (
    <Card>
      <h1>{t("consent.title")}</h1>
      <p>{t("consent.asks", { client: data.client_name })}</p>
      {errorCode === "unlendable_passport" && (
        <div className="card card-inset">
          <strong>{t("consent.unlendableTitle")}</strong>
          <p className="t-small">
            {t("consent.unlendableBody", { client: data.client_name })}
          </p>
        </div>
      )}
      <p>{t("consent.lend")}</p>
      <PassportSelect
        options={options}
        value={effectiveId}
        onChange={setPassportId}
      />
      <div
        style={{
          display: "flex",
          gap: "var(--space-1)",
          flexWrap: "wrap",
          marginTop: "var(--space-2)",
        }}
      >
        <ScopeChips scopes={selected.scopes} dim={notGranted} />
      </div>
      <p className="t-small">{t("consent.grantedNote")}</p>
      {requestedNotGranted.length > 0 && (
        <div style={{ marginTop: "var(--space-2)" }}>
          <p className="t-small">
            {t("consent.requestedNotGranted", { client: data.client_name })}
          </p>
          <div
            style={{
              display: "flex",
              gap: "var(--space-1)",
              flexWrap: "wrap",
              marginTop: "var(--space-1)",
            }}
          >
            <ScopeChips
              scopes={requestedNotGranted}
              dim={new Set(requestedNotGranted)}
            />
          </div>
        </div>
      )}
      <p className="t-small">
        {t("consent.expires", {
          date: formatDate(selected.expires_at, locale, "Europe/Berlin"),
        })}
      </p>
      {data.offline && <p>{t("consent.offline")}</p>}
      <div
        style={{
          display: "flex",
          gap: "var(--space-2)",
          marginTop: "var(--space-3)",
        }}
      >
        <form
          method="post"
          action="/oauth/authorize"
          onSubmit={() => clearPendingAuthorize()}
        >
          <HiddenAuthorizeFields params={params} consent={consent} />
          <input type="hidden" name="passport_id" value={effectiveId} />
          <Button type="submit" variant="primary">
            {t("consent.approve")}
          </Button>
        </form>
        <form
          method="post"
          action="/oauth/authorize"
          onSubmit={() => clearPendingAuthorize()}
        >
          <HiddenAuthorizeFields params={params} consent={consent} />
          <input type="hidden" name="deny" value="1" />
          <Button type="submit">{t("consent.deny")}</Button>
        </form>
      </div>
    </Card>
  );
}

export function OAuthConsent() {
  const t = useT();
  // Read once per mount: the fragment is the SPA's own address bar for this
  // screen, not a value that changes while it's open.
  const params = useMemo(fragmentParams, []);
  const clientId = params.get("client_id") ?? "";
  const scope = params.get("scope") ?? "";
  const consent = params.get("consent") ?? "";
  const errorCode = params.get("error");
  const me = useMe();
  const [passportId, setPassportId] = useState("");

  const query = useQuery({
    queryKey: ["oauth-consent-request", clientId, scope],
    // No point fetching a passport list for a render that's about to
    // navigate away (the no-nonce, no-error re-entry case below) — nothing
    // reads `query` until that branch has already returned.
    enabled: Boolean(consent) || Boolean(errorCode),
    queryFn: async () => {
      const { data, error } = await api.GET("/oauth/consent-request", {
        params: { query: { client_id: clientId, scope } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  // The not-signed-in case 302s here with no nonce at all (rather than a
  // bare 401), so App's own auth gate can render the login screen in place
  // with the hash preserved. OAuthConsent only ever mounts once that gate
  // has resolved PAST pending/error (App.tsx's AuthedApp only renders
  // ScreenView then) — so `me.data` here is not a guess about whether a
  // session exists, it is the same signal the gate already used to decide
  // this screen renders at all. Once it does, re-enter /oauth/authorize
  // with the same params (still nonce-free — a fresh one is only ever
  // minted server-side) to obtain the nonce this render is missing.
  //
  // Loop freedom: this effect fires only while BOTH `consent` and
  // `errorCode` are absent. The server's own contract (oauth_consentscreen.go)
  // means a request replayed with a session already accepted here always
  // resolves to one or the other on the next redirect — never back to the
  // bare no-nonce shape this effect reacts to — so it cannot re-arm itself a
  // second time for the same visit. A session that never resolves (the
  // structurally-unreachable case where this component mounts without one)
  // leaves this screen showing "reconnecting" forever rather than looping —
  // a dead end is the safe failure, not a loop.
  useEffect(() => {
    if (!consent && !errorCode && me.data) {
      globalThis.location.assign(reauthorizeUrl(params));
    }
  }, [consent, errorCode, me.data, params]);

  if (!consent && !errorCode) {
    return (
      <div className="wrap narrow">
        <EmptyState>{t("consent.reentering")}</EmptyState>
      </div>
    );
  }

  return (
    <div className="wrap narrow">
      <QueryGate query={query}>
        {(data) => {
          if (errorCode === "stale_consent") {
            return (
              <ConsentErrorCard
                title={t("consent.staleTitle")}
                body={t("consent.staleBody", { client: data.client_name })}
              />
            );
          }
          if (errorCode === "invalid_request") {
            return (
              <ConsentErrorCard
                title={t("consent.invalidTitle")}
                body={t("consent.invalidBody", { client: data.client_name })}
              />
            );
          }
          if (data.passports.length === 0) {
            return (
              <ConsentGuide clientName={data.client_name} params={params} />
            );
          }
          return (
            <ConsentSelector
              data={data}
              params={params}
              consent={consent}
              errorCode={errorCode}
              passportId={passportId}
              setPassportId={setPassportId}
            />
          );
        }}
      </QueryGate>
    </div>
  );
}

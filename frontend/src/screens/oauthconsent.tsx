// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { api } from "../api/client";
import { stashPendingAuthorize } from "../app/pendingauthorize";
import { navigate } from "../app/router";
import { Button, Card } from "../design-system/atoms";
import { PassportSelect, ScopeChips } from "../design-system/passportselect";
import { formatDate } from "../format/format";
import { useLocale, useT } from "../i18n";
import { problemMessage, QueryGate } from "./common";

// The human hands an agent their own authority here — the one screen where
// that decision is made, since the server renders no HTML any more
// (refactor(oauth): hand the consent screen to the SPA). GET /oauth/authorize
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

export function OAuthConsent() {
  const t = useT();
  const { locale } = useLocale();
  // Read once per mount: the fragment is the SPA's own address bar for this
  // screen, not a value that changes while it's open.
  const params = useMemo(fragmentParams, []);
  const clientId = params.get("client_id") ?? "";
  const scope = params.get("scope") ?? "";
  const consent = params.get("consent") ?? "";
  const [passportId, setPassportId] = useState("");

  const query = useQuery({
    queryKey: ["oauth-consent-request", clientId, scope],
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

  return (
    <div className="wrap narrow">
      <QueryGate query={query}>
        {(data) => {
          if (data.passports.length === 0) {
            // I7: a guide, not a disabled button — there is no approve
            // control at all to disable. The CTA is the only way forward.
            return (
              <Card>
                <h1>{t("consent.emptyTitle")}</h1>
                <p>{t("consent.emptyBody", { client: data.client_name })}</p>
                <Button
                  variant="primary"
                  onClick={() => {
                    // I8: stash the re-entry URL (fresh nonce on return), not
                    // the current one — the nonce this screen holds is spent
                    // the moment the human leaves to mint a passport.
                    stashPendingAuthorize({
                      url: reauthorizeUrl(params),
                      clientName: data.client_name,
                    });
                    navigate({ screen: "settings", id: "ai" });
                  }}
                >
                  {t("consent.emptyCta")}
                </Button>
              </Card>
            );
          }

          const selected =
            data.passports.find((option) => option.id === passportId) ??
            data.passports[0];
          const effectiveId = passportId || selected.id;
          // Scopes the passport carries beyond what this client would
          // actually get, dimmed so the human sees the connection may
          // receive less than the passport allows — never hidden outright.
          const notGranted = new Set(
            selected.scopes.filter(
              (candidate) => !selected.granted.includes(candidate),
            ),
          );

          return (
            <Card>
              <h1>{t("consent.title")}</h1>
              <p>{t("consent.asks", { client: data.client_name })}</p>
              <p>{t("consent.lend")}</p>
              <PassportSelect
                options={data.passports}
                value={effectiveId}
                onChange={setPassportId}
              />
              <div
                style={{
                  display: "flex",
                  gap: 6,
                  flexWrap: "wrap",
                  marginTop: 8,
                }}
              >
                <ScopeChips scopes={selected.scopes} dim={notGranted} />
              </div>
              <p className="t-small">{t("consent.grantedNote")}</p>
              <p className="t-small">
                {t("consent.expires", {
                  date: formatDate(
                    selected.expires_at,
                    locale,
                    "Europe/Berlin",
                  ),
                })}
              </p>
              {data.offline && <p>{t("consent.offline")}</p>}
              <div style={{ display: "flex", gap: 8, marginTop: 12 }}>
                <form method="post" action="/oauth/authorize">
                  <HiddenAuthorizeFields params={params} consent={consent} />
                  <input type="hidden" name="passport_id" value={effectiveId} />
                  <Button type="submit" variant="primary">
                    {t("consent.approve")}
                  </Button>
                </form>
                <form method="post" action="/oauth/authorize">
                  <HiddenAuthorizeFields params={params} consent={consent} />
                  <input type="hidden" name="deny" value="1" />
                  <Button type="submit">{t("consent.deny")}</Button>
                </form>
              </div>
            </Card>
          );
        }}
      </QueryGate>
    </div>
  );
}

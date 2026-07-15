import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Lock } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { api } from "../api/client";
import {
  Button,
  EmptyState,
  SectionHeader,
  Skeleton,
} from "../design-system/atoms";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessage } from "./common";
import {
  type Draft,
  dirtyKeys,
  displayOn,
  initialDraft,
  type PurposeView,
  toChoices,
} from "./preferences.logic";
import "./preferences.css";

// The public, anonymous email preference center (G-6, B-E11.32): the page a
// recipient lands on from an unsubscribe/manage-preferences link. The token
// in the URL is the whole capability — no session, no workspace header (the
// api client's request middleware already carves out /v1/public/* for this).
// An unknown and a revoked token both read as absent (404): this surface
// must never become an oracle for whether an address is known.

// A purpose the workspace's catalog doesn't carry a bespoke sentence for
// falls back to prefs.wordingGeneric — a workspace can define arbitrary
// purposes, so this map is deliberately not exhaustive.
const WORDING_KEYS: Record<string, MessageKey> = {
  marketing_email: "prefs.wording.marketing_email",
  events: "prefs.wording.events",
};

export function PreferenceCenterScreen({
  token,
}: Readonly<{ token?: string }>) {
  const t = useT();
  if (!token) {
    return (
      <div className="pref-page">
        <EmptyState>{t("prefs.invalidLink")}</EmptyState>
      </div>
    );
  }
  return <PreferenceCenterBody token={token} />;
}

// Marks a 404 so the render branch can show the "link is no longer valid"
// copy without distinguishing an unknown token from a revoked one — either
// way the honest response is identical (never a consent-state oracle).
class LinkInvalidError extends Error {}
// Marks a 429 (the public edge rate-limits per-IP and per-token) so the
// render branch gives an honest retry message instead of a raw failure.
class RateLimitedError extends Error {}

function PreferenceCenterBody({ token }: Readonly<{ token: string }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const queryKey = ["preference-center", token];

  const center = useQuery({
    queryKey,
    retry: false,
    queryFn: async () => {
      const { data, error, response } = await api.GET(
        "/public/preferences/{token}",
        { params: { path: { token } } },
      );
      if (error) {
        if (response.status === 404) {
          throw new LinkInvalidError();
        }
        if (response.status === 429) {
          throw new RateLimitedError();
        }
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  const purposes: PurposeView[] = center.data?.purposes ?? [];

  // The wording rendered at each toggle IS the wording submitted for it —
  // one map, computed once per data load, read both for display and for the
  // save payload. An independent re-derivation at submit time would break
  // the passthrough invariant the moment the two computations drifted.
  const wordingByKey = useMemo(() => {
    const map: Record<string, string> = {};
    for (const purpose of purposes) {
      const wordingKey = WORDING_KEYS[purpose.key];
      map[purpose.key] = wordingKey
        ? t(wordingKey)
        : t("prefs.wordingGeneric", { label: purpose.label });
    }
    return map;
  }, [purposes, t]);

  const [draft, setDraft] = useState<Draft | null>(null);
  const [partialSave, setPartialSave] = useState(false);

  // Seeds (and re-seeds) the draft from the server's latest body — on first
  // load, after a successful save, and after the refetch a partial-save
  // recovery triggers. Never from the optimistic draft that prompted a save.
  useEffect(() => {
    if (center.data) {
      setDraft(initialDraft(center.data.purposes));
    }
  }, [center.data]);

  const save = useMutation({
    mutationFn: async () => {
      if (!draft) {
        // The Save button only renders once the draft is seeded — this
        // guard only protects a stale closure, never a real path.
        throw new Error("preferences not loaded yet");
      }
      const { data, error } = await api.PUT("/public/preferences/{token}", {
        params: { path: { token } },
        body: {
          choices: toChoices(purposes, draft, (key) => wordingByKey[key]),
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (data) => {
      // The server's latest body is authoritative — never the draft that
      // prompted the save.
      queryClient.setQueryData(queryKey, data);
      setPartialSave(false);
    },
    onError: () => {
      // UpdatePreferences loops choices in separate transactions
      // (handlers_public.go), so a mid-list failure leaves earlier choices
      // committed — re-read rather than let the local draft masquerade as
      // what actually applied.
      setPartialSave(true);
      queryClient.invalidateQueries({ queryKey });
    },
  });

  if (center.isPending) {
    return (
      <div className="pref-page">
        <div className="pref-center">
          <Skeleton width="60%" />
          <Skeleton width="90%" />
          <Skeleton width="75%" />
        </div>
      </div>
    );
  }

  if (center.isError) {
    const message =
      center.error instanceof RateLimitedError
        ? t("prefs.rateLimited")
        : center.error instanceof LinkInvalidError
          ? t("prefs.invalidLink")
          : (center.error as Error).message;
    return (
      <div className="pref-page">
        <EmptyState>{message}</EmptyState>
      </div>
    );
  }

  if (!draft) {
    return (
      <div className="pref-page">
        <div className="pref-center">
          <Skeleton width="60%" />
        </div>
      </div>
    );
  }

  const dirty = dirtyKeys(purposes, draft);

  return (
    <div className="pref-page">
      <div className="pref-center">
        <SectionHeader title={t("prefs.title")} sub={t("prefs.sub")} />
        <ul className="pref-list">
          {purposes.map((purpose) => (
            <PreferenceRow
              key={purpose.key}
              purpose={purpose}
              on={draft[purpose.key] ?? displayOn(purpose.state)}
              wording={wordingByKey[purpose.key]}
              t={t}
              onToggle={() =>
                setDraft((prev) =>
                  prev ? { ...prev, [purpose.key]: !prev[purpose.key] } : prev,
                )
              }
            />
          ))}
        </ul>

        {partialSave && (
          <div className="card card-inset pref-partial-banner">
            <p>{t("prefs.partialSave")}</p>
          </div>
        )}

        {dirty.length > 0 && (
          <div className="pref-save-bar">
            <p className="pref-not-saved">{t("prefs.notSaved")}</p>
            <p className="t-caption">
              {t("prefs.savePending", {
                changes: dirty
                  .map(
                    (key) =>
                      purposes.find((purpose) => purpose.key === key)?.label ??
                      key,
                  )
                  .join(", "),
              })}
            </p>
            <p className="t-caption">{t("prefs.saveProof")}</p>
            <div className="pref-save-actions">
              <Button
                onClick={() => {
                  setDraft(initialDraft(purposes));
                  setPartialSave(false);
                }}
              >
                {t("prefs.discard")}
              </Button>
              <Button
                variant="primary"
                disabled={save.isPending}
                onClick={() => save.mutate()}
              >
                {t("prefs.save")}
              </Button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

function PreferenceRow({
  purpose,
  on,
  wording,
  t,
  onToggle,
}: Readonly<{
  purpose: PurposeView;
  on: boolean;
  wording: string;
  t: ReturnType<typeof useT>;
  onToggle: () => void;
}>) {
  return (
    <li className="pref-row">
      <div className="pref-row-main">
        <div className="pref-row-head">
          {purpose.locked && <Lock className="pref-lock-icon" aria-hidden />}
          <span className="pref-label">{purpose.label}</span>
          {purpose.locked && (
            <span className="pref-lock-badge">{t("prefs.alwaysOn")}</span>
          )}
        </div>
        <p className="t-caption" data-testid={`wording-${purpose.key}`}>
          {wording}
        </p>
        <p className="t-caption pref-state">
          {on ? t("prefs.subscribed") : t("prefs.notSubscribed")}
        </p>
        {purpose.locked && (
          <p className="t-caption pref-locked-why">{t("prefs.lockedWhy")}</p>
        )}
      </div>
      <button
        type="button"
        role="switch"
        aria-checked={on}
        aria-label={purpose.label}
        disabled={purpose.locked}
        className={`pref-toggle${on ? " on" : ""}`}
        onClick={onToggle}
      >
        <span className="pref-toggle-knob" aria-hidden />
      </button>
    </li>
  );
}

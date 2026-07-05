import { useMutation, useQuery } from "@tanstack/react-query";
import { useId, useState } from "react";
import { api, setWorkspaceSlug, workspaceSlug } from "../api/client";
import {
  Badge,
  Button,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
import { AutonomyDot } from "../design-system/trust";
import { formatDate } from "../format/format";
import { useLocale, useT } from "../i18n";
import { problemMessage, QueryGate } from "./common";

// Settings governance surface (B-EP09.13b): renders FROM the live seams —
// /me (identity + effective roles), passport minting (agent ≤ human,
// token shown once), consent purposes (DOI flags), the privacy inbox
// (DSRs + statutory deadlines) — plus the locked autonomy-tier table.
// EP09 renders governance; it never authors policy. The attributable
// audit-log view waits on a read endpoint (filed as feedback/13); no fake
// data stands in for it.

export function SettingsScreen() {
  const t = useT();
  return (
    <div className="wrap narrow">
      <SectionHeader title={t("nav.settings")} />
      <IdentityCard />
      <WorkspaceCard />
      <PassportCard />
      <AutonomyCard />
      <ConsentPurposesCard />
      <PrivacyInboxCard />
    </div>
  );
}

function IdentityCard() {
  const t = useT();
  const query = useQuery({
    queryKey: ["me"],
    queryFn: async () => {
      const { data, error } = await api.GET("/me");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  return (
    <section className="card" style={{ marginBottom: 14 }}>
      <SectionHeader title={t("settings.identity")} />
      <QueryGate query={query}>
        {(me) => (
          <div
            style={{
              display: "flex",
              gap: 8,
              flexWrap: "wrap",
              alignItems: "center",
            }}
          >
            <span>{me.user.email}</span>
            {me.roles.map((role) => (
              <Badge key={role} tone="accent">
                {role}
              </Badge>
            ))}
          </div>
        )}
      </QueryGate>
    </section>
  );
}

function WorkspaceCard() {
  const t = useT();
  const inputId = useId();
  const [slug, setSlug] = useState(workspaceSlug() ?? "");
  const [saved, setSaved] = useState(false);
  return (
    <section className="card" style={{ marginBottom: 14 }}>
      <SectionHeader
        title={t("settings.workspace")}
        sub={t("settings.workspaceSub")}
      />
      <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
        <span className="t-label" id={inputId}>
          {t("settings.slug")}
        </span>
        <TextInput
          aria-labelledby={inputId}
          value={slug}
          onChange={(event) => {
            setSlug(event.target.value);
            setSaved(false);
          }}
        />
        <Button
          small
          variant="primary"
          onClick={() => {
            setWorkspaceSlug(slug.trim());
            setSaved(true);
          }}
        >
          {t("trust.save")}
        </Button>
        {saved && <span className="t-caption">{t("settings.saved")}</span>}
      </div>
    </section>
  );
}

const PASSPORT_SCOPES = ["read", "draft", "write", "send", "enrich"] as const;

function PassportCard() {
  const t = useT();
  const [label, setLabel] = useState("");
  const [scopes, setScopes] = useState<Set<string>>(new Set(["read", "draft"]));
  const labelId = useId();

  const mint = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/passports", {
        body: {
          label: label.trim() || null,
          scopes: [...scopes] as (
            | "read"
            | "draft"
            | "write"
            | "send"
            | "enrich"
          )[],
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  return (
    <section className="card" style={{ marginBottom: 14 }}>
      <SectionHeader
        title={t("settings.passports")}
        sub={t("settings.passportsSub")}
      />
      <div
        style={{
          display: "flex",
          gap: 8,
          flexWrap: "wrap",
          alignItems: "center",
        }}
      >
        <span className="t-label" id={labelId}>
          {t("settings.passportLabel")}
        </span>
        <TextInput
          aria-labelledby={labelId}
          value={label}
          onChange={(event) => setLabel(event.target.value)}
        />
        {PASSPORT_SCOPES.map((scope) => (
          <label
            key={scope}
            className="t-caption"
            style={{ display: "inline-flex", gap: 4 }}
          >
            <input
              type="checkbox"
              checked={scopes.has(scope)}
              onChange={(event) => {
                const next = new Set(scopes);
                if (event.target.checked) {
                  next.add(scope);
                } else {
                  next.delete(scope);
                }
                setScopes(next);
              }}
            />
            {scope}
          </label>
        ))}
        <Button
          small
          variant="primary"
          disabled={scopes.size === 0 || mint.isPending}
          onClick={() => mint.mutate()}
        >
          {t("settings.mint")}
        </Button>
      </div>
      {mint.isSuccess && (
        <div className="card card-inset" style={{ marginTop: 10 }}>
          <p className="t-label">{t("settings.tokenOnce")}</p>
          <p
            className="t-mono"
            style={{ wordBreak: "break-all", marginTop: 4 }}
          >
            {mint.data.token}
          </p>
        </div>
      )}
      {mint.isError && (
        <p
          className="t-caption"
          style={{ color: "var(--danger)", marginTop: 8 }}
        >
          {mint.error instanceof Error ? mint.error.message : null}
        </p>
      )}
    </section>
  );
}

// The two-tier table (03b): informational, and the advance-stage row is
// locked 🟡 — there is no toggle that could soften it (AC-settings).
function AutonomyCard() {
  const t = useT();
  return (
    <section className="card" style={{ marginBottom: 14 }}>
      <SectionHeader
        title={t("settings.autonomy")}
        sub={t("settings.autonomySub")}
      />
      <ul
        style={{
          listStyle: "none",
          display: "flex",
          flexDirection: "column",
          gap: 6,
        }}
      >
        <li>
          <AutonomyDot tier="auto" /> <strong>{t("settings.tierRead")}</strong>
        </li>
        <li>
          <AutonomyDot tier="confirm" />{" "}
          <strong>{t("settings.tierSend")}</strong>
        </li>
        <li>
          <AutonomyDot tier="confirm" />{" "}
          <strong>{t("settings.tierAdvance")}</strong>{" "}
          <Badge tone="warn">{t("settings.locked")}</Badge>
        </li>
      </ul>
    </section>
  );
}

function ConsentPurposesCard() {
  const t = useT();
  const query = useQuery({
    queryKey: ["consent-purposes"],
    queryFn: async () => {
      const { data, error } = await api.GET("/consent-purposes");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  return (
    <section className="card" style={{ marginBottom: 14 }}>
      <SectionHeader title={t("settings.purposes")} />
      <QueryGate query={query} empty={(page) => page.data.length === 0}>
        {(page) => (
          <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
            {page.data.map((purpose) => (
              <Badge
                key={purpose.id}
                tone={purpose.requires_double_opt_in ? "warn" : undefined}
              >
                {purpose.label}
                {purpose.requires_double_opt_in ? " · DOI" : ""}
              </Badge>
            ))}
          </div>
        )}
      </QueryGate>
    </section>
  );
}

function PrivacyInboxCard() {
  const t = useT();
  const { locale } = useLocale();
  const query = useQuery({
    queryKey: ["dsrs"],
    queryFn: async () => {
      const { data, error } = await api.GET("/data-subject-requests", {
        params: { query: { limit: 50 } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  return (
    <section className="card">
      <SectionHeader
        title={t("settings.privacy")}
        sub={t("settings.privacySub")}
      />
      <QueryGate query={query} empty={(page) => page.data.length === 0}>
        {(page) => (
          <ul
            style={{
              listStyle: "none",
              display: "flex",
              flexDirection: "column",
              gap: 6,
            }}
          >
            {page.data.map((dsr) => (
              <li
                key={dsr.id}
                style={{ display: "flex", gap: 8, alignItems: "center" }}
              >
                <Badge
                  tone={
                    dsr.kind === "erasure"
                      ? "danger"
                      : dsr.kind === "rectify"
                        ? "warn"
                        : undefined
                  }
                >
                  {dsr.kind}
                </Badge>
                <span className="t-mono">{dsr.subject_ref}</span>
                <Badge
                  tone={dsr.status === "fulfilled" ? "success" : undefined}
                >
                  {dsr.status}
                </Badge>
                <span className="t-small">
                  {t("settings.due", {
                    date: formatDate(dsr.due_at, locale, "Europe/Berlin"),
                  })}
                </span>
              </li>
            ))}
          </ul>
        )}
      </QueryGate>
    </section>
  );
}

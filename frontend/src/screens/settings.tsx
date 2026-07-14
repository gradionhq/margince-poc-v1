import { useInfiniteQuery, useMutation, useQuery } from "@tanstack/react-query";
import { type ReactNode, useId, useState } from "react";
import { api, setWorkspaceSlug, workspaceSlug } from "../api/client";
import {
  Badge,
  Button,
  EmptyState,
  SectionHeader,
  Skeleton,
  TextInput,
} from "../design-system/atoms";
import { FieldGuard, RoleBadge } from "../design-system/rbac";
import { AutonomyDot } from "../design-system/trust";
import { formatDate, formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import { problemMessage, QueryGate, useMe } from "./common";

// Settings governance surface (B-EP09.13b): renders FROM the live seams —
// /me (identity + effective roles), passports (mint + the metadata list,
// token shown once and never re-disclosed), consent purposes (DOI flags),
// the privacy inbox (DSRs + statutory deadlines), the attributable
// audit-log view with live filters — plus the locked autonomy-tier table
// and the door to the automations editor. EP09 renders governance; it
// never authors policy.

export function SettingsScreen() {
  const t = useT();
  return (
    <div className="wrap narrow">
      <SectionHeader title={t("nav.settings")} />
      <IdentityCard />
      <WorkspaceCard />
      <PassportCard />
      <AutonomyCard />
      <AutomationsLinkCard />
      <CustomFieldsLinkCard />
      <ConsentPurposesCard />
      <AuditLogCard />
      <PrivacyInboxCard />
    </div>
  );
}

function IdentityCard() {
  const t = useT();
  const query = useMe();
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
              <RoleBadge key={role} roleKey={role} />
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
  const { locale } = useLocale();
  const [label, setLabel] = useState("");
  const [scopes, setScopes] = useState<Set<string>>(new Set(["read", "draft"]));
  const labelId = useId();

  // Metadata only — the wire schema carries no token (PassportSummary),
  // so this list cannot re-disclose one.
  const list = useQuery({
    queryKey: ["passports"],
    queryFn: async () => {
      const { data, error } = await api.GET("/passports");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

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
    onSuccess: () => list.refetch(),
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
      <QueryGate query={list} empty={(page) => page.data.length === 0}>
        {(page) => (
          <ul
            style={{
              listStyle: "none",
              display: "flex",
              flexDirection: "column",
              gap: 6,
              marginTop: 12,
            }}
          >
            {page.data.map((passport) => {
              const revoked = passport.revoked_at != null;
              return (
                <li
                  key={passport.id}
                  data-passport={passport.id}
                  style={{
                    display: "flex",
                    gap: 8,
                    alignItems: "center",
                    flexWrap: "wrap",
                    // struck, not dimmed — dimming would drop the row
                    // under the AA contrast floor (B-EP09.21)
                    textDecoration: revoked ? "line-through" : undefined,
                  }}
                >
                  <strong>{passport.label}</strong>
                  {/* The credential exists but is withheld by design (shown
                      once at mint) — masked reads as "withheld", not absent. */}
                  <span className="t-label">{t("settings.token")}</span>
                  <FieldGuard mode="masked" />
                  {passport.scopes.map((scope) => (
                    <Badge key={scope}>{scope}</Badge>
                  ))}
                  <span className="t-small">
                    {t("settings.created", {
                      date: formatDate(
                        passport.created_at,
                        locale,
                        "Europe/Berlin",
                      ),
                    })}
                  </span>
                  {passport.expires_at && (
                    <span className="t-small">
                      {t("settings.expires", {
                        date: formatDate(
                          passport.expires_at,
                          locale,
                          "Europe/Berlin",
                        ),
                      })}
                    </span>
                  )}
                  {revoked && (
                    <Badge tone="danger">{t("settings.revoked")}</Badge>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </QueryGate>
    </section>
  );
}

// The door to the automations editor (B-EP09.15) — a settings entry, not a
// rail item: the 9-item rail is canonical (AC-shell-1).
function AutomationsLinkCard() {
  const t = useT();
  return (
    <section className="card" style={{ marginBottom: 14 }}>
      <SectionHeader
        title={t("settings.automations")}
        sub={t("settings.automationsSub")}
      />
      <a href="#/automations">{t("settings.openAutomations")}</a>
    </section>
  );
}

// The door to the custom-fields admin (CF-T06) — a settings entry, not a
// rail item: the 9-item rail is canonical (AC-shell-1).
function CustomFieldsLinkCard() {
  const t = useT();
  return (
    <section className="card" style={{ marginBottom: 14 }}>
      <SectionHeader
        title={t("settings.customFields")}
        sub={t("settings.customFieldsSub")}
      />
      <a href="#/custom-fields">{t("settings.openCustomFields")}</a>
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

// AC-settings-16: the attributable audit view — live filters over
// actor / entity_type / action, keyset "load more" via the page cursor.
// Filtering restarts the cursor chain (a filter change is a new question).
function AuditLogCard() {
  const t = useT();
  const { locale } = useLocale();
  const [actor, setActor] = useState("");
  const [entityType, setEntityType] = useState("");
  const [action, setAction] = useState("");
  const filterId = useId();

  const query = useInfiniteQuery({
    queryKey: ["audit-log", actor, entityType, action],
    initialPageParam: null as string | null,
    queryFn: async ({ pageParam }) => {
      const { data, error } = await api.GET("/audit-log", {
        params: {
          query: {
            limit: 20,
            ...(pageParam ? { cursor: pageParam } : {}),
            ...(actor.trim() ? { actor: actor.trim() } : {}),
            ...(entityType.trim() ? { entity_type: entityType.trim() } : {}),
            ...(action.trim() ? { action: action.trim() } : {}),
          },
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    getNextPageParam: (last) => last.page.next_cursor ?? null,
  });

  const entries = query.data?.pages.flatMap((page) => page.data) ?? [];

  // Honest state matrix (§3a): loading, error, empty, then the list — kept as
  // sequential branches rather than a nested ternary in the JSX below.
  let body: ReactNode;
  if (query.isPending) {
    body = (
      <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
        <Skeleton width="60%" />
        <Skeleton width="90%" />
      </div>
    );
  } else if (query.isError) {
    body = (
      <EmptyState>
        <p>{t("common.error")}</p>
        <p className="t-mono" style={{ marginTop: 6 }}>
          {query.error instanceof Error ? query.error.message : null}
        </p>
        <Button small onClick={() => query.refetch()} style={{ marginTop: 10 }}>
          {t("common.retry")}
        </Button>
      </EmptyState>
    );
  } else if (entries.length === 0) {
    body = <EmptyState>{t("common.empty")}</EmptyState>;
  } else {
    body = (
      <>
        <ul
          style={{
            listStyle: "none",
            display: "flex",
            flexDirection: "column",
            gap: 6,
          }}
        >
          {entries.map((entry) => (
            <li
              key={entry.id}
              style={{
                display: "flex",
                gap: 8,
                alignItems: "center",
                flexWrap: "wrap",
              }}
            >
              <span className="t-small">
                {formatDateTime(entry.occurred_at, locale, "Europe/Berlin")}
              </span>
              <span className="t-mono t-small">
                {entry.actor_type}:{entry.actor_id}
              </span>
              <Badge tone="accent">{entry.action}</Badge>
              <span className="t-mono t-small">
                {entry.entity_type}
                {entry.entity_id ? ` ${entry.entity_id}` : ""}
              </span>
            </li>
          ))}
        </ul>
        {query.hasNextPage && (
          <Button
            small
            disabled={query.isFetchingNextPage}
            onClick={() => query.fetchNextPage()}
            style={{ marginTop: 10 }}
          >
            {t("settings.loadMore")}
          </Button>
        )}
      </>
    );
  }

  return (
    <section className="card" style={{ marginBottom: 14 }}>
      <SectionHeader title={t("settings.audit")} sub={t("settings.auditSub")} />
      <div
        style={{
          display: "flex",
          gap: 8,
          flexWrap: "wrap",
          alignItems: "center",
          marginBottom: 10,
        }}
      >
        <span className="t-label" id={`${filterId}-actor`}>
          {t("settings.auditActor")}
        </span>
        <TextInput
          aria-labelledby={`${filterId}-actor`}
          value={actor}
          onChange={(event) => setActor(event.target.value)}
        />
        <span className="t-label" id={`${filterId}-entity`}>
          {t("settings.auditEntity")}
        </span>
        <TextInput
          aria-labelledby={`${filterId}-entity`}
          value={entityType}
          onChange={(event) => setEntityType(event.target.value)}
        />
        <span className="t-label" id={`${filterId}-action`}>
          {t("settings.auditAction")}
        </span>
        <TextInput
          aria-labelledby={`${filterId}-action`}
          value={action}
          onChange={(event) => setAction(event.target.value)}
        />
      </div>
      {body}
    </section>
  );
}

// Erasure reads danger, a rectification reads warn, other DSR kinds are neutral.
function dsrKindTone(kind: string): "danger" | "warn" | undefined {
  if (kind === "erasure") {
    return "danger";
  }
  if (kind === "rectify") {
    return "warn";
  }
  return undefined;
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
                <Badge tone={dsrKindTone(dsr.kind)}>{dsr.kind}</Badge>
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

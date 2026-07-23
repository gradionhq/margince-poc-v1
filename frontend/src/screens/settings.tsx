import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import {
  Building2,
  ChevronDown,
  Database,
  Factory,
  type LucideIcon,
  Mic,
  Package,
  ScrollText,
  ShieldCheck,
  Sparkles,
  UsersRound,
  Webhook,
} from "lucide-react";
import { type ReactNode, useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { dotTier } from "../app/autonomy";
import { ENTITY_KINDS, type EntityKind } from "../app/entity";
import {
  Badge,
  Button,
  EmptyState,
  SectionHeader,
  Skeleton,
  TextInput,
} from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import { FieldGuard, RoleBadge } from "../design-system/rbac";
import {
  AutonomyDot,
  EvidenceChip,
  FieldDiff,
  PassportChip,
  toEvidence,
} from "../design-system/trust";
import { formatDate, formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import { AiCallsCard } from "./aicalls";
import { AiUsageCard } from "./aiusage";
import { ActorTag } from "./audit";
import {
  canConfigureAutomations,
  LoadMoreButton,
  problemMessage,
  QueryGate,
  throwProblem,
  useLogout,
  useMe,
} from "./common";
import {
  CompanyContextCard,
  useCompanyContextCapabilities,
} from "./company-context";
import { CreateAction, type CreateField, CreateRecordModal } from "./create";
import { EditAction } from "./edit";
import { EmbedReindexCard } from "./embedreindex";
import { EntityRef } from "./entityref";
import { ConsentPurposesCard, PrivacyInboxCard } from "./privacy";
import { UsersAdminCard } from "./users-admin";
import { VoiceDnaCard } from "./voice-dna";
import { WebhooksCard } from "./webhooks";
import "./settings.css";

// Settings governance surface (B-EP09.13b): renders FROM the live seams —
// /me (identity + effective roles), passports (mint + the metadata list,
// token shown once and never re-disclosed), consent purposes (DOI flags),
// the privacy inbox (DSRs + statutory deadlines), the attributable
// audit-log view with live filters — plus the locked autonomy-tier table
// and the door to the automations editor. EP09 renders governance; it
// never authors policy.

// The tab register: one section nav entry per real settings surface. Only
// surfaces this app actually renders get a tab — the mockup's Members /
// Booking / Flow / Connected-surfaces tabs have no live seam here, so they are
// omitted rather than stubbed (STATE-5). The tab is selected by the route id
// (#/settings/<id>), so a tab is linkable and the palette can deep-link one.
// Two groups: "you" (per-user, every member) and "org" (organization config,
// admin/ops only). The nav renders the org group only for a role that could
// actually use it; the server stays the RBAC authority on every card within.
// `ai` stays in the personal group: it carries the caller's own agent passports
// (per-user), so hiding the whole tab from a rep would regress passport minting.
// The admin-only cards inside it (usage, call trace) are gated per-card already.
const SETTINGS_TABS = [
  { id: "account", icon: Building2, group: "you" },
  { id: "voice", icon: Mic, group: "you" },
  { id: "ai", icon: Sparkles, group: "you" },
  { id: "company", icon: Factory, group: "org" },
  { id: "users", icon: UsersRound, group: "org" },
  { id: "data", icon: Database, group: "org" },
  { id: "catalog", icon: Package, group: "org" },
  { id: "privacy", icon: ShieldCheck, group: "org" },
  { id: "audit", icon: ScrollText, group: "org" },
  { id: "integrations", icon: Webhook, group: "org" },
] as const satisfies readonly {
  id: string;
  icon: LucideIcon;
  group: "you" | "org";
}[];

type SettingsTabId = (typeof SETTINGS_TABS)[number]["id"];

function tabContent(id: SettingsTabId): ReactNode {
  switch (id) {
    case "account":
      return <IdentityCard />;
    case "voice":
      return <VoiceDnaCard />;
    case "company":
      return <CompanyContextCard />;
    case "users":
      return <UsersAdminCard />;
    case "ai":
      return <AiSettingsTab />;
    case "data":
      return (
        <>
          <CustomFieldsLinkCard />
          <EmbedReindexCard />
        </>
      );
    case "catalog":
      return (
        <>
          <ProductsLinkCard />
          <OfferTemplatesLinkCard />
          <PipelinesCard />
        </>
      );
    case "privacy":
      return (
        <>
          <ConsentPurposesCard />
          <PrivacyInboxCard />
        </>
      );
    case "audit":
      return <AuditLogCard />;
    case "integrations":
      return <WebhooksCard />;
  }
}

const SETTINGS_GROUPS = ["you", "org"] as const;

export function SettingsScreen({ tab }: Readonly<{ tab?: string }>) {
  const t = useT();
  const me = useMe();
  const capabilities = useCompanyContextCapabilities();
  // Org config is admin/ops-owned (same predicate the write affordances use);
  // a rep/manager never sees the Organization group. The server re-checks.
  const isOrgAdmin = canConfigureAutomations(me.data?.roles);
  const tabs = SETTINGS_TABS.filter((entry) => {
    // Integrations is read-capable by EVERY role (the seeded policy grants
    // webhook_subscription read to admin/ops/manager/rep/read_only; only
    // create/rotate/replay are admin/ops-only, and WebhooksCard gates those
    // per-card). So it is exempt from the org-admin nav filter — a read-only
    // role must still reach the subscription list + delivery-health views,
    // and its deep link must not fall back to Account.
    if (entry.id === "integrations") {
      return true;
    }
    if (entry.group === "org" && !isOrgAdmin) {
      return false;
    }
    if (entry.id === "company" && !capabilities.data?.read_enabled) {
      return false;
    }
    return true;
  });
  // Unknown / absent id (or one now hidden by role) falls back to the first
  // visible tab — a stale deep-link lands on Account, never a blank screen.
  const active = tabs.find((entry) => entry.id === tab) ?? tabs[0];
  return (
    <div className="wrap">
      <SectionHeader title={t("nav.settings")} />
      <div className="set-grid">
        <nav className="set-nav" aria-label={t("settings.navAria")}>
          {SETTINGS_GROUPS.map((group) => {
            const groupTabs = tabs.filter((entry) => entry.group === group);
            if (groupTabs.length === 0) {
              return null;
            }
            return (
              <div key={group} className="set-nav-group">
                <div className="set-nav-grouplabel">
                  {t(`settings.group.${group}`)}
                </div>
                {groupTabs.map(({ id, icon: Icon }) => {
                  const isActive = id === active.id;
                  return (
                    <a
                      key={id}
                      href={`#/settings/${id}`}
                      className={isActive ? "active" : undefined}
                      aria-current={isActive ? "page" : undefined}
                    >
                      <Icon aria-hidden />
                      {t(`settings.tab.${id}`)}
                    </a>
                  );
                })}
              </div>
            );
          })}
        </nav>
        <div className="set-content">{tabContent(active.id)}</div>
      </div>
    </div>
  );
}

// The AI & autonomy tab. AiUsageCard (GET /ai/usage) and AiCallsCard
// (GET /ai/calls) require the automation Update grant server-side, so they
// are rendered only for admin/ops — a rep/manager would otherwise hit a
// 403 error box on a tab they can otherwise use. This mirrors the
// EconomyBanner's canConfigureAutomations guard on the same /ai/usage seam;
// the server stays the RBAC authority regardless.
function AiSettingsTab() {
  const me = useMe();
  const canSeeRuntime = canConfigureAutomations(me.data?.roles);
  return (
    <>
      {canSeeRuntime && <AiUsageCard />}
      {canSeeRuntime && <AiCallsCard />}
      <AutonomyCard />
      <PassportCard />
      <AgentToolsCard />
      <AutomationsLinkCard />
    </>
  );
}

function IdentityCard() {
  const t = useT();
  const query = useMe();
  const logout = useLogout();
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
      <Button
        small
        disabled={logout.isPending}
        onClick={() => logout.mutate()}
        style={{ marginTop: 10 }}
      >
        {t("auth.signOut")}
      </Button>
    </section>
  );
}

const PASSPORT_SCOPES = ["read", "draft", "write", "send", "enrich"] as const;

function PassportCard() {
  const t = useT();
  const { locale } = useLocale();
  const [label, setLabel] = useState("");
  const [scopes, setScopes] = useState<Set<string>>(new Set(["read", "draft"]));
  const [confirmId, setConfirmId] = useState<string | null>(null);
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

  // AS-2 kill-switch: revoke is a hard DELETE, never a soft toggle in this
  // client — ConfirmModal guards it so a stray click can't kill a live
  // agent's credential.
  const revoke = useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.DELETE("/passports/{id}", {
        params: { path: { id } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
    },
    onSuccess: () => {
      setConfirmId(null);
      list.refetch();
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
                  {!revoked && (
                    <Button
                      small
                      variant="danger"
                      onClick={() => setConfirmId(passport.id)}
                    >
                      {t("settings.revoke")}
                    </Button>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </QueryGate>
      <ConfirmModal
        open={confirmId != null}
        onClose={() => {
          setConfirmId(null);
          revoke.reset();
        }}
        title={t("settings.revoke")}
        confirmLabel={t("settings.revoke")}
        onConfirm={() => confirmId && revoke.mutate(confirmId)}
        pending={revoke.isPending}
        error={revoke.error instanceof Error ? revoke.error.message : null}
      >
        <p>{t("settings.revokeConfirm")}</p>
      </ConfirmModal>
    </section>
  );
}

// The read-only tool console (IT-1): the same governed surface an MCP client
// sees — GET /agent-tools, with an optional passport selector that dims any
// row the selected passport's granted scopes don't cover. No passport picked
// means every row reads as reachable (the unfiltered inventory).
function AgentToolsCard() {
  const t = useT();
  const [passportId, setPassportId] = useState<string>("");
  const tools = useQuery({
    queryKey: ["agent-tools"],
    queryFn: async () => {
      const { data, error } = await api.GET("/agent-tools");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  const passports = useQuery({
    queryKey: ["passports"],
    queryFn: async () => {
      const { data, error } = await api.GET("/passports");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  const selected = passports.data?.data.find((p) => p.id === passportId);
  const grantedScopes = new Set(selected?.scopes ?? []);

  return (
    <section className="card" style={{ marginBottom: 14 }}>
      <SectionHeader title={t("tools.title")} sub={t("tools.sub")} />
      {passports.data && passports.data.data.length > 0 && (
        <select
          className="input"
          aria-label={t("tools.scopeAll")}
          value={passportId}
          onChange={(event) => setPassportId(event.target.value)}
          style={{ marginBottom: 10 }}
        >
          <option value="">{t("tools.scopeAll")}</option>
          {passports.data.data
            .filter((p) => p.revoked_at == null)
            .map((p) => (
              <option key={p.id} value={p.id}>
                {t("tools.scopedTo", { label: p.label })}
              </option>
            ))}
        </select>
      )}
      <QueryGate query={tools} empty={(data) => data.data.length === 0}>
        {(data) => (
          <ul
            className="tool-rows"
            style={{
              listStyle: "none",
              display: "flex",
              flexDirection: "column",
              gap: 6,
            }}
          >
            {data.data.map((tool) => {
              const reachable =
                !passportId ||
                tool.required_scope == null ||
                grantedScopes.has(tool.required_scope);
              return (
                <li
                  key={tool.name}
                  data-tool={tool.name}
                  className="tool-row"
                  style={{
                    display: "flex",
                    alignItems: "center",
                    gap: 10,
                    opacity: reachable ? 1 : 0.4,
                  }}
                >
                  <AutonomyDot tier={dotTier(tool.tier)} />
                  <span className="t-mono" style={{ color: "var(--accent)" }}>
                    {tool.name}
                  </span>
                  {tool.required_scope && <Badge>{tool.required_scope}</Badge>}
                  {tool.egress && (
                    <Badge tone="warn">{t("tools.egress")}</Badge>
                  )}
                  {!reachable && (
                    <span className="t-caption">{t("tools.unreachable")}</span>
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

type Pipeline = components["schemas"]["Pipeline"];
type Stage = components["schemas"]["Stage"];

// The 3 shared scalar fields between create and edit pipeline forms.
function pipelineFields(t: ReturnType<typeof useT>): CreateField[] {
  return [
    { key: "name", label: "pipeline.name", required: true },
    {
      key: "is_default",
      label: "pipeline.default",
      type: "select",
      required: true,
      options: [
        { value: "false", label: t("pipeline.notDefault") },
        { value: "true", label: t("pipeline.default") },
      ],
    },
    { key: "position", label: "pipeline.position", type: "number" },
  ];
}

// Coerces a form value (CreateAction's values are strings; EditAction's
// update callback widens to Record<string, unknown> so a screen COULD prefill
// non-string values) down to the trimmed string this form always produces —
// mirrors deals.tsx's mapDealUpdate `str` helper, keeping both create's and
// edit's transports on the one map function without an `as` cast.
function str(v: unknown): string {
  return typeof v === "string" ? v.trim() : "";
}

function mapPipelineBody(v: Record<string, unknown>) {
  return {
    name: str(v.name),
    is_default: v.is_default === "true",
    position: v.position ? Number(str(v.position)) : 0,
  };
}

// Narrows the form's free-text semantic value into the Stage enum WITHOUT a
// cast (mirrors deals.tsx's forecastCategory) — an unrecognized value falls
// back to "open" rather than shipping a bad literal to the wire.
function stageSemantic(v: unknown): Stage["semantic"] {
  switch (v) {
    case "won":
      return "won";
    case "lost":
      return "lost";
    default:
      return "open";
  }
}

// UpdateStageRequest carries no pipeline_id (a stage never moves pipelines
// via this form) while CreateStageRequest requires one — so this returns
// only the fields the two requests share, and the create transport adds
// pipeline_id on top.
function mapStageBody(v: Record<string, unknown>) {
  return {
    name: str(v.name),
    position: v.position ? Number(str(v.position)) : 0,
    semantic: stageSemantic(v.semantic),
    win_probability: v.win_probability ? Number(str(v.win_probability)) : 0,
  };
}

function stageFields(t: ReturnType<typeof useT>): CreateField[] {
  return [
    { key: "name", label: "stage.name", required: true },
    { key: "position", label: "pipeline.position", type: "number" },
    {
      key: "semantic",
      label: "stage.semantic",
      type: "select",
      required: true,
      options: [
        { value: "open", label: t("stage.semOpen") },
        { value: "won", label: t("stage.semWon") },
        { value: "lost", label: t("stage.semLost") },
      ],
    },
    { key: "win_probability", label: "stage.winProb", type: "number" },
  ];
}

// Localized badge for a stage's semantic — open/won/lost each render as a
// short label rather than the raw enum value.
function stageSemanticLabel(
  semantic: Stage["semantic"],
  t: ReturnType<typeof useT>,
): string {
  if (semantic === "won") {
    return t("stage.semWon");
  }
  if (semantic === "lost") {
    return t("stage.semLost");
  }
  return t("stage.semOpen");
}

// Tone-less Badge shares the card-inset background it sits on (both resolve
// to var(--bgCard)) — the semantic pill needs an explicit tone to be visible.
function stageSemanticTone(
  semantic: Stage["semantic"],
): "success" | "danger" | "accent" {
  switch (semantic) {
    case "won":
      return "success";
    case "lost":
      return "danger";
    default:
      return "accent"; // open
  }
}

// The bespoke per-pipeline "new stage" trigger: CreateAction's testid
// (`new-record`) can't disambiguate multiple pipelines on one screen, so
// this composes the same Button + CreateRecordModal pieces directly rather
// than adding new form infra.
function StageCreate({ pipelineId }: Readonly<{ pipelineId: string }>) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const queryClient = useQueryClient();
  const mutation = useMutation({
    mutationFn: async (values: Record<string, string>) => {
      const { data, error } = await api.POST("/stages", {
        body: { ...mapStageBody(values), pipeline_id: pipelineId },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: () => {
      setOpen(false);
      queryClient.invalidateQueries({ queryKey: ["pipelines"] });
    },
  });
  return (
    <>
      <Button
        small
        data-testid={`new-stage-${pipelineId}`}
        onClick={() => setOpen(true)}
      >
        {t("stage.new")}
      </Button>
      <CreateRecordModal
        open={open}
        onClose={() => setOpen(false)}
        title={t("stage.new")}
        fields={stageFields(t)}
        pending={mutation.isPending}
        error={mutation.isError ? mutation.error.message : null}
        onSubmit={(values) => mutation.mutate(values)}
      />
    </>
  );
}

function StageRow({
  stage,
  canConfig,
  t,
}: Readonly<{
  stage: Stage;
  canConfig: boolean;
  t: ReturnType<typeof useT>;
}>) {
  return (
    <li
      style={{
        display: "grid",
        gridTemplateColumns: "minmax(0, 1fr) 88px 56px auto",
        gap: 8,
        alignItems: "center",
      }}
    >
      <span>{stage.name}</span>
      <Badge tone={stageSemanticTone(stage.semantic)}>
        {stageSemanticLabel(stage.semantic, t)}
      </Badge>
      <span className="t-mono t-small">{stage.win_probability}%</span>
      {canConfig && (
        <EditAction
          label={t("stage.edit")}
          invalidate="pipelines"
          recordKey="stage"
          record={{
            id: stage.id,
            name: stage.name,
            position: String(stage.position),
            semantic: stage.semantic,
            win_probability: String(stage.win_probability),
          }}
          fields={stageFields(t)}
          update={async (values) => {
            const { data, error } = await api.PATCH("/stages/{id}", {
              params: { path: { id: stage.id } },
              body: mapStageBody(values),
            });
            if (error) {
              throwProblem(error);
            }
            return data;
          }}
        />
      )}
    </li>
  );
}

function PipelineRow({
  pipeline,
  canConfig,
  t,
}: Readonly<{
  pipeline: Pipeline;
  canConfig: boolean;
  t: ReturnType<typeof useT>;
}>) {
  const stages = [...(pipeline.stages ?? [])].sort(
    (a, b) => a.position - b.position,
  );
  return (
    <div className="card card-inset" style={{ marginBottom: 10 }}>
      <div
        style={{
          display: "flex",
          gap: 8,
          alignItems: "center",
          flexWrap: "wrap",
        }}
      >
        <span className="t-h2">{pipeline.name}</span>
        <Badge tone={pipeline.is_default ? "success" : undefined}>
          {pipeline.is_default
            ? t("pipeline.default")
            : t("pipeline.notDefault")}
        </Badge>
        {canConfig && (
          <>
            <EditAction
              label={t("pipeline.edit")}
              invalidate="pipelines"
              recordKey="pipeline"
              record={{
                id: pipeline.id,
                name: pipeline.name,
                is_default: String(pipeline.is_default),
                position: String(pipeline.position),
              }}
              fields={pipelineFields(t)}
              update={async (values) => {
                const { data, error } = await api.PATCH("/pipelines/{id}", {
                  params: { path: { id: pipeline.id } },
                  body: mapPipelineBody(values),
                });
                if (error) {
                  throwProblem(error);
                }
                return data;
              }}
            />
            <StageCreate pipelineId={pipeline.id} />
          </>
        )}
      </div>
      <ul
        style={{
          listStyle: "none",
          display: "flex",
          flexDirection: "column",
          gap: 6,
          marginTop: 8,
        }}
      >
        {stages.map((stage) => (
          <StageRow key={stage.id} stage={stage} canConfig={canConfig} t={t} />
        ))}
      </ul>
    </div>
  );
}

// D-8: Settings → Pipelines config. Reads via the SAME ["pipelines","all"]
// key the deals screen's plural selector uses (an array shape, distinct
// from DealScreen's single-pipeline ["pipelines"] cache entry) — any
// mutation here invalidates the ["pipelines"] prefix, so both shapes stay
// fresh. Write affordances are gated on canConfigureAutomations; the list
// itself is read-only for everyone (the server stays the RBAC authority).
export function PipelinesCard() {
  const t = useT();
  const me = useMe();
  const canConfig = canConfigureAutomations(me.data?.roles);
  const query = useQuery({
    queryKey: ["pipelines", "all"],
    queryFn: async () => {
      const { data, error } = await api.GET("/pipelines", {
        params: { query: {} },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data.data;
    },
  });
  return (
    <section className="card" style={{ marginBottom: 14 }}>
      <SectionHeader
        title={t("settings.pipelines")}
        sub={t("settings.pipelinesSub")}
      />
      {canConfig && (
        <div style={{ marginBottom: 10 }}>
          <CreateAction
            label={t("pipeline.new")}
            invalidate="pipelines"
            screen="settings"
            create={async (values) => {
              const { data, error } = await api.POST("/pipelines", {
                body: { ...mapPipelineBody(values), stages: [] },
              });
              if (error) {
                throwProblem(error);
              }
              return data;
            }}
            fields={pipelineFields(t)}
          />
        </div>
      )}
      <QueryGate query={query} empty={(pipelines) => pipelines.length === 0}>
        {(pipelines) => (
          <>
            {pipelines.map((pipeline) => (
              <PipelineRow
                key={pipeline.id}
                pipeline={pipeline}
                canConfig={canConfig}
                t={t}
              />
            ))}
          </>
        )}
      </QueryGate>
    </section>
  );
}

function ProductsLinkCard() {
  const t = useT();
  return (
    <section className="card" style={{ marginBottom: 14 }}>
      <SectionHeader
        title={t("product.title")}
        sub={t("product.settingsSub")}
      />
      <a href="#/products">{t("product.open")}</a>
    </section>
  );
}

function OfferTemplatesLinkCard() {
  const t = useT();
  return (
    <section className="card" style={{ marginBottom: 14 }}>
      <SectionHeader
        title={t("template.title")}
        sub={t("template.settingsSub")}
      />
      <a href="#/offer-templates">{t("template.open")}</a>
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

type AuditLogEntry = components["schemas"]["AuditLogEntry"];

function isEntityKind(kind: string): kind is EntityKind {
  return (ENTITY_KINDS as readonly string[]).includes(kind);
}

// The union of before/after keys for one row's diff — a key present on
// neither side is never shown, so the panel only ever displays fields the
// mutation actually touched.
function diffKeys(
  before: AuditLogEntry["before"],
  after: AuditLogEntry["after"],
): string[] {
  const keys = new Set<string>();
  for (const key of Object.keys(before ?? {})) {
    keys.add(key);
  }
  for (const key of Object.keys(after ?? {})) {
    keys.add(key);
  }
  return [...keys].sort();
}

// A key absent from an object (withheld/never set) reads the same as an
// explicit null through FieldDiff's honest empty marker (created/cleared) —
// this never fabricates a value for a key the side genuinely lacks.
function diffValue(
  rec: AuditLogEntry["before"] | AuditLogEntry["after"],
  key: string,
): string | null {
  const value = rec?.[key];
  if (value === null || value === undefined) {
    return null;
  }
  // Object/array field values (custom-field JSON, links, ...) need JSON
  // rendering — the bare String() coercion collapses them to "[object
  // Object]", which is neither readable nor honest about what changed.
  return typeof value === "object" ? JSON.stringify(value) : String(value);
}

// yyyy-mm-dd from a date input, read as a UTC instant: start-of-day for
// `from`, end-of-day for `to`, so the range is inclusive of the whole `to`
// day rather than silently truncating it at midnight.
function fromDateParam(date: string): string {
  return new Date(`${date}T00:00:00.000Z`).toISOString();
}
function toDateParam(date: string): string {
  return new Date(`${date}T23:59:59.999Z`).toISOString();
}

type AuditLogFilters = Readonly<{
  actor: string;
  entityType: string;
  entityId: string;
  action: string;
  from: string;
  to: string;
}>;

// Every filter is optional-if-blank, so this stays a flat spread rather than
// a chain of conditionals in the queryFn itself (kept the query builder under
// the cognitive-complexity gate).
function auditLogQueryParams(
  filters: AuditLogFilters,
  pageParam: string | null,
) {
  const { actor, entityType, entityId, action, from, to } = filters;
  return {
    limit: 20,
    ...(pageParam ? { cursor: pageParam } : {}),
    ...(actor.trim() ? { actor: actor.trim() } : {}),
    ...(entityType.trim() ? { entity_type: entityType.trim() } : {}),
    ...(entityId.trim() ? { entity_id: entityId.trim() } : {}),
    ...(action.trim() ? { action: action.trim() } : {}),
    ...(from ? { from: fromDateParam(from) } : {}),
    ...(to ? { to: toDateParam(to) } : {}),
  };
}

function AuditLogRow({
  entry,
  meUserId,
}: Readonly<{ entry: AuditLogEntry; meUserId?: string }>) {
  const t = useT();
  const { locale } = useLocale();
  const [expanded, setExpanded] = useState(false);
  const keys = diffKeys(entry.before, entry.after);
  const evidence = toEvidence(entry.evidence);

  return (
    <li style={{ display: "flex", flexDirection: "column", gap: 6 }}>
      <div
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
        <ActorTag entry={entry} meUserId={meUserId} />
        <Badge tone="accent">{entry.action}</Badge>
        {entry.entity_id && isEntityKind(entry.entity_type) ? (
          <EntityRef kind={entry.entity_type} id={entry.entity_id} />
        ) : (
          <span className="t-mono t-small">
            {entry.entity_type}
            {entry.entity_id ? ` ${entry.entity_id}` : ""}
          </span>
        )}
        <Button
          small
          aria-expanded={expanded}
          aria-label={t("settings.auditExpand")}
          onClick={() => setExpanded((value) => !value)}
        >
          <ChevronDown
            aria-hidden
            size={14}
            style={{ transform: expanded ? "rotate(180deg)" : undefined }}
          />
        </Button>
      </div>
      {expanded && (
        <div
          style={{
            display: "flex",
            flexDirection: "column",
            gap: 6,
            paddingLeft: 12,
            borderLeft: "2px solid var(--borderSubtle)",
          }}
        >
          {keys.map((key) => (
            <div
              key={key}
              style={{ display: "flex", gap: 8, alignItems: "center" }}
            >
              <span className="t-label">{key}</span>
              <FieldDiff
                oldValue={diffValue(entry.before, key)}
                newValue={diffValue(entry.after, key)}
              />
            </div>
          ))}
          {entry.passport_id && <PassportChip id={entry.passport_id} />}
          {entry.on_behalf_of && (
            <span className="t-small">
              {t("settings.auditOnBehalf")}{" "}
              <span className="t-mono">{entry.on_behalf_of}</span>
            </span>
          )}
          {entry.authorization_rule && (
            <span className="t-small">
              {t("settings.auditRule")}: {entry.authorization_rule}
            </span>
          )}
          {evidence && <EvidenceChip evidence={evidence} />}
        </div>
      )}
    </li>
  );
}

// AC-settings-16: the attributable audit view — live filters over actor /
// entity_type / entity_id / action / from / to, keyset "load more" via the
// page cursor. Filtering restarts the cursor chain (a filter change is a new
// question). Each row expands into the before/after diff plus the agent
// attribution trail (passport, on-behalf-of human, authorization rule,
// grounding evidence) — collapsed by default so the flat scan stays fast.
export function AuditLogCard() {
  const t = useT();
  // The current user's id resolves audit "You" vs "A teammate" in ActorTag.
  const meUserId = useMe().data?.user?.id;
  const [actor, setActor] = useState("");
  const [entityType, setEntityType] = useState("");
  const [entityId, setEntityId] = useState("");
  const [action, setAction] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const filterId = useId();

  const query = useInfiniteQuery({
    queryKey: ["audit-log", actor, entityType, entityId, action, from, to],
    initialPageParam: null as string | null,
    queryFn: async ({ pageParam }) => {
      const { data, error } = await api.GET("/audit-log", {
        params: {
          query: auditLogQueryParams(
            { actor, entityType, entityId, action, from, to },
            pageParam,
          ),
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
            gap: 10,
          }}
        >
          {entries.map((entry) => (
            <AuditLogRow key={entry.id} entry={entry} meUserId={meUserId} />
          ))}
        </ul>
        <LoadMoreButton query={query} />
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
        <span className="t-label" id={`${filterId}-entity-id`}>
          {t("settings.auditEntityId")}
        </span>
        <TextInput
          aria-labelledby={`${filterId}-entity-id`}
          value={entityId}
          onChange={(event) => setEntityId(event.target.value)}
        />
        <span className="t-label" id={`${filterId}-action`}>
          {t("settings.auditAction")}
        </span>
        <TextInput
          aria-labelledby={`${filterId}-action`}
          value={action}
          onChange={(event) => setAction(event.target.value)}
        />
        <span className="t-label" id={`${filterId}-from`}>
          {t("settings.auditFrom")}
        </span>
        <TextInput
          type="date"
          aria-labelledby={`${filterId}-from`}
          value={from}
          onChange={(event) => setFrom(event.target.value)}
        />
        <span className="t-label" id={`${filterId}-to`}>
          {t("settings.auditTo")}
        </span>
        <TextInput
          type="date"
          aria-labelledby={`${filterId}-to`}
          value={to}
          onChange={(event) => setTo(event.target.value)}
        />
      </div>
      {body}
    </section>
  );
}

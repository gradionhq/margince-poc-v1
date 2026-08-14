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
  KeyRound,
  type LucideIcon,
  Mail,
  Mic,
  Plug,
  ShieldCheck,
  Sparkles,
  UserRound,
  UsersRound,
  Webhook,
  Wrench,
} from "lucide-react";
import { type ReactNode, useId, useRef, useState } from "react";
import { api } from "../api/client";
import type { components, operations } from "../api/schema";
import { dotTier } from "../app/autonomy";
import { useCan, useCanWrite, useHoldsAdminRole } from "../app/capability";
import { ENTITY_KINDS, type EntityKind } from "../app/entity";
import type { NavLevelEntry, NavLevelGroup, NavSection } from "../app/nav";
import { ResumeConnectBanner } from "../app/resumeconnectbanner";
import { setTheme, THEMES, useTheme } from "../app/theme";
import {
  Badge,
  Button,
  Card,
  Checkbox,
  EmptyState,
  Field,
  SegmentedControl,
  Skeleton,
  Textarea,
  TextInput,
} from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import { PassportSelect, ScopeChips } from "../design-system/passportselect";
import { FieldGuard, RoleBadge } from "../design-system/rbac";
import { Select } from "../design-system/select";
import {
  AutonomyDot,
  EvidenceChip,
  FieldDiff,
  PassportChip,
  toEvidence,
} from "../design-system/trust";
import { formatDate, formatDateTime } from "../format/format";
import { LOCALES, localeNameKey, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { AiCallsCard } from "./aicalls";
import { AiUsageCard } from "./aiusage";
import { ActorTag } from "./audit";
import { AutomationsAdmin } from "./automations";
import { CaptureSettingsCard } from "./capture-settings";
import {
  LoadMoreButton,
  problemMessageOf,
  QueryGate,
  throwProblem,
  useLogout,
  useMe,
} from "./common";
import {
  CompanyContextCard,
  useCompanyContextCapabilities,
} from "./company-context";
import { ConnectedAgentsCard } from "./connected-agents";
import { ConnectorsCard } from "./connectors";
import { ConsumerMailDomainsCard } from "./consumer-mail-domains";
import { CreateAction, type CreateField, CreateRecordModal } from "./create";
import { CustomFieldsAdmin } from "./customfields";
import { EditAction } from "./edit";
import { EmbedReindexCard } from "./embedreindex";
import { EntityRef } from "./entityref";
import { ExtensionAccessCard } from "./extension-access";
import { ImportCard } from "./import";
import { InstallationSettingsCard } from "./installation-settings";
import { ProviderCard } from "./integrations-provider";
import { JobHealthCard } from "./jobhealth";
import { LinkedInImportCard } from "./linkedin-import";
import { LinkedInReachCard } from "./linkedin-reach";
import { OfferTemplatesAdmin } from "./offertemplates";
import { OverlayCard } from "./overlay";
import { MirrorUserMapCard } from "./overlay-usermap";
import { OwnDomainsCard } from "./own-domains";
import { ConsentPurposesCard, PrivacyInboxCard } from "./privacy";
import { ProductsAdmin } from "./products";
import { FxRatesCard, ModelCostsCard } from "./rates";
import { RetentionCard } from "./retention";
import { UsersAdminCard } from "./users-admin";
import { VoiceDnaCard } from "./voice-dna";
import { WebhooksCard } from "./webhooks";
import "./settings.css";

// Settings governance surface (B-EP09.13b): renders FROM the live seams —
// /me (identity + effective roles), passports (mint + the metadata list,
// token shown once and never re-disclosed), consent purposes (DOI flags),
// the privacy inbox (DSRs + statutory deadlines), the attributable
// audit-log view with live filters — plus the locked autonomy-tier table
// and the automations the installation runs unattended. EP09 renders
// governance; it never authors policy.

// The entry register: one section nav entry per settings SUBJECT. Only surfaces
// this app actually renders get one — the mockup's Booking / Flow /
// Connected-surfaces tabs have no live seam here, so they are omitted rather
// than stubbed (STATE-5). The entry is selected by the route id
// (#/settings/<id>), so it is linkable and the palette can deep-link one.
//
// Twelve, and it used to be fifteen tabs plus nine routes outside them. What
// collapsed and why: two surfaces both called "Capture" became one; the
// installation and the company profile were always the same organization;
// currency rates joined the base currency they convert to while model prices
// joined the AI runtime they price; user administration and extension
// permissions are one question about authority; the field editor, pipeline
// designer, product list and offer templates all define the shape a record
// takes; and the operational verbs that were hiding beside the field editor — a
// reindex, job health, the danger zone — became a place of their own.
//
// One of those merges was later UNDONE, which is why the count is twelve rather
// than eleven: connectors and the overlay both answer "what are we connected to"
// and were merged on that reading, but the question has two different owners —
// see the split below.
//
// Two groups: "you" (per-user, every member) and "org" (organization config).
// Every org entry carries its OWN predicate — the grant the cards on it actually
// ask for — and the group heading renders when at least one member survives.
// One predicate for the whole group could only ever be a guess about a
// heterogeneous set: it spans surfaces with clean object grants (data model,
// organization) and surfaces with no RBAC object at all (people, privacy),
// which the server gates on the role itself. The server stays the RBAC
// authority on every card within.
//
// The personal group is where a credential or a connection the PERSON holds
// lives: `agents` carries the caller's own passports, so gating it would regress
// passport minting for every seat that is not an admin, and `connections` carries
// their own mailbox and their own LinkedIn network.
//
// `connections` and `integrations` were ONE row, and that row was the reason the
// list had an entry with no predicate at all: it held a rep's own mailbox and the
// installation's webhooks together, so any honest gate on it took a personal task
// away from whoever it hid it from. The seam was never a missing group — it was
// one entry belonging to both. Split by WHOSE thing each surface is, they both get
// an honest predicate, and the ungated special case is gone rather than moved.
const SETTINGS_TABS = [
  { id: "account", icon: UserRound, group: "you" },
  { id: "voice", icon: Mic, group: "you" },
  { id: "agents", icon: KeyRound, group: "you" },
  { id: "connections", icon: Plug, group: "you" },
  { id: "general", icon: Building2, group: "org" },
  { id: "people", icon: UsersRound, group: "org" },
  { id: "integrations", icon: Webhook, group: "org" },
  { id: "capture", icon: Mail, group: "org" },
  { id: "data-model", icon: Database, group: "org" },
  { id: "ai", icon: Sparkles, group: "org" },
  { id: "privacy", icon: ShieldCheck, group: "org" },
  { id: "maintenance", icon: Wrench, group: "org" },
] as const satisfies readonly {
  id: string;
  icon: LucideIcon;
  group: "you" | "org";
}[];

type SettingsTabId = (typeof SETTINGS_TABS)[number]["id"];

function tabContent(id: SettingsTabId): ReactNode {
  switch (id) {
    case "account":
      return (
        <>
          <IdentityCard />
          <EmailSignatureCard />
          <PreferencesCard />
        </>
      );
    case "voice":
      return <VoiceDnaCard />;
    case "agents":
      return <AgentsTab />;
    case "general":
      return <GeneralTab />;
    case "people":
      return (
        <>
          <UsersAdminCard />
          {/* Beside the member list because it answers the same question one
              level up: that card says which role a person holds, this one says
              what a role may reach. */}
          <ExtensionAccessCard />
        </>
      );
    case "connections":
      return <ConnectionsTab />;
    case "integrations":
      return <IntegrationsTab />;
    case "capture":
      return (
        <>
          {/* Which domains are OURS, then what to do with mail from the rest,
              then which of the rest are consumer mailboxes — the posture, then
              the two judgements that read it. Before this they sat on two
              different tabs that shared the word "Capture" and neither said
              which one the other meant. */}
          <OwnDomainsCard />
          <CaptureSettingsCard />
          <ConsumerMailDomainsCard />
        </>
      );
    case "data-model":
      return <DataModelTab />;
    case "ai":
      return <AiSettingsTab />;
    case "privacy":
      return (
        <>
          <ConsentPurposesCard />
          {/* The retention ladder sits under the purpose catalogue and above
              the DSR inbox: what the installation keeps by default, before the
              requests that override it case by case. Admin/ops in substance, and
              it says so without the grant rather than vanishing — every card on
              this page now behaves the same way, which is the point: three
              different answers to one denial on one page is what made it
              unreadable. */}
          <RetentionCard />
          <PrivacyInboxCard />
          {/* Last, and on the same page: the trail is what proves the three
              surfaces above it were honoured. It gates itself on the admin
              role, which the purpose registry above does not. */}
          <AuditLogCard />
        </>
      );
    case "maintenance":
      return (
        <>
          {/* Three operational verbs, in ascending order of consequence: a
              reindex that costs tokens, a read of what the background system is
              holding, and a reset that empties the installation. They hid beside
              the custom-field editor before, which put "define a field" and
              "delete everything" on one page. Job health had no surface at all —
              an operator watching a stalled queue had nothing to look at. */}
          <ImportCard />
          <EmbedReindexCard />
          <JobHealthCard />
          <ResetDataCard />
        </>
      );
  }
}

// What YOU are connected to. Every surface here reads a per-user seam — the
// connector list is scoped to the calling human server-side (capture is per-user,
// RC-8), and both LinkedIn surfaces read `/me`. So this belongs to the personal
// group, and needs no grant: a mailbox nobody else can see is not organization
// configuration, and the entry that used to hold both kinds could not say so.
function ConnectionsTab() {
  return (
    <>
      <ConnectorsCard />
      <LinkedInImportCard />
      {/* No review queue here: a match a human must judge is a proposal, and
          proposals live in the approvals inbox. This shows what the import
          bought — which accounts the network reaches. */}
      <LinkedInReachCard />
    </>
  );
}

// What the INSTALLATION is wired to: one shared contact-data credential, the
// outbound subscriptions, the incumbent CRM it mirrors, and who each of its users
// is over there. All four are workspace-wide — a key everybody spends from, a webhook everybody's writes
// fire, a system-of-record flip that re-points every read — which is why they
// sit under the organization heading and the personal connections do not.
function IntegrationsTab() {
  return (
    <>
      <ProviderCard />
      <WebhooksCard />
      {/* Everything overlay — connect, live sync/budget health (OverlayCard
          renders OverlayLiveSection itself once a connection is active or in
          error, so it is not rendered a second time here), and the user
          mapping. Deliberately NOT gated on useSorMode() === "overlay": a
          workspace is native until an overlay is connected, so mode-gating
          would hide the only surface that can connect one. In native mode
          OverlayCard renders its connect form and the rest stays quiet. */}
      <OverlayCard />
      <MirrorUserMapCard />
    </>
  );
}

// The organization, once: the installation's own settings, the currency table,
// and the company profile the AI reads.
//
// The currency pair is ADJACENT, which is the whole reason these were merged: the
// base currency is declared in the second card of InstallationSettingsCard and
// every rate below converts to it, and before the merge the lock reason was
// explained on one tab while the consequence landed on another. The company
// profile stood between them until now, so the claim of adjacency was made by a
// comment and not by the page.
function GeneralTab() {
  return (
    <>
      <InstallationSettingsCard />
      <FxRatesCard />
      <CompanyContextCard />
    </>
  );
}

// The shape a record takes: which fields it carries, which stages it moves
// through, and the priced things that go on an offer. Four surfaces that were
// three separate screens behind door-cards and one editor inline — a door is
// not a section, and the doors are gone.
function DataModelTab() {
  return (
    <>
      <CustomFieldsAdmin />
      <PipelinesCard />
      <ProductsAdmin />
      <OfferTemplatesAdmin />
    </>
  );
}

const SETTINGS_GROUPS = ["you", "org"] as const;

// The route this screen answers, named once: the shell mounts the settings level
// by matching it, and the section published below declares it.
export const SETTINGS_SCREEN = "settings";

type OrgTabId = Extract<(typeof SETTINGS_TABS)[number], { group: "org" }>["id"];

// Which Organization entries this principal can use, one answer per entry, each
// asking for the grant the cards on it ask for. The nav then describes the seat
// instead of the role name it was assigned: a principal granted product writes by
// an edited role reaches the data model, and nobody is offered a page whose every
// card would refuse them.
//
// OPENING AN ENTRY IS A READ, so every predicate here asks for a READ grant.
//
// They asked for write grants before, because each was written to answer "can you
// USE this", and the cost was measured against the live API: a read-only seat was
// hidden from eight of eleven entries the server answers 200 on — including three
// surfaces (products, offer templates, custom fields) that were ungated routes of
// their own before the merge. A client that hides a page the server serves is not
// protecting anything; it is disagreeing with the authority. So the rule is one
// rule for all of them: the entry opens if the principal may READ any part of it,
// and the write affordances inside say for themselves who may use them.
//
// A read grant is not a formality even where every seeded role holds it. The
// predicate asks the live grant, so a role edited to drop `custom_field:read`
// loses the Data model row — which `true` could never express, and which is the
// difference between a predicate that happens to be satisfied and no predicate.
//
// A merged entry takes the UNION of what its parts asked for, never the
// intersection: an entry that opened before this change must still open, or a
// restructure quietly becomes a permission change. Where a part is narrower than
// its page, that part gates itself inside — and a part withheld by a PERMISSION
// says so rather than vanishing (design-system/README.md).
//
// The licensing seat is deliberately not folded in (see capability.ts): a read
// seat still reads the pages behind these entries.
//
// EVERY predicate is evaluated here, unconditionally, before anything composes
// them. The number of hooks a render runs must not depend on which grants came
// back — so the `||` sits on the results, never around the calls, and no hook
// may move into the filter over the tab list.
/**
 * Which Organization entries this principal may open.
 *
 * Exported because the command palette must answer the SAME question: it offers a
 * shortcut to two of these entries, and a shortcut that lands on the Account
 * fallback is a command that lied. One predicate map, two readers.
 *
 * `probeCompanyFlag` is false for that caller. The company rollout flag is a
 * network read, and the palette is mounted on every screen while the settings rail
 * is mounted on one — firing it app-wide to answer a question the palette never
 * asks (it offers no shortcut to General) would spend a request per session for
 * nothing. With the flag unread, `general` falls back to the installation and
 * currency reads beside it, which is the honest answer for a caller that has not
 * asked whether the company profile exists.
 */
export function useSettingsEntryVisibility(
  probeCompanyFlag = true,
): Readonly<Record<OrgTabId, boolean>> {
  const capabilities = useCompanyContextCapabilities(probeCompanyFlag);
  const pipeline = useCan("pipeline", "read");
  const product = useCan("product", "read");
  const offerTemplate = useCan("offer_template", "read");
  const customField = useCan("custom_field", "read");
  const fxRate = useCan("fx_rate", "read");
  const aiModelRate = useCan("ai_model_rate", "read");
  const embeddingReindex = useCan("embedding_reindex", "read");
  const organization = useCan("organization", "read");
  const installation = useCan("installation_settings", "read");
  const captureSettings = useCan("capture_settings", "read");
  const automation = useCan("automation", "read");
  const webhook = useCan("webhook_subscription", "read");
  // The consent registry's server gate, which is not a role and not "any member":
  // consent/store.go's ListPurposes calls auth.Require(ctx, "person", read).
  const person = useCan("person", "read");
  const overlay = useCan("overlay_connection", "read");
  // The one predicate below that is a ROLE rather than a grant. `GET /admin/reset-data`
  // and the job-health read are gated on the literal admin role server-side and no
  // RBAC object describes them — a `role` object would encode a constant, and an
  // admin who revoked their own grant on it could never restore it (capability.ts).
  // Everything else above is a `read`, because opening a page is reading it.
  const isAdmin = useHoldsAdminRole();
  return {
    // The organization, its profile and its currency table are one entry now, so
    // the predicate is the union of what they each asked for. Each is gated on
    // the SAME live grant the card inside asks for rather than on a role name:
    // deriving it from admin/ops would disagree with the cards in both
    // directions — an admin whose installation_settings grant was removed would
    // get a page of disabled fields, and a principal holding the grant under an
    // edited role could not reach the surface they may use.
    //
    // The company profile carries a second condition that is a rollout FLAG
    // rather than a permission, so its grant ANDs with it: PUT /company is gated
    // on organization writes, and the flag says whether the surface exists on
    // this installation at all.
    general:
      installation ||
      (organization && (capabilities.data?.read_enabled ?? false)) ||
      fxRate,
    // The member roster, the roles on it, and what a role may reach. This is the
    // one entry with no grant to ask for: no RBAC object describes identity
    // administration and none can — a `role` object would encode a constant, and
    // an admin who revoked their own grant on it could never restore it
    // (capability.ts) — so the server gates it on the role directly.
    //
    // Open to every member, because the server is: `GET /users` answers 200 to
    // any authenticated principal, and the roster is the answer to "who is on my
    // team, and what may they do", which is not an admin's private question. The
    // invite form and every role control below it are the admin's, and withhold
    // themselves. Membership is the honest predicate here, and every principal
    // reaching this code has it by construction.
    people: true,
    capture: captureSettings,
    // The installation's own outside wiring — the shared provider credential, the
    // outbound subscriptions, the incumbent mirror. Either read opens it, and the
    // provider card carries no grant of its own because the server answers for it.
    //
    // The system-of-record chip in the topbar is shown to EVERY seat and points
    // here, so an entry this narrow would strand whoever follows it on the Account
    // fallback — the overlay read every seeded role holds is what keeps that link
    // honest, and it is a live grant rather than an exemption.
    integrations: webhook || overlay,
    // Everything that defines the shape a record takes: the field editor, the
    // pipeline designer, the product list, the offer templates. Any one of their
    // reads opens the page; the authoring controls inside each ask for their own
    // write.
    "data-model": customField || pipeline || product || offerTemplate,
    // The automations the installation runs, what it spent, and what it charges
    // per model. The automations read is the one every seeded role holds, and it
    // is what keeps this page reachable for manager, rep and read_only — the
    // server answers their automations read 200, and the editor was a route of
    // its own before this page absorbed it.
    ai: automation || aiModelRate,
    // The consent purpose registry, the retention ladder, the subject-request
    // queue and the audit trail. `consent_config` is a governed object upstream and
    // absent from the shipped RBAC vocabulary, so there is no grant NAMED for the
    // registry — but the server does not gate it on a role either: ListPurposes
    // demands `person:read`, so that is the grant to ask for, and asking it is what
    // keeps this from being `true` standing in for a permission. Every seeded role
    // holds it, and a role edited to drop it would otherwise reach a page of four
    // refusals. The three surfaces below the registry are narrower and each says so.
    privacy: person,
    // The operational verbs, and the one entry that genuinely narrows. The reindex
    // read is admin/ops; job health and the danger zone are admin-ONLY (the server
    // spells both with RequireAdmin), so an ops seat reaches this page for the
    // reindex and finds the other two withheld. Nobody below ops has anything to
    // read here at all. The reindex is an ordinary grant an edited role can hold, so
    // the entry opens on either and the cards inside decide.
    maintenance: isAdmin || embeddingReindex,
  };
}

// Which tabs this principal may use, and which of them the route selects. The
// nav in the sidebar and the content on the page both read this, so the two
// cannot disagree about what is current — including on the fallback below.
function useVisibleSettingsTabs(tab?: string) {
  const orgTabVisible = useSettingsEntryVisibility();
  const tabs = SETTINGS_TABS.filter(
    (entry) => entry.group !== "org" || orgTabVisible[entry.id],
  );
  // Unknown / absent id (or one this principal cannot see) falls back to the
  // first visible tab — a stale deep-link lands on Account, never a blank
  // screen.
  return { tabs, active: tabs.find((entry) => entry.id === tab) ?? tabs[0] };
}

/**
 * The settings level, as data the sidebar can render.
 *
 * The shell asks for this and renders it as the second navigation level; it
 * never learns what a grant is. The two groups are the ones this screen has
 * always had — "You" is per-user work, "Organization" is posture an admin
 * curates — and a group with no visible member is dropped rather than printed
 * empty. They are named for the SUBJECT rather than repeating the word the level
 * above them already carries: "Settings / Your settings / …" said it twice in a
 * 200px column.
 */
export function useSettingsSection(tab?: string): NavSection {
  const { tabs, active } = useVisibleSettingsTabs(tab);
  // Both message keys are composed from the ids, and both annotations are what
  // make them KEYS: a template literal narrows to the catalog's union only where
  // something expects one, and unannotated it would compile as any old string —
  // an unknown key has to stay a compile error.
  const groups = SETTINGS_GROUPS.map(
    (group): NavLevelGroup => ({
      headingKey: `settings.group.${group}`,
      items: tabs
        .filter((entry) => entry.group === group)
        .map(
          (entry): NavLevelEntry => ({
            id: entry.id,
            labelKey: `settings.tab.${entry.id}`,
            icon: entry.icon,
          }),
        ),
    }),
  ).filter((group) => group.items.length > 0);
  return {
    screen: SETTINGS_SCREEN,
    titleKey: "nav.settings",
    activeId: active.id,
    groups,
  };
}

export function SettingsScreen({ tab }: Readonly<{ tab?: string }>) {
  const { active } = useVisibleSettingsTabs(tab);
  // No nav column and no heading of its own: the entries are the sidebar's second
  // level now, and the shell's page head names the entry, so the page is that
  // entry's own content across the whole reading column.
  //
  // Every page sets its rhythm HERE rather than relying on the shell's
  // `.wrap > .card + .card` default, because that rule matches a card following a
  // card and settings pages are no longer stacks of cards: the merge left several
  // holding a `<form>`, a `<section>`, a flex wrapper or a bare heading-plus-table.
  // Where the rule missed, the gap was ZERO and two surfaces read as one. Owning
  // it once is the difference between twelve pages that space correctly and
  // twelve that each have to remember to.
  return (
    <div className="wrap">
      <ResumeConnectBanner />
      <div className="settings-stack">{tabContent(active.id)}</div>
    </div>
  );
}

// The organization's AI, read in the order the questions arrive: WHAT RUNS
// unattended, what it spent, what it charges per model, and — last, because it is
// a debugging instrument rather than a setting — the per-call trace.
//
// The old order was exactly backwards. It opened on spend and put the automations
// that CAUSE the spend at the bottom, four screens down, past a price table with
// one row per model; and for manager, rep and read_only the two spend cards say
// only that they are withheld, so the page opened on a price sheet and buried the
// one surface they came to use.
//
// Automations render here rather than in the rail: they are set-and-forget
// configuration, and the product already offered a second door to them from
// this very tab. They gate themselves per affordance on the automation grants.
//
// Every card here gates ITSELF, which is why this composes four unconditionally.
// The spend card and the call trace are reads the server gates on
// automation:update — the AI runtime's spend is treated as operator information,
// so seeing it takes the automation write grant and not any AI-named object — and
// each keeps its place and says so rather than vanishing, because an absent spend
// card claims nothing was spent. The gate belongs in the card for the same reason
// the retention ladder's does: a caller composing a page cannot state a denial on
// a surface's behalf.
function AiSettingsTab() {
  return (
    <>
      <AutomationsAdmin />
      <AiUsageCard />
      <ModelCostsCard />
      <AiCallsCard />
    </>
  );
}

// This person's own agent authority: what an agent may do unattended, the
// credentials they have minted, the clients holding one, and the governed tools
// those credentials reach. Every seat gets it, ungated — a passport is lent by
// the human, so an admin-only surface here would mean only admins could lend.
function AgentsTab() {
  return (
    <>
      <AutonomyCard />
      <PassportCard />
      {/* Directly after the passports, because it is the second half of one
          story: mint a passport, then lend it to a client that connects. */}
      <ConnectedAgentsCard />
      <AgentToolsCard />
    </>
  );
}

function IdentityCard() {
  const t = useT();
  const query = useMe();
  const logout = useLogout();
  return (
    <Card title={t("settings.identity")}>
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
        style={{ marginTop: "var(--space-3)" }}
      >
        {t("auth.signOut")}
      </Button>
    </Card>
  );
}

/**
 * This person's own preferences: how the product looks, and which language it
 * reads in.
 *
 * They sit beside the identity card because that is what they belong to — a
 * reader looking for their theme looks under their own account, not under an
 * account MENU in the sidebar, which is for the three places it can take you.
 * Both controls drive the state the rest of the app already reads (`app/theme.ts`
 * and the locale context), so nothing here is a second source of truth: the
 * theme survives a reload because that store persists it, and the language lasts
 * as long as the session does because the context is where it lives.
 */
// The sign-off appended below every message this member sends.
//
// It lives beside identity rather than under the composer because it is who
// the sender IS, not something about one mail: a signature written per message
// would be a different signature every time, which is the opposite of what one
// is for. Plain text, because the transport sends text/plain — markup here
// would arrive as tags in the message.
function EmailSignatureCard() {
  const t = useT();
  const queryClient = useQueryClient();
  const [body, setBody] = useState<string | null>(null);
  const signature = useQuery({
    queryKey: ["me-email-signature"],
    queryFn: async () => {
      const { data, error } = await api.GET("/me/email-signature");
      if (error) {
        throwProblem(error);
      }
      return data ?? { body: "" };
    },
  });
  const save = useMutation({
    mutationFn: async (next: string) => {
      const { data, error } = await api.PUT("/me/email-signature", {
        body: { body: next },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (saved) => {
      // Hand the edit back to the server's answer. It trims what it stores, so
      // a member who typed trailing spaces would otherwise keep seeing them
      // over a row that no longer has them — with Save still lit, offering to
      // save a difference that exists only in the browser.
      setBody(saved?.body ?? "");
      queryClient.invalidateQueries({ queryKey: ["me-email-signature"] });
    },
  });

  // The saved value until the member types; theirs from then on. Reading state
  // straight from the query would discard every keystroke the moment a refetch
  // landed underneath them.
  const shown = body ?? signature.data?.body ?? "";

  return (
    <Card
      title={t("settings.signature")}
      sub={t("settings.signatureSub")}
      actions={
        <Button
          small
          disabled={save.isPending || shown === (signature.data?.body ?? "")}
          onClick={() => save.mutate(shown)}
        >
          {save.isPending ? t("settings.signatureSaving") : t("record.save")}
        </Button>
      }
    >
      <div className="form-stack">
        <Field label={t("settings.signatureLabel")}>
          {(control) => (
            <Textarea
              {...control}
              rows={5}
              value={shown}
              placeholder={t("settings.signaturePlaceholder")}
              onChange={(event) => setBody(event.target.value)}
            />
          )}
        </Field>
        <p className="t-caption">{t("settings.signatureHint")}</p>
        {save.isError && (
          <p className="t-caption" style={{ color: "var(--danger)" }}>
            {problemMessageOf(save.error, t)}
          </p>
        )}
      </div>
    </Card>
  );
}

function PreferencesCard() {
  const t = useT();
  const [theme] = useTheme();
  const { locale, setLocale } = useLocale();
  return (
    <Card title={t("settings.preferences")} sub={t("settings.preferencesSub")}>
      <div className="form-stack">
        {/* Not a `Field`: a SegmentedControl is a `fieldset`, and a <label for>
            pointing at one names nothing. So the name rides on the control
            itself and this line is the eye's copy of it — the same words, which
            is what WCAG 2.5.3 asks of a voice user who says what they can read. */}
        <div className="field">
          <span className="t-label">{t("shell.theme")}</span>
          <SegmentedControl
            options={THEMES}
            value={theme}
            onChange={setTheme}
            label={t("shell.theme")}
            labels={{ light: t("theme.light"), dark: t("theme.dark") }}
          />
        </div>
        <Field label={t("locale.switchLabel")}>
          {(control) => (
            <Select
              {...control}
              value={locale}
              // `Select` reports a string. The options are built from LOCALES,
              // so narrowing the answer through that same list is what makes it
              // a Locale — no assertion, and nothing acted on that the control
              // was never offering.
              onChange={(next) => {
                const picked = LOCALES.find((option) => option === next);
                if (picked) {
                  setLocale(picked);
                }
              }}
              options={LOCALES.map((option) => ({
                value: option,
                label: t(localeNameKey(option)),
              }))}
            />
          )}
        </Field>
      </div>
    </Card>
  );
}

const PASSPORT_SCOPES = ["read", "draft", "write", "send", "enrich"] as const;

// The scope's wire token is what the server reads; a person choosing what to lend
// an agent needs the sentence. Composed rather than switched, and annotated so an
// added scope is a missing-key compile error rather than a checkbox that quietly
// labels itself `enrich` in every language.
function scopeLabelKey(scope: (typeof PASSPORT_SCOPES)[number]): MessageKey {
  return `passport.scope.${scope}`;
}

function PassportCard() {
  const t = useT();
  const { locale } = useLocale();
  const [label, setLabel] = useState("");
  const [scopes, setScopes] = useState<Set<string>>(new Set(["read", "draft"]));
  const [confirmId, setConfirmId] = useState<string | null>(null);
  const revokingRow = useRef<HTMLLIElement | null>(null);
  const labelId = useId();

  // Metadata only — the wire schema carries no token (PassportSummary),
  // so this list cannot re-disclose one.
  const list = useQuery({
    queryKey: ["passports"],
    queryFn: async () => {
      const { data, error } = await api.GET("/passports");
      if (error) {
        throwProblem(error);
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
        throwProblem(error);
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
        throwProblem(error);
      }
    },
    onSuccess: async () => {
      // Refetch BEFORE closing, so the row focus returns to is already carrying
      // the revoked state it is meant to announce. Closing first restores focus
      // against the pre-revoke DOM and reads the row back unchanged.
      await list.refetch();
      setConfirmId(null);
    },
  });

  return (
    <Card title={t("settings.passports")} sub={t("settings.passportsSub")}>
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
          <Checkbox
            key={scope}
            className="t-caption"
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
            label={t(scopeLabelKey(scope))}
          />
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
        <Card as="div" inset style={{ marginTop: "var(--space-3)" }}>
          <p className="t-label">{t("settings.tokenOnce")}</p>
          <p
            className="t-mono"
            style={{ wordBreak: "break-all", marginTop: 4 }}
          >
            {mint.data.token}
          </p>
        </Card>
      )}
      {mint.isError && (
        <p
          className="t-caption"
          style={{ color: "var(--danger)", marginTop: 8 }}
        >
          {problemMessageOf(mint.error, t)}
        </p>
      )}
      <p className="t-small" style={{ marginTop: "var(--space-2)" }}>
        {t("settings.passportsLendHint")}
      </p>
      {/* Only what this human MINTED. A row carrying a connection was issued by
          the token exchange to a client — it belongs to ConnectedAgentsCard,
          and listing it here put a raw DCR client id among the names the human
          chose. `connection` is the server's own statement of which kind a row
          is; the `oauth:` label prefix is display text and decides nothing. */}
      <QueryGate
        query={list}
        empty={(page) =>
          page.data.every((passport) => passport.connection != null)
        }
      >
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
            {page.data
              .filter((passport) => passport.connection == null)
              .map((passport) => {
                const revoked = passport.revoked_at != null;
                return (
                  <li
                    key={passport.id}
                    data-passport={passport.id}
                    // Reachable by focus() without joining anybody's Tab order:
                    // the revoke confirm hands focus back here, because the
                    // button it was opened from is gone by then.
                    tabIndex={-1}
                    style={{
                      display: "flex",
                      gap: "var(--space-2)",
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
                    <ScopeChips scopes={passport.scopes} />
                    <span className="t-small">
                      {t("settings.created", {
                        date: formatDate(
                          passport.created_at,
                          locale,
                          "Europe/Berlin",
                        ),
                      })}
                    </span>
                    {/* A credential's lifetime is a personal deadline, so it
                        reads on the viewer's own calendar — the same
                        zone-by-purpose split the consent screen makes. created_at
                        above stays the fixed record zone. */}
                    {passport.expires_at && (
                      <span className="t-small">
                        {t("settings.expires", {
                          date: formatDate(
                            passport.expires_at,
                            locale,
                            Intl.DateTimeFormat().resolvedOptions().timeZone,
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
                        // The row is remembered from the CLICK rather than from
                        // `confirmId`: the focus resolver runs as the dialog
                        // closes, by which time that state is already back to
                        // null and there is nothing left to look the row up by.
                        onClick={(event) => {
                          revokingRow.current =
                            event.currentTarget.closest<HTMLLIElement>("li");
                          setConfirmId(passport.id);
                        }}
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
        error={revoke.error ? problemMessageOf(revoke.error, t) : null}
        // The revoked passport's own row, which survives the DELETE as a
        // struck-through entry carrying the "revoked" badge — so focus lands on
        // the outcome, at the place the reader was working. The Revoke button
        // they pressed cannot be the target: it is what the badge replaced.
        returnFocusTo={() => revokingRow.current}
      >
        <p>{t("settings.revokeConfirm")}</p>
      </ConfirmModal>
    </Card>
  );
}

// The read-only tool console (IT-1): the same governed surface an MCP client
// sees — GET /agent-tools, with an optional passport selector that strikes
// through any row the selected passport's granted scopes don't cover. No passport
// picked means every row reads as reachable (the unfiltered inventory).
function AgentToolsCard() {
  const t = useT();
  const [passportId, setPassportId] = useState<string>("");
  const tools = useQuery({
    queryKey: ["agent-tools"],
    queryFn: async () => {
      const { data, error } = await api.GET("/agent-tools");
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  const passports = useQuery({
    queryKey: ["passports"],
    queryFn: async () => {
      const { data, error } = await api.GET("/passports");
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  // Live, and the human's OWN to lend. A connection's credential is neither:
  // the server refuses to lend a grant-bound passport (identity's
  // lendablePassportPredicate), so offering one here would name a choice the
  // consent screen cannot honour — and would put a raw DCR client id back in
  // front of a reader the rest of this change just took it away from.
  const lendable = (passports.data?.data ?? []).filter(
    (p) => p.revoked_at == null && p.connection == null,
  );
  // The filter follows the selector: a passport revoked while it was the
  // chosen scope drops out of the options, and the <select> then shows "all
  // passports" — so the inventory must read as unfiltered too, rather than
  // stay quietly scoped to a credential no longer on offer.
  const scopeId = lendable.some((p) => p.id === passportId) ? passportId : "";
  const grantedScopes = new Set(
    lendable.find((p) => p.id === scopeId)?.scopes ?? [],
  );

  return (
    <Card title={t("tools.title")} sub={t("tools.sub")}>
      {passports.data && passports.data.data.length > 0 && (
        <div className="tool-scope-filter">
          <PassportSelect
            options={lendable.map((p) => ({
              id: p.id,
              label: t("tools.scopedTo", { label: p.label }),
              scopes: p.scopes,
            }))}
            value={scopeId}
            onChange={setPassportId}
            allowEmpty
            emptyLabel={t("tools.scopeAll")}
            ariaLabel={t("tools.scopeAll")}
          />
        </div>
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
                !scopeId ||
                tool.required_scope == null ||
                grantedScopes.has(tool.required_scope);
              return (
                <li key={tool.name} data-tool={tool.name} className="tool-row">
                  <div className="tool-row-head">
                    <AutonomyDot tier={dotTier(tool.tier)} />
                    {/* Struck, not dimmed. Dimming the row to 0.4 took the whole
                        row under the AA contrast floor (B-EP09.21) — including
                        the caption below that is supposed to be the text
                        equivalent of the dimming, so the one part a reader needs
                        most became the hardest to read. The passport list and the
                        connected-agent rows both chose the strikethrough over
                        dimming for exactly this reason.
                        It wraps the FACTS about the tool and nothing else: the
                        badges state its governance, which is true either way, and
                        the caption is the explanation. */}
                    <span
                      className="t-mono"
                      style={{
                        color: "var(--accent)",
                        textDecoration: reachable ? undefined : "line-through",
                      }}
                    >
                      {tool.name}
                    </span>
                    {tool.title && (
                      <span
                        className="t-caption"
                        style={{
                          textDecoration: reachable
                            ? undefined
                            : "line-through",
                        }}
                      >
                        {tool.title}
                      </span>
                    )}
                    {tool.required_scope && (
                      <Badge>{tool.required_scope}</Badge>
                    )}
                    {tool.egress && (
                      <Badge tone="warn">{t("tools.egress")}</Badge>
                    )}
                    {!reachable && (
                      <span className="t-caption">
                        {t("tools.unreachable")}
                      </span>
                    )}
                  </div>
                  {/* The text an agent actually selects on. This console
                      promises the surface an MCP client sees, and the name
                      alone was never that. */}
                  {tool.description && (
                    <p className="t-caption tool-description">
                      {tool.description}
                    </p>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </QueryGate>
    </Card>
  );
}

// The danger-zone reset action: wipes a non-production installation back to
// its first-boot state. Double-gated client-side — the admin role AND the
// server-driven `non_production` posture on /me (never VITE_UI_PREVIEW_RESET,
// which is the unrelated password-reset link) — so the affordance is invisible
// on a production install even to an admin; the server enforces both the
// same way and 404s the endpoint outright in production regardless of what
// this card renders. This is admin-ONLY, and narrower than the Maintenance entry
// that hosts it — that entry opens on the embedding_reindex read, so an ops seat
// reaches it for the search index and simply finds no reset control. The
// server's auth.RequireAdmin on /admin/reset-data admits only the literal
// "admin" role (mirrors users-admin.tsx's isAdmin check), so neither a manager
// nor an ops user may see a button that can only 403. The organization's name
// is not carried on MeResponse, so this never fetches or compares it
// client-side: the input just has to be non-empty to enable the confirm
// button, and the server is the sole judge of whether the typed text actually
// matches (a mismatch comes back as a 422, surfaced verbatim in the dialog).
// The full reset response — derived from the generated operation type
// (T6: no `as`, no hand-duplicated field list) so a wire change that adds or
// renames a counter fails typecheck here instead of silently going unshown.
type ResetSummary =
  operations["resetData"]["responses"][200]["content"]["application/json"];

function ResetDataCard() {
  const t = useT();
  const me = useMe();
  const isAdmin = useHoldsAdminRole();
  const workspaceName = me.data?.workspace_name ?? "";
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState("");
  // What the last reset actually cleared — null until one has run, so the
  // danger zone stays quiet on first render rather than implying a result
  // nobody triggered.
  const [summary, setSummary] = useState<ResetSummary | null>(null);
  const queryClient = useQueryClient();

  const reset = useMutation({
    mutationFn: async () => {
      // The summary always describes the latest attempt, never a prior one:
      // clearing here means a retry's error can never leave a previous
      // success sitting on screen, and an in-flight retry shows no summary.
      setSummary(null);
      const { data, error } = await api.POST("/admin/reset-data", {
        body: { confirmation: typed },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (data) => {
      setOpen(false);
      setTyped("");
      setSummary(data ?? null);
      // A reset wipes every domain table for the workspace — every cached
      // list/detail query is stale, not just the ones this card knows about.
      queryClient.invalidateQueries();
    },
  });

  if (!isAdmin || !me.data?.non_production) {
    return null;
  }

  return (
    <Card
      title={t("settings.dangerZone")}
      sub={t("settings.dangerZoneSub")}
      // The one card that announces its own danger: the red border is what
      // separates a destructive surface from the ordinary settings around it.
      style={{ borderColor: "var(--danger)" }}
    >
      <p className="t-caption">{t("settings.resetDataDesc")}</p>
      <Button
        small
        variant="danger"
        onClick={() => setOpen(true)}
        style={{ marginTop: "var(--space-3)" }}
      >
        {t("settings.resetDataButton")}
      </Button>
      {summary && (
        <p
          className="t-caption"
          role="status"
          style={{ marginTop: "var(--space-2)" }}
        >
          {t("settings.resetDataResult", {
            tables: summary.tables_cleared,
            jobs: summary.jobs_deleted,
            streams: summary.streams_purged,
            keys: summary.cache_keys_deleted,
            objects: summary.objects_deleted,
          })}
        </p>
      )}
      {summary?.drain_timed_out && (
        <p
          className="t-caption"
          role="alert"
          style={{ color: "var(--warn)", marginTop: "var(--space-1)" }}
        >
          {t("settings.resetDataDrainWarning")}
        </p>
      )}
      <ConfirmModal
        open={open}
        onClose={() => {
          // Don't let Escape/backdrop dismiss the dialog mid-request: closing
          // re-enables the outer button while the first destructive POST is
          // still in flight (reset.reset() clears mutation state but cannot
          // abort the sent request), which would allow a second reset.
          if (reset.isPending) {
            return;
          }
          setOpen(false);
          setTyped("");
          reset.reset();
        }}
        title={t("settings.resetDataConfirmTitle")}
        confirmLabel={t("settings.resetDataButton")}
        confirmVariant="danger"
        confirmDisabled={typed.trim() === "" || reset.isPending}
        onConfirm={() => reset.mutate()}
        pending={reset.isPending}
        error={reset.error ? problemMessageOf(reset.error, t) : null}
      >
        <p>{t("settings.resetDataConfirmBody")}</p>
        {workspaceName ? (
          <p className="t-caption">
            {t("settings.resetDataConfirmName")}{" "}
            {/* userSelect:all lets one click select the whole name to copy */}
            <code style={{ userSelect: "all", fontWeight: 600 }}>
              {workspaceName}
            </code>
          </p>
        ) : null}
        <TextInput
          aria-label={t("settings.resetDataConfirmLabel")}
          value={typed}
          onChange={(event) => setTyped(event.target.value)}
        />
      </ConfirmModal>
    </Card>
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
        error={mutation.isError ? problemMessageOf(mutation.error, t) : null}
        onSubmit={(values) => mutation.mutate(values)}
      />
    </>
  );
}

// The removal half of the bounded stage surface. Both refusals are the
// server's — a stage still holding deals, and the terminal won/lost pair —
// so this asks and then shows what it was told rather than pre-judging
// from the row: the refusal names the deals standing in the way, which is
// the part an admin acts on, and a board read a minute ago would name the
// wrong ones.
function StageRemove({ stage }: Readonly<{ stage: Stage }>) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const queryClient = useQueryClient();
  // Removal is pipeline:delete, not the pipeline:update everything else on
  // this card runs on — the server gates it that way, so a principal who
  // may add and rename stages but not remove one is not shown a control
  // that could only ever answer 403. Read before the early return: the
  // hooks a render performs must not depend on the answer.
  const canRemove = useCanWrite("pipeline", "delete");
  const remove = useMutation({
    mutationFn: async () => {
      const { error } = await api.DELETE("/stages/{id}", {
        params: { path: { id: stage.id } },
      });
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: async () => {
      // The refetched pipelines FIRST, then the dialog: closing it hands
      // focus back to a list that must no longer hold this row.
      await queryClient.invalidateQueries({ queryKey: ["pipelines"] });
      setOpen(false);
    },
  });
  // A refusal is about the workspace's state, not the dialog's: reopening
  // must ask again rather than reprint what the last attempt was told.
  const close = () => {
    remove.reset();
    setOpen(false);
  };
  if (!canRemove) {
    return null;
  }
  return (
    <>
      <Button
        small
        variant="danger"
        data-testid={`remove-stage-${stage.id}`}
        onClick={() => setOpen(true)}
      >
        {t("stage.remove")}
      </Button>
      <ConfirmModal
        open={open}
        onClose={close}
        title={t("stage.removeTitle")}
        confirmLabel={t("stage.removeConfirm")}
        confirmVariant="danger"
        pending={remove.isPending}
        error={remove.isError ? problemMessageOf(remove.error, t) : null}
        onConfirm={() => remove.mutate()}
      >
        <p className="t-small">{t("stage.removeBody", { name: stage.name })}</p>
      </ConfirmModal>
    </>
  );
}

function StageRow({
  stage,
  canEdit,
  t,
}: Readonly<{
  stage: Stage;
  canEdit: boolean;
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
      {canEdit && (
        <span
          style={{
            display: "flex",
            gap: "var(--space-2)",
            alignItems: "center",
          }}
        >
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
          <StageRemove stage={stage} />
        </span>
      )}
    </li>
  );
}

function PipelineRow({
  pipeline,
  canEdit,
  t,
}: Readonly<{
  pipeline: Pipeline;
  canEdit: boolean;
  t: ReturnType<typeof useT>;
}>) {
  const stages = [...(pipeline.stages ?? [])].sort(
    (a, b) => a.position - b.position,
  );
  return (
    <Card as="div" inset style={{ marginBottom: "var(--space-3)" }}>
      <div
        style={{
          display: "flex",
          gap: 8,
          alignItems: "center",
          flexWrap: "wrap",
        }}
      >
        {/* A real heading, not a span wearing heading type. The pipeline is the
            subject of everything in this row and the stage list under it, so a
            reader navigating by heading has to be able to land on it — before this
            the Pipelines card was one outline node with an unreachable interior.
            h3: the card's own title is the h2 under the page's h1. */}
        <h3 className="t-h2" style={{ margin: 0 }}>
          {pipeline.name}
        </h3>
        <Badge tone={pipeline.is_default ? "success" : undefined}>
          {pipeline.is_default
            ? t("pipeline.default")
            : t("pipeline.notDefault")}
        </Badge>
        {canEdit && (
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
          <StageRow key={stage.id} stage={stage} canEdit={canEdit} t={t} />
        ))}
      </ul>
    </Card>
  );
}

// D-8: Settings → Pipelines config. Reads via the SAME ["pipelines","all"]
// key the deals screen's plural selector uses (an array shape, distinct
// from DealScreen's single-pipeline ["pipelines"] cache entry) — any
// mutation here invalidates the ["pipelines"] prefix, so both shapes stay
// fresh. The list itself is readable by everyone; only the write affordances are
// gated, and the server stays the RBAC authority. Three of the five seeded roles
// hold pipeline READ and no write verb at all, so for most readers this card is
// the read-only case rather than an edge of it — which is why it states that
// posture once instead of leaving a reader to infer it from absent buttons.
export function PipelinesCard() {
  const t = useT();
  // Adding a pipeline is pipeline:create. Everything else here — renaming a
  // pipeline, adding a stage, editing one, reordering — is pipeline:update,
  // including the stage CREATE affordance: a stage is not its own RBAC object,
  // so adding one is an update to the pipeline that owns it.
  const canCreate = useCanWrite("pipeline", "create");
  const canEdit = useCanWrite("pipeline", "update");
  const query = useQuery({
    queryKey: ["pipelines", "all"],
    queryFn: async () => {
      const { data, error } = await api.GET("/pipelines", {
        params: { query: {} },
      });
      if (error) {
        throwProblem(error);
      }
      return data.data;
    },
  });
  return (
    <Card title={t("settings.pipelines")} sub={t("settings.pipelinesSub")}>
      {/* Said once, at the top, rather than annotating each absent control — the
          rule in design-system/README.md. A reader holding one of the two verbs
          can see for themselves which controls they got. */}
      {!canCreate && !canEdit && (
        <p className="t-caption" style={{ marginBottom: "var(--space-3)" }}>
          {t("settings.pipelinesReadOnly")}
        </p>
      )}
      {canCreate && (
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
                canEdit={canEdit}
                t={t}
              />
            ))}
          </>
        )}
      </QueryGate>
    </Card>
  );
}

// The two-tier table (03b): informational, and the advance-stage row is
// locked 🟡 — there is no toggle that could soften it (AC-settings).
function AutonomyCard() {
  const t = useT();
  return (
    <Card title={t("settings.autonomy")} sub={t("settings.autonomySub")}>
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
    </Card>
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

// The unfiltered question the view opens on. One object rather than six
// useState strings, so the filter row can be its own component that hands back
// a whole answer instead of taking six setters — and so the query key is that
// same answer, which cannot drift from what the request carries.
const UNFILTERED_AUDIT_LOG: AuditLogFilters = {
  actor: "",
  entityType: "",
  entityId: "",
  action: "",
  from: "",
  to: "",
};

// The six filters, declared once. Naming them here keeps the accessible wiring
// identical across the row: each control is named by the `t-label` span beside
// it via aria-labelledby, because a real <label> per control would wrap every
// field onto its own line and the row is what makes six filters scannable.
const AUDIT_LOG_FILTER_FIELDS: readonly Readonly<{
  key: keyof AuditLogFilters;
  labelKey: MessageKey;
  // A calendar picker rather than free text — the two ends of the range.
  date?: boolean;
}>[] = [
  { key: "actor", labelKey: "settings.auditActor" },
  { key: "entityType", labelKey: "settings.auditEntity" },
  { key: "entityId", labelKey: "settings.auditEntityId" },
  { key: "action", labelKey: "settings.auditAction" },
  { key: "from", labelKey: "settings.auditFrom", date: true },
  { key: "to", labelKey: "settings.auditTo", date: true },
];

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

function AuditLogFilterFields({
  filters,
  onChange,
}: Readonly<{
  filters: AuditLogFilters;
  onChange: (next: AuditLogFilters) => void;
}>) {
  const t = useT();
  const filterId = useId();
  return (
    // Six narrow filters read as a grid of labelled cells rather than one long
    // row: in a row each control took the width of the card and the six of them
    // stacked into a page-tall form, which is the shape of something to fill in
    // rather than something to narrow a list with.
    <div
      style={{
        display: "grid",
        gridTemplateColumns: "repeat(auto-fit, minmax(11rem, 1fr))",
        gap: "var(--space-3)",
      }}
    >
      {AUDIT_LOG_FILTER_FIELDS.map((field) => {
        const labelId = `${filterId}-${field.key}`;
        return (
          <div
            key={field.key}
            style={{
              display: "flex",
              flexDirection: "column",
              gap: "var(--space-1)",
            }}
          >
            <span className="t-label" id={labelId}>
              {t(field.labelKey)}
            </span>
            <TextInput
              type={field.date ? "date" : undefined}
              aria-labelledby={labelId}
              value={filters[field.key]}
              onChange={(event) =>
                onChange({ ...filters, [field.key]: event.target.value })
              }
            />
          </div>
        );
      })}
    </div>
  );
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

// The result half of the audit view: the answer to whatever the filter row
// currently asks. Keyset "load more" via the page cursor, and a filter change
// is a new question — the filters ARE the query key, so changing one restarts
// the cursor chain instead of appending to a stale one.
// The first page asks for no cursor. Named and typed here rather than asserted at
// the call site: `initialPageParam: null` alone narrows the page param to `null`
// and then rejects the string cursors every page after the first carries.
const FIRST_AUDIT_PAGE: string | null = null;

function AuditLogEntries({
  filters,
  meUserId,
}: Readonly<{ filters: AuditLogFilters; meUserId?: string }>) {
  const t = useT();
  // The full trail is the admin's alone (AAD-ROLE-4/A91, enforced by
  // privacy.ListAuditLog), while the page it sits on opens for ops too — the
  // consent registry above is theirs. So the read is gated here rather than
  // merely rendered, and the fetch is disabled for anyone else: an ops seat
  // reaching this page must not issue a call that can only 403, and must not be
  // handed a red failure with a Retry that cannot succeed.
  const isAdmin = useHoldsAdminRole();
  const query = useInfiniteQuery({
    queryKey: ["audit-log", filters],
    enabled: isAdmin,
    initialPageParam: FIRST_AUDIT_PAGE,
    queryFn: async ({ pageParam }) => {
      const { data, error } = await api.GET("/audit-log", {
        params: { query: auditLogQueryParams(filters, pageParam) },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    getNextPageParam: (last) => last.page.next_cursor ?? null,
  });

  const entries = query.data?.pages.flatMap((page) => page.data) ?? [];

  // Honest state matrix (§3a): withheld, loading, error, empty, then the list —
  // kept as sequential branches rather than a nested ternary in the JSX below.
  let body: ReactNode;
  if (!isAdmin) {
    // Withheld rather than absent, and the card keeps its place: an absent trail
    // on a page that opens for ops would read as "nothing has happened here",
    // which is a different claim from "this is not yours to read". The same
    // choice the subject-request queue above it makes, for the same reason.
    body = (
      <EmptyState>
        <p className="t-small">{t("settings.auditAdminOnly")}</p>
      </EmptyState>
    );
  } else if (query.isPending) {
    body = (
      <div
        style={{
          display: "flex",
          flexDirection: "column",
          gap: "var(--space-3)",
        }}
      >
        <Skeleton width="60%" />
        <Skeleton width="90%" />
      </div>
    );
  } else if (query.isError) {
    body = (
      <EmptyState>
        <p>{t("common.error")}</p>
        <p className="t-mono" style={{ marginTop: "var(--space-2)" }}>
          {problemMessageOf(query.error, t)}
        </p>
        <Button
          small
          onClick={() => query.refetch()}
          style={{ marginTop: "var(--space-3)" }}
        >
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
            gap: "var(--space-3)",
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
    <Card title={t("settings.auditEntries")} sub={t("settings.auditSub")}>
      {body}
    </Card>
  );
}

// AC-settings-16: the attributable audit view — live filters over actor /
// entity_type / entity_id / action / from / to, keyset "load more" via the
// page cursor. Two cards, because the question and the answer are two things:
// six filters and a page of entries in one box made the row read as a header
// the entries belonged to, and the entries scroll while the filters stay put.
// Each row expands into the before/after diff plus the agent attribution trail
// (passport, on-behalf-of human, authorization rule, grounding evidence) —
// collapsed by default so the flat scan stays fast.
export function AuditLogCard() {
  const t = useT();
  // The current user's id resolves audit "You" vs "A teammate" in ActorTag.
  const meUserId = useMe().data?.user?.id;
  const isAdmin = useHoldsAdminRole();
  const [filters, setFilters] = useState<AuditLogFilters>(UNFILTERED_AUDIT_LOG);
  return (
    <>
      {/* The filter row is absent, not withheld, for a reader who may not read
          the trail: six inputs that narrow a list they cannot see are a control
          with nothing behind it. The ENTRIES card below stays and says why —
          absence there would claim nothing had happened. */}
      {isAdmin && (
        /* "Filters" here and the log's own name on the card BELOW, which is the
           one that holds it. The page head used to name the log — it was an
           entry of its own — and now says "Privacy & audit" over four cards, so
           a filter row and a card of "Entries" left nothing on the page saying
           which log this is. Naming the filters instead would put the name above
           the question rather than above the answer. */
        <Card title={t("settings.auditFilters")}>
          <AuditLogFilterFields filters={filters} onChange={setFilters} />
        </Card>
      )}
      <AuditLogEntries filters={filters} meUserId={meUserId} />
    </>
  );
}

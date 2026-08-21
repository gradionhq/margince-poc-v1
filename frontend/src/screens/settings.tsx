import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import {
  Activity,
  BadgeCheck,
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
import {
  type ReactNode,
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";
import { api, FIRST_PAGE } from "../api/client";
import type { components, operations } from "../api/schema";
import { dotTier } from "../app/autonomy";
import { useCan, useCanWrite, useHoldsAdminRole } from "../app/capability";
import { isEntityKind } from "../app/entity";
import { unitsForSecretScope } from "../app/extensions";
import type { NavLevelEntry, NavLevelGroup, NavSection } from "../app/nav";
import { ResumeConnectBanner } from "../app/resumeconnectbanner";
import { useUnsavedGuard } from "../app/unsaved";
import {
  Avatar,
  Badge,
  Button,
  Checkbox,
  EmptyState,
  Field,
  Modal,
  SectionHeader,
  Skeleton,
  Textarea,
  TextInput,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { ConfirmModal } from "../design-system/confirmmodal";
import { Eyebrow } from "../design-system/eyebrow";
import { Panel, PanelBody, PanelPlate, PanelRow } from "../design-system/panel";
import { PassportSelect, ScopeChips } from "../design-system/passportselect";
import { FieldGuard, RoleBadge } from "../design-system/rbac";
import { Select } from "../design-system/select";
import { ToastRegion, useToast } from "../design-system/toast";
import {
  AutonomyDot,
  EvidenceChip,
  FieldDiff,
  PassportChip,
  toEvidence,
} from "../design-system/trust";
import { formatDate, formatDateTime } from "../format/format";
import { LOCALES, type Locale, localeNameKey, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { AiRoutingCard } from "./ai-routing";
import { AiCallsCard } from "./aicalls";
import { AiUsageCard } from "./aiusage";
import { ActorTag } from "./audit";
import { AutomationsAdmin } from "./automations";
import { BlockedDomainsCard } from "./blocked-domains";
import { CaptureActivityTab } from "./capture-activity";
import { CaptureExclusionsCard } from "./capture-exclusions";
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
import { ExtensionUnitsCard } from "./extension-units";
import { ImportCard } from "./import";
import { InstallationSettingsCard } from "./installation-settings";
import { ProviderCard } from "./integrations-provider";
import { JobHealthCard } from "./jobhealth";
import {
  LeadDisqualifyReasonsCard,
  LeadHandlingCard,
  LeadSourcesCard,
} from "./leadvocab";
import { LicenseCard } from "./license";
import { LinkedInImportCard } from "./linkedin-import";
import { LinkedInReachCard } from "./linkedin-reach";
import { SEARCH_DEBOUNCE_MS } from "./listquery";
import { MailSharingCard } from "./mail-sharing";
import { OfferTemplatesAdmin } from "./offertemplates";
import { OverlayCard } from "./overlay";
import { MirrorUserMapCard } from "./overlay-usermap";
import { OwnDomainsCard } from "./own-domains";
import { ChangePasswordCard } from "./passwordcard";
import { ConsentPurposesCard, PrivacyInboxCard } from "./privacy";
import { ProductsAdmin } from "./products";
import { FxRatesCard, ModelCostsCard } from "./rates";
import { RestrictedRecordsCard } from "./restrictedrecords";
import { RetentionCard } from "./retention";
import { TeamsCard } from "./users-access";
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
// It used to be fifteen tabs plus nine routes outside them. What collapsed and
// why: two surfaces both called "Capture" became one; the
// installation and the company profile were always the same organization;
// currency rates joined the base currency they convert to while model prices
// joined the AI runtime they price; user administration and extension
// permissions are one question about authority; the field editor, pipeline
// designer, product list and offer templates all define the shape a record
// takes; and the operational verbs that were hiding beside the field editor — a
// reindex, job health, the danger zone — became a place of their own.
//
// One of those merges was later UNDONE: connectors and the overlay both answer
// "what are we connected to" and were merged on that reading, but the question
// has two different owners — see the split below. Capture activity is newer and
// additive: it answers what those connections DID, which no existing entry
// could say.
//
// No sentence here counts the entries. Three of them used to, and by the time
// anyone looked they said eleven, twelve and thirteen for a register holding
// fourteen — a number in prose beside a list is a second source of truth that
// nothing updates and no test can check. The list below is the count.
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
//
// Width is NOT an entry's business. Every page takes the whole column, so they
// line up with each other and with the rest of the app: a reader
// moving between two settings pages sees the content start and end where the
// last one did. A per-page measure buys a nicer form column at the cost of the
// page appearing to change size as you navigate, and of a knob each new entry
// has to answer for. Where a single control would otherwise stretch to the full
// column, the control constrains itself (`.settings-intrinsic`, and each
// surface's own field widths) — that is a property of the control, which knows
// how wide it wants to be, not of the page, which does not.
// Exported for the nav suite, which derives its expected label list from THIS
// register rather than restating it. A restated list is a second source of truth
// that nothing updates: the copy in the test omitted `license` for as long as
// that entry existed, so a fully wired fourteenth tab — register, predicate,
// content, sidebar deep link, two locales — was invisible to every assertion in
// the file, including the two that claim to check the whole level.
export const SETTINGS_TABS = [
  { id: "account", icon: UserRound, group: "you" },
  { id: "voice", icon: Mic, group: "you" },
  { id: "agents", icon: KeyRound, group: "you" },
  { id: "connections", icon: Plug, group: "you" },
  { id: "capture-activity", icon: Activity, group: "you" },
  { id: "general", icon: Building2, group: "org" },
  { id: "people", icon: UsersRound, group: "org" },
  { id: "integrations", icon: Webhook, group: "org" },
  { id: "capture", icon: Mail, group: "org" },
  { id: "data-model", icon: Database, group: "org" },
  { id: "ai", icon: Sparkles, group: "org" },
  { id: "privacy", icon: ShieldCheck, group: "org" },
  { id: "license", icon: BadgeCheck, group: "org" },
  { id: "maintenance", icon: Wrench, group: "org" },
] as const satisfies readonly {
  id: string;
  icon: LucideIcon;
  group: "you" | "org";
}[];

// Exported alongside the register: a caller that needs the label for an entry
// builds the key from this, and `settings.tab.${SettingsTabId}` is then a
// literal union TypeScript can check against MessageKey — no assertion, so a
// typo is a compile error rather than a lookup that silently falls back to the
// raw key and lets a test validate a label that does not exist.
export type SettingsTabId = (typeof SETTINGS_TABS)[number]["id"];

function tabContent(id: SettingsTabId): ReactNode {
  switch (id) {
    case "account":
      return (
        <>
          <IdentityCard />
          <ChangePasswordCard />
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
          {/* Who may edit whose records is a matter of team membership now
              that every seat reads every customer record. */}
          <TeamsCard />
          {/* Beside the member list because it answers the same question one
              level up: that card says which role a person holds, this one says
              what a role may reach. */}
          <ExtensionAccessCard />
        </>
      );
    case "connections":
      return <ConnectionsTab />;
    // Beside `connections` and after it on purpose: that tab says what you are
    // connected to, this one says what those connections did with your mail.
    case "capture-activity":
      return <CaptureActivityTab />;
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
          {/* What the reader themselves, and the workspace, keep out entirely:
              addresses and domains the sink drops before storing anything. */}
          <CaptureExclusionsCard />
          {/* Last, because it is the OUTCOME of the three above rather than a
              fourth rule: which domains ended up refused a company, and whether
              a machine or a person decided it. An operator hunting a company
              that never arrived reads down the page and finishes here. */}
          <BlockedDomainsCard />
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
          {/* What the ladder's statutory floor is holding right now, under the
              ladder that explains why: an erasure that met a Handelsbrief
              restricted it rather than destroying it, and the controller has
              to be able to see that without opening the audit trail. */}
          <RestrictedRecordsCard />
          <PrivacyInboxCard />
          {/* Last, and on the same page: the trail is what proves the three
              surfaces above it were honoured. It gates itself on the admin
              role, which the purpose registry above does not. */}
          <AuditLogCard />
        </>
      );
    case "license":
      return <LicenseCard />;
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
      {/* The rule first, then the mailboxes that live under it: sharing is a
          workspace posture every user works under, not a property of any one
          connection below. */}
      <MailSharingCard />
      <ConnectorsCard />
      <LinkedInImportCard />
      {/* No review queue here: a match a human must judge is a proposal, and
          proposals live in the approvals inbox. This shows what the import
          bought — which accounts the network reaches. */}
      <LinkedInReachCard />
      {/* Last, and only when the installation composed a unit whose credential
          is the member's OWN — that is what a `user`-scoped secret is, and it
          is the same thing every card above it holds. A unit is offered here
          rather than from the rail because enabling one adds something to
          configure, not a destination. */}
      <ExtensionUnitsCard scope="user" />
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
      {/* The other half of the units split: a unit whose secret is
          `workspace`-scoped holds the INSTALLATION's credential, like the four
          cards above it, so it is offered here and not on a member's own
          Connections page. Which page a unit lands on is its manifest's
          decision, never this file's. */}
      <ExtensionUnitsCard scope="workspace" />
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
      <LeadSourcesCard />
      <LeadDisqualifyReasonsCard />
      <LeadHandlingCard />
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
  const licenseRead = useCan("license", "read");
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
    //
    // The composed units are the third card, and they open the entry on their
    // own PRESENCE rather than on a grant: this page is the only place a
    // workspace-scoped unit is offered at all — it has no rail row and the
    // palette never carried one — so a role holding neither read would lose the
    // unit itself, not merely the two cards above it. Presence is the honest
    // predicate because the card asks for no grant; the unit's own screen is
    // what refuses, on the object it declares.
    integrations:
      webhook || overlay || unitsForSecretScope("workspace").length > 0,
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
    // What the license grants and how much of it is used. Admin/ops-only, read
    // included — the narrowest predicate on the rail beside Maintenance's,
    // because a seat meter is the installation's commercial standing and a rep
    // reads their own seat elsewhere (UC-ADMIN-03 F1). A live grant rather than a
    // role name, like every other entry here: an ops principal whose license read
    // was removed by an edited role loses the row, which is the difference
    // between asking the grant and asking who somebody is.
    license: licenseRead,
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
  // it once is the difference between every page spacing correctly and every
  // page having to remember to. Width is owned the same way and is the same for
  // all of them, so there is nothing here to branch on.
  return (
    <div className="wrap">
      <ResumeConnectBanner />
      {/* Unsaved drafts in here are held by the guard above the routed screen
          (App.tsx), not by this screen. A guard installed HERE could only see
          moves between settings entries: it unmounts with the screen, so a draft
          was safe from one tab to the next and still discarded without a word the
          moment the reader clicked Contacts. The cards below claim through
          `useUnsavedGuard` and need to know nothing about where the answer is
          asked. */}
      <div className="settings-stack arrive-stack">{tabContent(active.id)}</div>
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
      {/* Which vendor the installation's text is sent to, first: it is the
          decision the three cards under it report against. */}
      <AiRoutingCard />
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
      {/* The passports FIRST, because minting one is why a reader opens this
          page. The autonomy table used to stand above them: three locked,
          purely informational rows — nothing on it can be changed — sitting
          between the reader and the only control here. It is reference for the
          tiers the tools below are governed by, so it now reads after them. */}
      <PassportCard />
      {/* Directly after the passports, because it is the second half of one
          story: mint a passport, then lend it to a client that connects. */}
      <ConnectedAgentsCard />
      <AgentToolsCard />
      <AutonomyCard />
    </>
  );
}

// Who you are, drawn the way the product draws an identity everywhere else: the
// record page's own header block (composed.css `.record-head`) — a mark, the
// name, the standing badges on the name's line, and the meta that qualifies it
// underneath. Three items loose in a wide box said nothing about which of them
// was the subject; the mark and the type scale do, before a word is read.
function IdentityCard() {
  const t = useT();
  const query = useMe();
  const logout = useLogout();
  return (
    <Panel
      title={t("settings.identity")}
      actions={
        <Button
          small
          disabled={logout.isPending}
          onClick={() => logout.mutate()}
        >
          {t("auth.signOut")}
        </Button>
      }
    >
      <PanelBody>
        <QueryGate query={query}>
          {(me) => (
            <div className="settings-identity">
              {/* Both halves are required on the wire, so the `?? ""` is not a
                  default — it is the promise that a server answering with
                  neither costs the reader an unnamed chip rather than the whole
                  page: this block renders inside the app shell, and a throw here
                  takes the navigation down with it. */}
              {/* The address is the tint's key, so this reader keeps the same
                  colour here as in the rail's account block and as their turns
                  in the onboarding transcript — and keeps it after they change
                  their display name. */}
              <Avatar
                identity={me.user.email || undefined}
                name={me.user.display_name || me.user.email || ""}
              />
              <div className="settings-identity-id">
                <div className="settings-identity-name">
                  <strong>{me.user.display_name || me.user.email}</strong>
                  {me.roles.map((role) => (
                    <RoleBadge key={role} roleKey={role} />
                  ))}
                </div>
                {/* The two lines that qualify the name: the address the session
                    is bound to, and the workspace it is bound to it IN. */}
                <span className="t-small">{me.user.email}</span>
                <span className="t-small">{me.workspace_name}</span>
              </div>
            </div>
          )}
        </QueryGate>
      </PanelBody>
    </Panel>
  );
}

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
  const toast = useToast();
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
      // The save had no visible answer at all: the button went disabled because
      // the draft now matched the server, and that was the whole signal — a
      // control losing its affordance, which reads as the form having given up
      // rather than as the write having landed. Only failure spoke.
      toast.show(t("settings.saved"));
    },
  });

  // The saved value until the member types; theirs from then on. Reading state
  // straight from the query would discard every keystroke the moment a refetch
  // landed underneath them.
  const shown = body ?? signature.data?.body ?? "";
  // The same comparison the Save button already made, now also the claim that
  // stops a sidebar click throwing the draft away.
  useUnsavedGuard(shown !== (signature.data?.body ?? ""));

  return (
    <Panel
      title={t("settings.signature")}
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
      <PanelBody className="form-stack">
        <p className="t-small settings-panel-sub">
          {t("settings.signatureSub")}
        </p>
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
        <ToastRegion toast={toast} />
        {save.isError && (
          <Callout tone="danger" live="alert">
            {problemMessageOf(save.error, t)}
          </Callout>
        )}
      </PanelBody>
    </Panel>
  );
}

/**
 * This person's own preferences, minus the one that is chosen elsewhere.
 *
 * Appearance is picked from the ACCOUNT MENU: it is the setting a reader
 * changes most often and from wherever they happen to be standing, so it lives
 * where they already are rather than three screens away. This card keeps the
 * preferences that are not appearance, and it sits beside the identity card
 * because that is what they belong to. Each control drives state the rest of
 * the app already reads, so nothing here is a second source of truth: the
 * language lasts as long as the session does because the locale context is
 * where it lives.
 */
function PreferencesCard() {
  const t = useT();
  const { locale, setLocale } = useLocale();
  return (
    <Panel title={t("settings.preferences")}>
      <PanelBody>
        <p className="t-small settings-panel-sub">
          {t("settings.preferencesSub")}
        </p>
        <div className="form-stack">
          <Field label={t("locale.switchLabel")} className="settings-intrinsic">
            {(control) => (
              <Select
                {...control}
                value={locale}
                // `Select` reports a string. The options are built from LOCALES,
                // so narrowing the answer through that same list is what makes
                // it a Locale — no assertion, and nothing acted on that the
                // control was never offering.
                onChange={(next) => {
                  const picked = LOCALES.find((option) => option === next);
                  if (picked) {
                    setLocale(picked);
                  }
                }}
                // Language names are proper nouns and deliberately not
                // translated, so every option here is in a different language
                // from the page around it — WCAG 2.2 AA 3.1.2, the same reason
                // the login footer's switcher carries `lang` on each name. Our
                // locale codes are BCP 47 language subtags, so the code IS the
                // value `lang` wants.
                options={LOCALES.map((option) => ({
                  value: option,
                  label: t(localeNameKey(option)),
                  lang: option,
                }))}
              />
            )}
          </Field>
        </div>
      </PanelBody>
    </Panel>
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
  const queryClient = useQueryClient();
  const [label, setLabel] = useState("");
  const [scopes, setScopes] = useState<Set<string>>(new Set(["read", "draft"]));
  const [confirmId, setConfirmId] = useState<string | null>(null);
  const [minting, setMinting] = useState(false);
  const revokingRow = useRef<HTMLElement | null>(null);
  // Where the minted token lands. It is a live region that is ALWAYS mounted
  // for the drawer's whole life rather than one that appears with the token in
  // it: a region inserted at the same moment as its content is not reliably
  // announced, and this token is shown exactly once in its life.
  const tokenRegion = useRef<HTMLDivElement | null>(null);
  const mintTitleId = useId();
  const mintScopeHintId = useId();

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
      const { data, error, response } = await api.POST("/passports", {
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
      // An expired session must not read as a mint that did nothing. The `me`
      // probe is cached for five minutes, so without this the screen keeps
      // believing it is signed in and the button simply fails in silence —
      // which is how the OAuth consent screen's empty-passport guide became an
      // inescapable loop: it sends the human here to mint, the mint 401s
      // without saying so, and returning finds no passport and shows the same
      // guide again.
      //
      // Keyed on the STATUS rather than on `error` being truthy: a non-2xx
      // with no body leaves `error` undefined in this client (compose.tsx
      // documents the same shape), and that response would otherwise pass as
      // a success and then render an undefined token.
      if (response.status === 401) {
        // useLogout's order, for its reason: drop every other cached answer
        // BEFORE resetting the probe. AuthGate restores this route after the
        // next sign-in, and whoever signs in then must not be shown the
        // previous session's passport list out of cache.
        queryClient.removeQueries({
          predicate: (query) => query.queryKey[0] !== "me",
        });
        await queryClient.resetQueries({ queryKey: ["me"] });
      }
      if (error) {
        throwProblem(error);
      }
      if (!response.ok) {
        // A refusal that carried no problem document still has to refuse.
        throwProblem({
          type: "about:blank",
          title: response.statusText || "Request failed",
          status: response.status,
        });
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

  // The token arrives asynchronously, is shown once, and is never re-disclosed:
  // announcing it is not enough on its own, because a reader whose focus is
  // still on the Mint button has to hunt for what they just made. Focus moves
  // to the region holding it, which is also what makes the copy gesture reach
  // it from the keyboard.
  const minted = mint.isSuccess;
  useEffect(() => {
    if (minted) {
      tokenRegion.current?.focus();
    }
  }, [minted]);

  // Closing resets the whole attempt, so re-opening starts clean rather than
  // showing the previous mint's token or its refusal. The scope defaults come
  // back with it — the drawer is not a form somebody left half-filled, it is a
  // new passport each time.
  const closeMint = useCallback(() => {
    // Refused while the request is outstanding, and this is about losing a
    // credential rather than about tidiness. `mint.reset()` detaches the
    // observer; it does not cancel the POST. A drawer closed mid-flight
    // therefore still creates a passport on the server, and its token — shown
    // exactly once, never re-served — goes with the drawer. There is no way
    // back: the list carries metadata only.
    if (mint.isPending) {
      return;
    }
    setMinting(false);
    setLabel("");
    setScopes(new Set(["read", "draft"]));
    mint.reset();
  }, [mint]);

  return (
    // The card LISTS what exists; minting is a drawer. It used to be one flex
    // row holding a label, a name field, five scope ticks and the submit — eight
    // controls on one line with a single 8px gap between all of them, so nothing
    // said where the field ended and the choices began. A form that wide is not
    // a row in a settings card; it is a form, and the product already has the
    // surface for one.
    <Panel
      title={t("settings.passports")}
      titleAction={
        <Button small variant="primary" onClick={() => setMinting(true)}>
          {t("settings.mint")}
        </Button>
      }
      footer={t("settings.passportsLendHint")}
    >
      <PanelBody>
        <p className="t-small settings-panel-sub">
          {t("settings.passportsSub")}
        </p>
      </PanelBody>
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
        {(page) =>
          page.data
            .filter((passport) => passport.connection == null)
            .map((passport) => (
              <PassportRow
                key={passport.id}
                passport={passport}
                locale={locale}
                onRevoke={(row) => {
                  revokingRow.current = row;
                  setConfirmId(passport.id);
                }}
              />
            ))
        }
      </QueryGate>
      <Modal
        open={minting}
        onClose={closeMint}
        labelledBy={mintTitleId}
        placement="right"
      >
        <h2 className="t-h2" id={mintTitleId}>
          {t("settings.mint")}
        </h2>
        {/* The token region is mounted for the whole life of the drawer rather
            than appearing with the token in it: a live region inserted at the
            same moment as its content is not reliably announced, and this token
            is shown exactly once. */}
        <div
          className="passport-token"
          ref={tokenRegion}
          tabIndex={-1}
          role="status"
        >
          {mint.isSuccess && (
            <PanelPlate>
              <p className="t-label">{t("settings.tokenOnce")}</p>
              <p className="t-mono passport-token-value">{mint.data.token}</p>
            </PanelPlate>
          )}
        </div>
        {mint.isSuccess ? (
          // The drawer does NOT close itself on success. Closing would take the
          // one and only sight of the credential with it, and a reader who was
          // still reading has no way back — the list carries metadata and the
          // server will not re-disclose a token.
          <div className="form-actions">
            <Button variant="primary" onClick={closeMint}>
              {t("settings.mintDone")}
            </Button>
          </div>
        ) : (
          <form
            className="form-stack"
            onSubmit={(event) => {
              event.preventDefault();
              if (scopes.size > 0 && !mint.isPending) mint.mutate();
            }}
          >
            <Field label={t("settings.passportLabel")}>
              {(control) => (
                <TextInput
                  {...control}
                  value={label}
                  onChange={(event) => setLabel(event.target.value)}
                />
              )}
            </Field>
            {/* A fieldset with a legend, which is what five checkboxes that
                belong together ARE. Loose siblings beside a text input said
                nothing about what they were choices FOR, and the accessible
                group had no name at all. `.field-multiselect` is the house
                spelling — `create.tsx` has used it for exactly this since it
                was written. */}
            <fieldset
              className="field-multiselect"
              aria-describedby={mintScopeHintId}
            >
              <legend className="t-label">
                {t("settings.passportScopes")}
              </legend>
              <p id={mintScopeHintId} className="t-caption">
                {t("settings.passportScopesHint")}
              </p>
              {PASSPORT_SCOPES.map((scope) => (
                <Checkbox
                  key={scope}
                  className="t-label"
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
            </fieldset>
            {/* Beside the button that produced it, in the danger tone. It used
                to render two blocks below, past the token region, as a caption
                with no live role — so a refused mint announced nothing and sat
                where nothing had been pressed. */}
            {mint.isError && (
              <Callout tone="danger" live="alert">
                {problemMessageOf(mint.error, t)}
              </Callout>
            )}
            <div className="form-actions">
              <Button disabled={mint.isPending} onClick={closeMint}>
                {t("settings.mintCancel")}
              </Button>
              <Button
                type="submit"
                variant="primary"
                disabled={scopes.size === 0 || mint.isPending}
                // A passport with no scope is a credential that can do nothing,
                // so the button says why it is refused rather than sitting pale
                // with nothing to offer.
                reason={
                  scopes.size === 0
                    ? t("settings.passportScopesRequired")
                    : undefined
                }
              >
                {mint.isPending ? t("settings.minting") : t("settings.mint")}
              </Button>
            </div>
          </form>
        )}
      </Modal>
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
    </Panel>
  );
}

type PassportSummary = components["schemas"]["PassportSummary"];

// One minted passport, as a full-bleed row under the mint form. The revoked
// state is STRUCK, never dimmed: dimming drops the row under the AA contrast
// floor (B-EP09.21), and it is the revoked row a reader most needs to read.
function PassportRow({
  passport,
  locale,
  onRevoke,
}: Readonly<{
  passport: PassportSummary;
  locale: Locale;
  onRevoke: (row: HTMLElement | null) => void;
}>) {
  const t = useT();
  const revoked = passport.revoked_at != null;
  return (
    <PanelRow>
      {/* Reachable by focus() without joining anybody's Tab order: the revoke
          confirm hands focus back here, because the button it was opened from
          is gone by then. */}
      <div
        data-passport={passport.id}
        tabIndex={-1}
        className={
          revoked ? "passport-facts passport-struck" : "passport-facts"
        }
      >
        <strong>{passport.label}</strong>
        {/* The credential exists but is withheld by design (shown once at
            mint) — masked reads as "withheld", not absent. */}
        <span className="t-label">{t("settings.token")}</span>
        <FieldGuard mode="masked" />
        <ScopeChips scopes={passport.scopes} />
        <span className="t-small">
          {t("settings.created", {
            date: formatDate(passport.created_at, locale, "Europe/Berlin"),
          })}
        </span>
        {/* A credential's lifetime is a personal deadline, so it reads on the
            viewer's own calendar — the same zone-by-purpose split the consent
            screen makes. created_at above stays the fixed record zone. */}
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
        {revoked && <Badge tone="danger">{t("settings.revoked")}</Badge>}
        {!revoked && (
          <Button
            small
            variant="danger"
            // The row is remembered from the CLICK rather than from
            // `confirmId`: the focus resolver runs as the dialog closes, by
            // which time that state is already back to null and there is
            // nothing left to look the row up by.
            onClick={(event) =>
              onRevoke(
                event.currentTarget.closest<HTMLElement>("[data-passport]"),
              )
            }
          >
            {t("settings.revoke")}
          </Button>
        )}
      </div>
    </PanelRow>
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
    <Panel title={t("tools.title")}>
      <PanelBody>
        <p className="t-small settings-panel-sub">{t("tools.sub")}</p>
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
      </PanelBody>
      <QueryGate query={tools} empty={(data) => data.data.length === 0}>
        {(data) =>
          data.data.map((tool) => (
            <ToolRow
              key={tool.name}
              tool={tool}
              reachable={
                !scopeId ||
                tool.required_scope == null ||
                grantedScopes.has(tool.required_scope)
              }
            />
          ))
        }
      </QueryGate>
    </Panel>
  );
}

type AgentTool = components["schemas"]["AgentTool"];

// One governed tool, as a full-bleed row. Struck, not dimmed: dimming the row
// to 0.4 took the whole row under the AA contrast floor (B-EP09.21) — including
// the caption that is supposed to be the text equivalent of the dimming, so the
// one part a reader needs most became the hardest to read. The strikethrough
// wraps the FACTS and nothing else: the badges state the tool's governance,
// which is true either way, and the caption is the explanation.
function ToolRow({
  tool,
  reachable,
}: Readonly<{ tool: AgentTool; reachable: boolean }>) {
  const t = useT();
  const struck = reachable ? undefined : "tool-out-of-scope";
  return (
    <PanelRow>
      <div data-tool={tool.name} className="tool-row-body">
        <div className="tool-row-head">
          <AutonomyDot tier={dotTier(tool.tier)} />
          <span
            className={["t-mono", "tool-name", struck]
              .filter(Boolean)
              .join(" ")}
          >
            {tool.name}
          </span>
          {tool.title && (
            <span className={["t-caption", struck].filter(Boolean).join(" ")}>
              {tool.title}
            </span>
          )}
          {tool.required_scope && <Badge>{tool.required_scope}</Badge>}
          {tool.egress && <Badge tone="warn">{t("tools.egress")}</Badge>}
          {!reachable && (
            <span className="t-caption">{t("tools.unreachable")}</span>
          )}
        </div>
        {/* The text an agent actually selects on. This console promises the
            surface an MCP client sees, and the name alone was never that. */}
        {tool.description && (
          <p className="t-caption tool-description">{tool.description}</p>
        )}
      </div>
    </PanelRow>
  );
}

// The danger-zone reset action: wipes the installation back to its first-boot
// state. Double-gated client-side — the admin role AND the server-driven
// `data_reset_available` flag on /me (never VITE_UI_PREVIEW_RESET, which is the
// unrelated password-reset link) — so the affordance is invisible unless the
// deployment armed the capability; the server gates the endpoint on that same
// value and 404s it otherwise, regardless of what this card renders.
//
// This is admin-ONLY, and narrower than the Maintenance entry
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

  if (!isAdmin || !me.data?.data_reset_available) {
    return null;
  }

  return (
    <Panel
      title={t("settings.dangerZone")}
      // The one card that announces its own danger: the red border is what
      // separates a destructive surface from the ordinary settings around it.
      className="settings-danger"
      actions={
        <Button small variant="danger" onClick={() => setOpen(true)}>
          {t("settings.resetDataButton")}
        </Button>
      }
    >
      <PanelBody>
        <p className="t-small settings-panel-sub">
          {t("settings.dangerZoneSub")}
        </p>
        <p className="t-caption">{t("settings.resetDataDesc")}</p>
        {summary && (
          <p className="t-caption settings-danger-result" role="status">
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
          <p className="t-caption settings-danger-warning" role="alert">
            {t("settings.resetDataDrainWarning")}
          </p>
        )}
      </PanelBody>
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
        // The typed confirmation gates whether the reset may START; the input
        // is still editable while it runs, and a reader who clears it mid-write
        // would otherwise re-arm the gate on a control that is already going.
        confirmDisabled={!reset.isPending && typed.trim() === ""}
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
    </Panel>
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
function StageRemove({
  stage,
  returnFocusTo,
}: Readonly<{ stage: Stage; returnFocusTo: () => HTMLElement | null }>) {
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
        // The stage list, not the trigger: a successful removal unmounts
        // the row this button lives in, so there is nothing to hand focus
        // back to (design-system/confirmmodal).
        returnFocusTo={returnFocusTo}
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
  returnFocusTo,
}: Readonly<{
  stage: Stage;
  canEdit: boolean;
  t: ReturnType<typeof useT>;
  returnFocusTo: () => HTMLElement | null;
}>) {
  return (
    // The four tracks (name, semantic badge, probability, edit) live in
    // settings.css, where they can have a phone breakpoint. Inline, the three
    // fixed tracks plus the Edit button left about 60px for a stage name that
    // could not wrap, and it painted straight over the badge beside it.
    <li className="stage-row">
      <span className="stage-name">{stage.name}</span>
      <Badge tone={stageSemanticTone(stage.semantic)}>
        {stageSemanticLabel(stage.semantic, t)}
      </Badge>
      <span className="t-mono t-small">{stage.win_probability}%</span>
      {/* Each control carries its own verb — editing a stage is
          pipeline:update, removing one is pipeline:delete — so a role
          holding one without the other still sees the one it may use. */}
      <span
        style={{
          display: "flex",
          gap: "var(--space-2)",
          alignItems: "center",
        }}
      >
        {canEdit && (
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
        <StageRemove stage={stage} returnFocusTo={returnFocusTo} />
      </span>
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
  const stageList = useRef<HTMLUListElement>(null);
  const stages = [...(pipeline.stages ?? [])].sort(
    (a, b) => a.position - b.position,
  );
  return (
    <PanelRow className="pipeline-row">
      {/* A real heading through the primitive, not a span — nor an `h3` wearing
          `t-h2`, which drew the inner heading at 18px under a card title at 15px
          and told the eye the pipeline outranked the card holding it. The
          pipeline is the subject of this row and the stage list under it, so a
          reader navigating by heading has to be able to land on it; `level={3}`
          is what makes the type step DOWN with the outline instead of past it. */}
      <SectionHeader
        title={pipeline.name}
        level={3}
        actions={
          <>
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
          </>
        }
      />
      {/* tabIndex -1 so a removal can hand focus to the list it changed:
          the row's own Remove button is gone by then, and focus dropped to
          <body> leaves a screen-reader user at the top of the document. */}
      <ul ref={stageList} tabIndex={-1} className="stage-rows">
        {stages.map((stage) => (
          <StageRow
            key={stage.id}
            stage={stage}
            canEdit={canEdit}
            t={t}
            returnFocusTo={() => stageList.current}
          />
        ))}
      </ul>
    </PanelRow>
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
    <Panel
      title={t("settings.pipelines")}
      // The verb that CHANGES this panel, in the band under its body — not one
      // more row among the pipelines it would add to.
      actions={
        canCreate ? (
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
        ) : undefined
      }
    >
      <PanelBody>
        <p className="t-small settings-panel-sub">
          {t("settings.pipelinesSub")}
        </p>
        {/* Said once, at the top, rather than annotating each absent control —
            the rule in design-system/README.md. A reader holding one of the two
            verbs can see for themselves which controls they got. */}
        {!canCreate && !canEdit && (
          <p className="t-caption">{t("settings.pipelinesReadOnly")}</p>
        )}
      </PanelBody>
      <QueryGate query={query} empty={(pipelines) => pipelines.length === 0}>
        {(pipelines) =>
          pipelines.map((pipeline) => (
            <PipelineRow
              key={pipeline.id}
              pipeline={pipeline}
              canEdit={canEdit}
              t={t}
            />
          ))
        }
      </QueryGate>
    </Panel>
  );
}

// The two-tier table (03b): informational, and the advance-stage row is
// locked 🟡 — there is no toggle that could soften it (AC-settings). It reads
// AFTER the passports and the tools it governs, because nothing on it can be
// acted on: it is the reference for the tiers those rows are marked with.
function AutonomyCard() {
  const t = useT();
  return (
    <Panel title={t("settings.autonomy")}>
      <PanelBody>
        <p className="t-small">{t("settings.autonomySub")}</p>
      </PanelBody>
      <PanelRow className="autonomy-row">
        <AutonomyDot tier="auto" /> <strong>{t("settings.tierRead")}</strong>
      </PanelRow>
      <PanelRow className="autonomy-row">
        <AutonomyDot tier="confirm" /> <strong>{t("settings.tierSend")}</strong>
      </PanelRow>
      <PanelRow className="autonomy-row">
        <AutonomyDot tier="confirm" />{" "}
        <strong>{t("settings.tierAdvance")}</strong>{" "}
        <Badge tone="warn">{t("settings.locked")}</Badge>
      </PanelRow>
    </Panel>
  );
}

type AuditLogEntry = components["schemas"]["AuditLogEntry"];

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

// The six filters, declared once, so the accessible wiring is identical across
// the row. Each is a `Field` — a real <label> with `htmlFor`, so clicking the
// words focuses the control. It used to be a `t-label` span pointed at by
// aria-labelledby, on the reasoning that a real label would wrap every field
// onto its own line; that is not what happens. `.field` IS a flex column, and
// the grid around it decides the layout either way, so the span bought nothing
// and cost the click target.
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

/**
 * The filters as a QUESTION, settled — which is a different thing from the
 * filters as they are being typed.
 *
 * The row updates on every keystroke, as it must; what waits is the query. The
 * filter object is the query key, so before this every character was its own
 * `GET /audit-log`: typing `agent:runner` asked the server twelve questions,
 * eleven of them about prefixes nobody wanted an answer to, and the answers
 * could land out of order. The shared list surface settles its own search on
 * this same constant, so a filter costs the same anywhere in the product.
 */
function useSettledAuditLogFilters(typed: AuditLogFilters): AuditLogFilters {
  const [settled, setSettled] = useState(typed);
  useEffect(() => {
    const timer = setTimeout(() => setSettled(typed), SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [typed]);
  return settled;
}

function AuditLogFilterFields({
  filters,
  onChange,
}: Readonly<{
  filters: AuditLogFilters;
  onChange: (next: AuditLogFilters) => void;
}>) {
  const t = useT();
  return (
    // Six narrow filters read as a grid of labelled cells rather than one long
    // row: in a row each control took the width of the card and the six of them
    // stacked into a page-tall form, which is the shape of something to fill in
    // rather than something to narrow a list with.
    <div className="audit-filters">
      {AUDIT_LOG_FILTER_FIELDS.map((field) => (
        <Field key={field.key} label={t(field.labelKey)}>
          {(control) => (
            <TextInput
              {...control}
              type={field.date ? "date" : undefined}
              value={filters[field.key]}
              onChange={(event) =>
                onChange({ ...filters, [field.key]: event.target.value })
              }
            />
          )}
        </Field>
      ))}
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
    <PanelRow className="audit-row">
      <div className="audit-row-head">
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
        <div className="audit-row-diff">
          {keys.map((key) => (
            <div key={key} className="audit-diff-line">
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
    </PanelRow>
  );
}

// The result half of the audit view: the answer to whatever the filter row
// currently asks. Keyset "load more" via the page cursor, and a filter change
// is a new question — the filters ARE the query key, so changing one restarts
// the cursor chain instead of appending to a stale one.

function AuditLogEntries({
  filters,
  meUserId,
}: Readonly<{ filters: AuditLogFilters; meUserId?: string }>): ReactNode {
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
    initialPageParam: FIRST_PAGE,
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

  // Honest state matrix (§3a): withheld, loading, error, empty, then the rows —
  // kept as sequential branches rather than a nested ternary in the JSX below.
  // Everything but the rows is body content and sits in a `PanelBody`; the rows
  // themselves are full-bleed and are the panel's own direct children.
  if (!isAdmin) {
    // Withheld rather than absent, and the card keeps its place: an absent trail
    // on a page that opens for ops would read as "nothing has happened here",
    // which is a different claim from "this is not yours to read". The same
    // choice the subject-request queue above it makes, for the same reason.
    return (
      <PanelBody>
        <EmptyState>
          <p className="t-small">{t("settings.auditAdminOnly")}</p>
        </EmptyState>
      </PanelBody>
    );
  }
  if (query.isPending) {
    return (
      <PanelBody>
        <div className="audit-loading">
          <Skeleton width="60%" />
          <Skeleton width="90%" />
        </div>
      </PanelBody>
    );
  }
  if (query.isError) {
    return (
      <PanelBody>
        <EmptyState>
          <p>{t("common.error")}</p>
          <p className="t-mono audit-error-cause">
            {problemMessageOf(query.error, t)}
          </p>
          <Button small onClick={() => query.refetch()}>
            {t("common.retry")}
          </Button>
        </EmptyState>
      </PanelBody>
    );
  }
  if (entries.length === 0) {
    return (
      <PanelBody>
        <EmptyState>{t("common.empty")}</EmptyState>
      </PanelBody>
    );
  }
  return (
    <>
      {entries.map((entry) => (
        <AuditLogRow key={entry.id} entry={entry} meUserId={meUserId} />
      ))}
      <PanelBody>
        <LoadMoreButton query={query} />
      </PanelBody>
    </>
  );
}

// AC-settings-16: the attributable audit view — live filters over actor /
// entity_type / entity_id / action / from / to, keyset "load more" via the
// page cursor. ONE card, because every other filtered list in this product puts
// its dials inside the list's own surface: a card of its own titled "Filters"
// made six inputs a subject in the page outline, level with the trail they
// narrow, and left a reader scanning two cards to answer one question.
// Each row expands into the before/after diff plus the agent attribution trail
// (passport, on-behalf-of human, authorization rule, grounding evidence) —
// collapsed by default so the flat scan stays fast.
export function AuditLogCard() {
  const t = useT();
  // The current user's id is what lets ActorTag read "You" rather than naming
  // the viewer back to themselves. It is the BARE user id; the wire spells a
  // human actor "human:<uuid>", and ActorTag owns that difference.
  const meUserId = useMe().data?.user?.id;
  const isAdmin = useHoldsAdminRole();
  const [filters, setFilters] = useState<AuditLogFilters>(UNFILTERED_AUDIT_LOG);
  // The row reads what is being typed; the entries read what has settled.
  const asked = useSettledAuditLogFilters(filters);
  return (
    <Panel title={t("settings.auditEntries")}>
      <PanelBody>
        <p className="t-small settings-panel-sub">{t("settings.auditSub")}</p>
        {/* The filter row is absent, not withheld, for a reader who may not read
            the trail: six inputs that narrow a list they cannot see are a
            control with nothing behind it. The ROWS below stay and say why —
            absence there would claim nothing had happened.
            Its name is an eyebrow rather than a card title: it labels the dials
            inside the log's surface, and the log's own name above is what says
            which trail this is. */}
        {isAdmin && (
          <>
            <Eyebrow as="h3">{t("settings.auditFilters")}</Eyebrow>
            <AuditLogFilterFields filters={filters} onChange={setFilters} />
          </>
        )}
      </PanelBody>
      <AuditLogEntries filters={asked} meUserId={meUserId} />
    </Panel>
  );
}

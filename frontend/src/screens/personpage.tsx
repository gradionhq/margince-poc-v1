import { useQuery } from "@tanstack/react-query";
import {
  CalendarPlus,
  CheckSquare,
  Link as LinkIcon,
  Mail,
  MapPin,
  MoreHorizontal,
  Phone,
  Search,
} from "lucide-react";
import type { ReactNode } from "react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
import { Button, SegmentedControl } from "../design-system/atoms";
import { RecordView } from "../design-system/composed";
import { Panel, PanelBody } from "../design-system/panel";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { throwProblem } from "./common";
import {
  hasCommercial,
  hasCommitments,
  hasMatters,
  PersonBriefCard,
  PersonCommercialCard,
  PersonCommitmentsCard,
  PersonMattersCard,
} from "./personcards";
import {
  PersonComposer,
  PersonMeetingBrief,
  PersonResearchDrawer,
} from "./persondrawers";
import { PersonMemory } from "./personmemory";
import { PersonRail } from "./personrail";
import { PersonStrip } from "./personstrip";
import { PersonToday } from "./persontoday";
import "./person360.css";

// The person record page V2 (ADR-0096, concept person-record-page-v2).
//
// It opens on a REASON, not a record: the moment the server selected leads,
// the facts that change how you read it sit above the fold, and the database
// view of the person is a tab away rather than the first thing on screen.

type Person360 = components["schemas"]["Person360"];
type PersonMomentAction = components["schemas"]["PersonMomentAction"];

// The seven tabs, in the concept's order (§5.4). URL-addressable, so a tab
// survives a reload and can be linked to.
export const PERSON_TABS = [
  "overview",
  "activity",
  "deals",
  "meetings",
  "research",
  "files",
  "history",
] as const;
export type PersonTab = (typeof PERSON_TABS)[number];

const TAB_LABEL_KEYS: Readonly<Record<PersonTab, MessageKey>> = {
  overview: "person.tab.overview",
  activity: "person.tab.activity",
  deals: "person.tab.deals",
  meetings: "person.tab.meetings",
  research: "person.tab.research",
  files: "person.tab.files",
  history: "person.tab.history",
};

// The placeholder sentence names the tab mid-sentence, where English wants the
// label lowercased and German does not. Lowercasing at the call site would
// mangle every German noun, so each locale carries its own mid-sentence form.
const TAB_TOPIC_KEYS: Readonly<Record<PersonTab, MessageKey>> = {
  overview: "person.topic.overview",
  activity: "person.topic.activity",
  deals: "person.topic.deals",
  meetings: "person.topic.meetings",
  research: "person.topic.research",
  files: "person.topic.files",
  history: "person.topic.history",
};

export function isPersonTab(value: string | undefined): value is PersonTab {
  return PERSON_TABS.includes((value ?? "") as PersonTab);
}

// The intent phrases a moment action can ask the composer to open with. The
// server names the reason in its own vocabulary ("agenda", "follow_up"); the
// composer hands what it holds to a language model and shows it to a rep, so
// both need a sentence rather than an enum value.
const COMPOSER_INTENT_KEYS: Readonly<Record<string, MessageKey>> = {
  agenda: "person.composer.intentAgenda",
  reply: "person.composer.intentReply",
  deliver_commitment: "person.composer.intentCommitment",
  follow_up: "person.composer.intentFollowUp",
};

// What the composer opens with, from an action's prefill.
//
// An intent this client does not know opens an EMPTY composer rather than
// passing the raw token through: "deliver_commitment" in the intent field is
// worse than nothing, because a rep reads it as something the product meant to
// say, and the model reads it as an instruction nobody wrote.
function composerIntentOf(
  prefill: Readonly<Record<string, string>> | undefined,
  t: ReturnType<typeof useT>,
): string {
  const key = COMPOSER_INTENT_KEYS[prefill?.intent ?? ""];
  return key ? t(key) : "";
}

export function PersonPageV2({
  id,
  tab,
}: Readonly<{ id: string; tab: PersonTab }>) {
  const t = useT();
  const view = useQuery({
    queryKey: ["person360", id],
    queryFn: async () => {
      const { data, error } = await api.GET("/people/{id}/360", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  const brief = useQuery({
    queryKey: ["personBrief", id],
    queryFn: async () => {
      const { data, error } = await api.GET("/people/{id}/brief", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  const guard = useQuery({
    queryKey: ["personConsentGuard", id],
    queryFn: async () => {
      const { data, error } = await api.GET("/people/{id}/consent/guard", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  // Which drawer is open, if any. One at a time: two surfaces over the same
  // record would each claim to be the thing the reader is doing.
  const [drawer, setDrawer] = useState<Drawer>(null);
  // What the action that opened the composer wanted written. A rung knows WHY
  // it fired and says so in its prefill; before this the client dropped that
  // on the floor and opened the same empty composer as the generic button, so
  // "Draft a follow-up" and "Write an email" did exactly the same thing.
  const [composerIntent, setComposerIntent] = useState("");

  if (view.isLoading) {
    return <div className="wrap">{t("person.page.loading")}</div>;
  }
  if (view.isError || !view.data) {
    return <div className="wrap">{t("person.page.notOpened")}</div>;
  }

  const person = view.data.person;
  const firstName = person.full_name.split(" ")[0];
  // Consent is decided per PURPOSE, so the guard carries one email entry per
  // purpose. The hero button asks a wider question than any single entry —
  // "is there anything we may write to them about" — and reading only the
  // first entry answered it with whichever purpose sorted first, disabling the
  // button on a contact the product would happily let you mail transactionally.
  // Which purpose applies is then the composer's own question.
  const emailAllowed = (guard.data?.entries ?? []).some(
    (entry) => entry.channel === "email" && entry.verdict === "allowed",
  );

  // The action loop. Every surface the contract can name routes here.
  //
  // A destination this client cannot route opens NOTHING rather than guessing:
  // the typed descriptor exists so a button whose path does not exist is never
  // rendered, and silently doing something else would be worse than the 404 it
  // was meant to prevent.
  const runAction = (action: PersonMomentAction) => {
    const destination = action.destination;
    if (!destination) {
      // An action with no destination is its own destination — the composer is
      // the sensible home for the drafting kinds.
      if (action.kind === "draft_reply") {
        setComposerIntent("");
        setDrawer("composer");
      }
      return;
    }
    switch (destination.surface) {
      case "composer":
        setComposerIntent(composerIntentOf(destination.prefill, t));
        setDrawer("composer");
        return;
      case "research":
        setDrawer("research");
        return;
      case "meeting_brief":
        setDrawer("meeting");
        return;
      case "record":
        if (destination.entity_id) {
          navigate({ screen: "deals", id: destination.entity_id });
        }
        return;
      default:
        // `task` has no surface on this page yet. Doing nothing is the honest
        // outcome; inventing a navigation would take the reader somewhere they
        // did not ask to go.
        return;
    }
  };

  return (
    <div className="wrap">
      <RecordView
        name={person.full_name}
        avatarSrc={null}
        subtitle={<PersonSubtitle view={view.data} />}
        pulse={<PersonIdentityLine view={view.data} />}
        actions={
          <PersonActions
            guardAllows={emailAllowed}
            personId={id}
            onEmail={() => setDrawer("composer")}
            onResearch={() => setDrawer("research")}
          />
        }
        actionsInline
        zone="Europe/Berlin"
        // The readings ride the band, above the columns and across the full
        // width: they describe the RELATIONSHIP, not one view of it, and a
        // strip that vanished on the Deals tab would move the tab bar and
        // re-flow the page under the reader.
        band={
          <PersonStrip
            view={view.data}
            consentVerdict={emailAllowed ? "allowed" : undefined}
          />
        }
        railLabel={t("person.page.asideLabel")}
        rail={
          <PersonRail
            view={view.data}
            guard={guard.data}
            firstName={firstName}
            onExplain={() => navigate({ screen: "contacts", id })}
          />
        }
      >
        {/* The bar leads the column it governs, so it sits in the main column
            rather than in the band above it: what it chooses is what appears
            beneath it, not the readings over it. */}
        <div className="pe-tabs">
          <SegmentedControl
            options={PERSON_TABS}
            value={tab}
            onChange={(next) => navigate({ screen: "contacts", id, id2: next })}
            labels={{
              overview: t(TAB_LABEL_KEYS.overview),
              activity: t(TAB_LABEL_KEYS.activity),
              deals: t(TAB_LABEL_KEYS.deals),
              meetings: t(TAB_LABEL_KEYS.meetings),
              research: t(TAB_LABEL_KEYS.research),
              files: t(TAB_LABEL_KEYS.files),
              history: t(TAB_LABEL_KEYS.history),
            }}
          />
        </div>

        {tab === "overview" && (
          // One vertical stack of full-width panels, not a grid of half-width
          // cards: every panel here is read top to bottom once, and prose in a
          // half-column column gets a measure too narrow to read as prose.
          <div className="pe-overview-stack">
            {view.data.moment && (
              <PersonToday
                moment={view.data.moment}
                firstName={firstName}
                onAction={runAction}
              />
            )}
            <PersonBriefCard
              brief={brief.data}
              loading={brief.isLoading}
              view={view.data}
            />
            {hasCommercial(view.data) && (
              <PersonCommercialCard view={view.data} />
            )}
            {hasCommitments(view.data) && (
              <PersonCommitmentsCard view={view.data} firstName={firstName} />
            )}
            {hasMatters(view.data) && (
              <PersonMattersCard view={view.data} firstName={firstName} />
            )}
            <PersonMemory view={view.data} />
          </div>
        )}

        {tab !== "overview" && (
          // The other six tabs are addressable and named, and each says what
          // it will hold. An empty panel that looked like a rendering failure
          // would be worse than one that says what is coming.
          <Panel title={t(TAB_LABEL_KEYS[tab])}>
            <PanelBody>
              <p className="pe-prose">
                {t("person.page.tabPlaceholder", {
                  topic: t(TAB_TOPIC_KEYS[tab]),
                })}
              </p>
            </PanelBody>
          </Panel>
        )}
        <PersonComposer
          personId={id}
          view={view.data}
          guard={guard.data}
          open={drawer === "composer"}
          intent={composerIntent}
          onClose={() => setDrawer(null)}
        />
        <PersonResearchDrawer
          personId={id}
          personName={person.full_name}
          providerProfile={view.data.provider_profile}
          open={drawer === "research"}
          onClose={() => setDrawer(null)}
        />
        <PersonMeetingBrief
          activityId={view.data.next_meeting?.activity_id ?? null}
          open={drawer === "meeting"}
          onClose={() => setDrawer(null)}
        />
      </RecordView>
    </div>
  );
}

// Which drawer is open. Null is the ordinary state — the page is the thing the
// reader is looking at, and a drawer is a detour from it.
type Drawer = "composer" | "research" | "meeting" | null;

// The header's second line: what this person does, and where. The company is a
// link because it is a record of its own, not a label.
function PersonSubtitle({ view }: Readonly<{ view: Person360 }>): ReactNode {
  const person = view.person;
  const employment = view.employments?.data?.[0];
  return (
    <div>
      {person.title}
      {employment?.organization_name && (
        <>
          {person.title ? " · " : ""}
          <button
            type="button"
            className="pe-meta-link"
            onClick={() =>
              navigate({
                screen: "companies",
                id: employment.organization_id,
              })
            }
          >
            {employment.organization_name}
          </button>
        </>
      )}
    </div>
  );
}

// The identity line under the name: how to reach them, and who holds the
// relationship. ONE wrapping line rather than two — a reader takes the whole
// line in at once, and splitting it made the header three deep for facts that
// are each a few words long. Standing is quieter than a contact method within
// that line: it qualifies the record rather than being a way to act on it.
function PersonIdentityLine({
  view,
}: Readonly<{ view: Person360 }>): ReactNode {
  const t = useT();
  const person = view.person;
  const email = person.emails?.[0]?.email;
  const phone = person.phones?.[0]?.phone;
  const role = view.commercial?.role;
  return (
    <div className="pe-identity-meta">
      <div className="pe-meta-line">
        {email && (
          <span className="pe-meta-fact">
            <Mail size={13} aria-hidden="true" />
            {email}
          </span>
        )}
        {phone && (
          <span className="pe-meta-fact">
            <Phone size={13} aria-hidden="true" />
            {phone}
          </span>
        )}
        {person.address?.city && (
          <span className="pe-meta-fact">
            <MapPin size={13} aria-hidden="true" />
            {person.address.city}
          </span>
        )}
        {/* `social` is an open map on the wire, so its values are unknown to
            the type system. The fact renders only when there is a string to
            stand behind it — a link with nothing at the end is worse than no
            link at all. */}
        {typeof person.social?.linkedin === "string" && (
          <span className="pe-meta-fact">
            <LinkIcon size={13} aria-hidden="true" />
            {t("person.page.linkedin")}
          </span>
        )}
        {/* The role is what the relationship edge records — never inferred from
            a job title, which is why a person with a title can still have no
            buying role and the line simply omits it. */}
        {role && (
          <span className="pe-meta-fact pe-meta-quiet">
            {t("person.page.buyingRole")}: {role.replace(/_/g, " ")}
          </span>
        )}
        <span className="pe-meta-fact pe-meta-quiet">
          {t("person.page.owner")}:{" "}
          {view.person.owner_id
            ? t("person.page.ownerAssigned")
            : t("person.page.ownerUnassigned")}
        </span>
      </div>
    </div>
  );
}

// The primary actions, in the concept's order (§5.2). Email leads and is the
// only green one: a page with two primary actions has none.
function PersonActions({
  guardAllows,
  personId,
  onEmail,
  onResearch,
}: Readonly<{
  guardAllows: boolean;
  personId: string;
  onEmail: () => void;
  onResearch: () => void;
}>): ReactNode {
  const t = useT();
  return (
    <>
      {/* Email leads and is the only green one: a page with two primary
          actions has none. It is disabled when the guard refuses, so a rep
          learns they may not write BEFORE spending words on it. */}
      <Button variant="primary" disabled={!guardAllows} onClick={onEmail}>
        <Mail size={15} aria-hidden="true" /> {t("person.action.email")}
      </Button>
      <Button
        onClick={() =>
          navigate({ screen: "contacts", id: personId, id2: "activity" })
        }
      >
        <Phone size={15} aria-hidden="true" /> {t("person.action.call")}
      </Button>
      <Button
        onClick={() =>
          navigate({ screen: "contacts", id: personId, id2: "meetings" })
        }
      >
        <CalendarPlus size={15} aria-hidden="true" /> {t("person.action.book")}
      </Button>
      <Button onClick={() => navigate({ screen: "tasks" })}>
        <CheckSquare size={15} aria-hidden="true" />{" "}
        {t("person.action.addTask")}
      </Button>
      <Button onClick={onResearch}>
        <Search size={15} aria-hidden="true" /> {t("person.action.research")}
      </Button>
      <Button
        aria-label={t("person.action.more")}
        onClick={() =>
          navigate({ screen: "contacts", id: personId, id2: "history" })
        }
      >
        <MoreHorizontal size={15} aria-hidden="true" />
      </Button>
    </>
  );
}

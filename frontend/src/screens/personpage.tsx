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
import { api } from "../api/client";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
import { Button, SegmentedControl } from "../design-system/atoms";
import { RecordView } from "../design-system/composed";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { throwProblem } from "./common";
import {
  PersonBriefCard,
  PersonCommercialCard,
  PersonCommitmentsCard,
  PersonMattersCard,
} from "./personcards";
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

  if (view.isLoading) {
    return <div className="wrap">{t("person.page.loading")}</div>;
  }
  if (view.isError || !view.data) {
    return <div className="wrap">{t("person.page.notOpened")}</div>;
  }

  const person = view.data.person;
  const firstName = person.full_name.split(" ")[0];
  const emailVerdict = guard.data?.entries.find(
    (entry) => entry.channel === "email",
  );

  // The action loop. Every destination the server names routes here; one it
  // does not name opens nothing rather than guessing a path.
  const runAction = (action: PersonMomentAction) => {
    const destination = action.destination;
    if (!destination) {
      return;
    }
    if (destination.surface === "record" && destination.entity_id) {
      navigate({ screen: "deals", id: destination.entity_id });
    }
  };

  return (
    <div className="wrap">
      <RecordView
        name={person.full_name}
        avatarSrc={null}
        subtitle={<PersonSubtitle view={view.data} />}
        controls={<PersonOwner view={view.data} />}
        actions={
          <PersonActions guardAllows={emailVerdict?.verdict === "allowed"} />
        }
        zone="Europe/Berlin"
        asideLabel={t("person.page.asideLabel")}
        aside={
          <PersonRail
            view={view.data}
            guard={guard.data}
            firstName={firstName}
            onAction={runAction}
            onExplain={() => navigate({ screen: "contacts", id })}
          />
        }
      >
        <PersonStrip view={view.data} consentVerdict={emailVerdict?.verdict} />

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
          <>
            {view.data.moment && (
              <PersonToday
                moment={view.data.moment}
                firstName={firstName}
                onAction={runAction}
              />
            )}

            <div className="pe-grid">
              <div className="pe-col">
                <PersonBriefCard brief={brief.data} loading={brief.isLoading} />
                <PersonMattersCard view={view.data} firstName={firstName} />
              </div>
              <div className="pe-col">
                <PersonCommercialCard view={view.data} />
                <PersonCommitmentsCard view={view.data} firstName={firstName} />
              </div>
            </div>

            <PersonMemory view={view.data} />
          </>
        )}

        {tab !== "overview" && (
          // The other six tabs are addressable and named, and each says what
          // it will hold. An empty panel that looked like a rendering failure
          // would be worse than one that says what is coming.
          <section className="pe-card">
            <h3 className="pe-card-title">{t(TAB_LABEL_KEYS[tab])}</h3>
            <p className="pe-prose">
              {t("person.page.tabPlaceholder", {
                topic: t(TAB_TOPIC_KEYS[tab]),
              })}
            </p>
          </section>
        )}
      </RecordView>
    </div>
  );
}

// The header's second line: title · company, then the contact methods as
// compact pills (§5.2).
function PersonSubtitle({ view }: Readonly<{ view: Person360 }>): ReactNode {
  const t = useT();
  const person = view.person;
  const employment = view.employments?.data?.[0];
  const email = person.emails?.[0]?.email;
  const phone = person.phones?.[0]?.phone;
  return (
    <>
      <div>
        {person.title}
        {employment?.organization_name && (
          <>
            {person.title ? " · " : ""}
            <button
              type="button"
              className="pe-rail-more"
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
      <div className="pe-chiprow pe-chiprow-spaced">
        {email && (
          <span className="pe-memory-channel">
            <Mail size={13} aria-hidden="true" />
            {email}
          </span>
        )}
        {phone && (
          <span className="pe-memory-channel">
            <Phone size={13} aria-hidden="true" />
            {phone}
          </span>
        )}
        {person.address?.city && (
          <span className="pe-memory-channel">
            <MapPin size={13} aria-hidden="true" />
            {person.address.city}
          </span>
        )}
        {person.social?.linkedin && (
          <span className="pe-memory-channel">
            <LinkIcon size={13} aria-hidden="true" />
            {t("person.page.linkedin")}
          </span>
        )}
      </div>
    </>
  );
}

// Buying role and owner, top right. The role is what the relationship edge
// records — never inferred from a job title.
function PersonOwner({ view }: Readonly<{ view: Person360 }>): ReactNode {
  const t = useT();
  const role = view.commercial?.role;
  return (
    <div>
      {role && (
        <div className="pe-rail-row">
          <span className="pe-rail-label">{t("person.page.buyingRole")}</span>
          <span className="pe-rail-value">{role.replace(/_/g, " ")}</span>
        </div>
      )}
      <div className="pe-rail-row">
        <span className="pe-rail-label">{t("person.page.owner")}</span>
        <span className="pe-rail-value">
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
}: Readonly<{ guardAllows: boolean }>): ReactNode {
  const t = useT();
  return (
    <>
      <Button variant="primary" disabled={!guardAllows}>
        <Mail size={15} aria-hidden="true" /> {t("person.action.email")}
      </Button>
      <Button>
        <Phone size={15} aria-hidden="true" /> {t("person.action.call")}
      </Button>
      <Button>
        <CalendarPlus size={15} aria-hidden="true" /> {t("person.action.book")}
      </Button>
      <Button>
        <CheckSquare size={15} aria-hidden="true" />{" "}
        {t("person.action.addTask")}
      </Button>
      <Button>
        <Search size={15} aria-hidden="true" /> {t("person.action.research")}
      </Button>
      <Button aria-label={t("person.action.more")}>
        <MoreHorizontal size={15} aria-hidden="true" />
      </Button>
    </>
  );
}

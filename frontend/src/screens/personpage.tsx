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
import { throwProblem } from "./common";
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

const TAB_LABELS: Readonly<Record<PersonTab, string>> = {
  overview: "Overview",
  activity: "Activity",
  deals: "Deals",
  meetings: "Meetings",
  research: "Research",
  files: "Files",
  history: "History",
};

export function isPersonTab(value: string | undefined): value is PersonTab {
  return PERSON_TABS.includes((value ?? "") as PersonTab);
}

export function PersonPageV2({
  id,
  tab,
}: Readonly<{ id: string; tab: PersonTab }>) {
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
    return <div className="wrap">Loading…</div>;
  }
  if (view.isError || !view.data) {
    return <div className="wrap">This contact could not be opened.</div>;
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
        actions={<PersonActions guardAllows={emailVerdict?.verdict === "allowed"} />}
        zone="Europe/Berlin"
        asideLabel="Relationship context"
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
        <PersonStrip
          view={view.data}
          consent={consentWord(emailVerdict?.verdict)}
        />

        <div className="pe-tabs">
          <SegmentedControl
            options={PERSON_TABS}
            value={tab}
            onChange={(next) => navigate({ screen: "contacts", id, id2: next })}
            labels={TAB_LABELS}
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
                <PersonCommitmentsCard
                  view={view.data}
                  firstName={firstName}
                />
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
            <h3 className="pe-card-title">{TAB_LABELS[tab]}</h3>
            <p className="pe-prose">
              This tab is not built yet. The overview carries the relationship;
              this will carry {TAB_LABELS[tab].toLowerCase()}.
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
      <div className="pe-chiprow" style={{ marginTop: 8 }}>
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
            LinkedIn
          </span>
        )}
      </div>
    </>
  );
}

// Buying role and owner, top right. The role is what the relationship edge
// records — never inferred from a job title.
function PersonOwner({ view }: Readonly<{ view: Person360 }>): ReactNode {
  const role = view.commercial?.role;
  return (
    <div>
      {role && (
        <div className="pe-rail-row">
          <span className="pe-rail-label">Buying role</span>
          <span className="pe-rail-value">{role.replace(/_/g, " ")}</span>
        </div>
      )}
      <div className="pe-rail-row">
        <span className="pe-rail-label">Owner</span>
        <span className="pe-rail-value">
          {view.person.owner_id ? "Assigned" : "Unassigned"}
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
  return (
    <>
      <Button variant="primary" disabled={!guardAllows}>
        <Mail size={15} aria-hidden="true" /> Email
      </Button>
      <Button>
        <Phone size={15} aria-hidden="true" /> Call
      </Button>
      <Button>
        <CalendarPlus size={15} aria-hidden="true" /> Book
      </Button>
      <Button>
        <CheckSquare size={15} aria-hidden="true" /> Add task
      </Button>
      <Button>
        <Search size={15} aria-hidden="true" /> Research
      </Button>
      <Button aria-label="More actions">
        <MoreHorizontal size={15} aria-hidden="true" />
      </Button>
    </>
  );
}

function consentWord(verdict: string | undefined): string | null {
  switch (verdict) {
    case "allowed":
      return "Allowed";
    case "blocked":
      return "Blocked";
    case "unknown":
      return "Unknown";
    default:
      return null;
  }
}

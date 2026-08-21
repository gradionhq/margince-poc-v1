/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { ActorTag, AuditEntryLine, humanizeToken } from "./audit";

afterEach(() => {
  cleanup();
  // Restored per case, so one test pretending to be elsewhere never decides
  // what the next one reads.
  vi.restoreAllMocks();
});

// The viewer's zone as a screen asks for it. The formatters take their zone as
// an argument and never consult resolvedOptions, so this redirects only a
// screen's own lookup — which is exactly what a test of the zone CHOICE needs.
function pretendViewerZone(timeZone: string): void {
  const real = Intl.DateTimeFormat().resolvedOptions();
  vi.spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions").mockReturnValue({
    ...real,
    timeZone,
  });
}

type AuditLogEntry = components["schemas"]["AuditLogEntry"];

// ME is the viewer's bare app_user id, the way useMe() reports it. The wire's
// actor_id is the TYPED principal id, so a human row spells it "human:<ME>" —
// the two are different strings, and a fixture that pretends otherwise cannot
// catch a comparison that forgets the prefix.
const ME = "01a01740-c9c2-736d-a0b6-d3e3dcb13111";

const entry = (over: Partial<AuditLogEntry> = {}): AuditLogEntry => ({
  id: "a1",
  actor_type: "human",
  actor_id: `human:${ME}`,
  action: "create",
  entity_type: "custom_field",
  entity_id: "cf-1",
  occurred_at: "2026-07-10T14:09:00Z",
  ...over,
});

const wrap = (ui: React.ReactNode) =>
  render(<LocaleProvider initial="en">{ui}</LocaleProvider>);

describe("humanizeToken", () => {
  it("de-underscores an enum data value into a readable phrase", () => {
    expect(humanizeToken("advance_stage")).toBe("advance stage");
    expect(humanizeToken("custom_field")).toBe("custom field");
    expect(humanizeToken("create")).toBe("create");
  });
});

describe("ActorTag", () => {
  it("reads 'You' when the human actor is the viewer, matching the wire's prefixed id", () => {
    wrap(<ActorTag entry={entry({ actor_type: "human" })} meUserId={ME} />);
    expect(screen.getByText("You")).toBeTruthy();
  });

  it("names another human, rather than calling them 'a teammate'", () => {
    wrap(
      <ActorTag
        entry={entry({
          actor_type: "human",
          actor_id: "human:u-other",
          actor_name: "Lars Vogt",
        })}
        meUserId={ME}
      />,
    );
    expect(screen.getByText("Lars Vogt")).toBeTruthy();
    // Attribution exists so somebody can be asked about the change; the
    // opaque id is not that somebody and never reaches the reader.
    expect(screen.queryByText(/u-other/)).toBeNull();
  });

  it("says the member is unknown when no name resolved, never a raw uuid", () => {
    wrap(
      <ActorTag
        entry={entry({
          actor_type: "human",
          actor_id: "human:u-gone",
          actor_name: null,
        })}
        meUserId={ME}
      />,
    );
    expect(screen.getByText("Unknown member")).toBeTruthy();
    expect(screen.queryByText(/u-gone/)).toBeNull();
  });

  it("leads with the granting human and qualifies with the agent, not the reverse", () => {
    wrap(
      <ActorTag
        entry={entry({
          actor_type: "agent",
          actor_id: "agent:01a01740-c9c2-736d-a0b6-d3e3dcb13111",
          on_behalf_of: "u-lars",
          on_behalf_of_name: "Lars Vogt",
        })}
        meUserId={ME}
      />,
    );
    expect(screen.getByText("Lars Vogt")).toBeTruthy();
    expect(screen.getByText("via an agent")).toBeTruthy();
    // The passport uuid was the prominent half before PD-002. It is now not
    // shown at all: a person is answerable for the change, and the tool is a
    // qualifier on them.
    expect(screen.queryByText(/01a01740/)).toBeNull();
  });

  it("reads 'You' for an agent acting under the viewer's own authority", () => {
    wrap(
      <ActorTag
        entry={entry({
          actor_type: "agent",
          actor_id: "agent:p1",
          on_behalf_of: ME,
          on_behalf_of_name: "Lars Vogt",
        })}
        meUserId={ME}
      />,
    );
    expect(screen.getByText("You")).toBeTruthy();
    expect(screen.getByText("via an agent")).toBeTruthy();
  });

  it("calls a presented-but-unresolved grant a gap, rather than falling back to 'System'", () => {
    wrap(
      <ActorTag
        entry={entry({
          actor_type: "agent",
          actor_id: "agent:p1",
          passport_id: "01a01740-c9c2-736d-a0b6-d3e3dcb13999",
          on_behalf_of: null,
          on_behalf_of_name: null,
        })}
        meUserId={ME}
      />,
    );
    expect(screen.getByText("No human authority recorded")).toBeTruthy();
    // The identifier is what is left to show, so it IS shown — but as the
    // subordinate half, not the label.
    expect(screen.getByText("agent:p1")).toBeTruthy();
    expect(screen.queryByText("System")).toBeNull();
  });

  it("does not call a background agent a gap — no grant was presented", () => {
    wrap(
      <ActorTag
        entry={entry({
          actor_type: "agent",
          actor_id: "agent:extension_tick",
          passport_id: null,
          on_behalf_of: null,
          on_behalf_of_name: null,
        })}
        meUserId={ME}
      />,
    );
    // compose/extjobsrun.go writes exactly this shape per extension job tick.
    // There is no human to name and nothing failed, so reporting a missing
    // authority would report a defect that does not exist.
    expect(screen.getByText("agent:extension_tick")).toBeTruthy();
    expect(screen.queryByText("No human authority recorded")).toBeNull();
    expect(screen.queryByText("System")).toBeNull();
  });

  it("never renders an actor with no label at all", () => {
    // actor_id is a bare `string` in the contract with no minimum length. No
    // writer should produce an empty one, but an icon with nothing beside it
    // would be a row attributed to nothing, so the kind stands in.
    wrap(
      <ActorTag
        entry={entry({ actor_type: "connector", actor_id: "" })}
        meUserId={ME}
      />,
    );
    expect(screen.getByText("connector")).toBeTruthy();
  });

  it("names the human behind a connector when one authorised it", () => {
    wrap(
      <ActorTag
        entry={entry({
          actor_type: "connector",
          actor_id: "connector:gmail",
          on_behalf_of: "u-lars",
          on_behalf_of_name: "Lars Vogt",
        })}
        meUserId={ME}
      />,
    );
    expect(screen.getByText("Lars Vogt")).toBeTruthy();
    expect(screen.getByText("via a connector")).toBeTruthy();
  });

  it("shows a bare connector's own name — no granting human is not a gap there", () => {
    wrap(
      <ActorTag
        entry={entry({
          actor_type: "connector",
          actor_id: "connector:finance",
        })}
        meUserId={ME}
      />,
    );
    expect(screen.getByText("connector:finance")).toBeTruthy();
    expect(screen.queryByText("No human authority recorded")).toBeNull();
  });

  it("reads 'System' for a system actor", () => {
    wrap(
      <ActorTag
        entry={entry({ actor_type: "system", actor_id: "system" })}
        meUserId={ME}
      />,
    );
    expect(screen.getByText("System")).toBeTruthy();
  });
});

describe("AuditEntryLine", () => {
  it("renders a readable actor + action + entity, never the raw uuids", () => {
    wrap(
      <AuditEntryLine
        entry={entry({
          actor_type: "human",
          action: "create",
          entity_type: "custom_field",
          entity_id: "cf-1",
        })}
        meUserId={ME}
      />,
    );
    expect(screen.getByText("You")).toBeTruthy();
    expect(screen.getByText("create")).toBeTruthy();
    expect(screen.getByText("custom field")).toBeTruthy();
    // the opaque uuids never reach the reader
    expect(screen.queryByText(/cf-1/)).toBeNull();
    expect(screen.queryByText(new RegExp(ME))).toBeNull();
  });

  it("dates an entry on the organization's clock, not the reader's", () => {
    // 18:00Z on 21 August is 20:00 the same day in Berlin and 01:00 the NEXT
    // day in Ho Chi Minh City. An audit line is a fact in the shared book, so
    // two investigators must be able to quote it by the same day: reading it on
    // the viewer's clock is how one of them ends up quoting 22 August.
    pretendViewerZone("Asia/Ho_Chi_Minh");
    wrap(
      <AuditEntryLine
        entry={entry({ occurred_at: "2026-08-21T18:00:00Z" })}
        meUserId={ME}
      />,
    );
    // en-GB renders DD/MM/YYYY (format.ts INTL_LOCALE), so the day is the first
    // field and the assertion is about which calendar day is claimed.
    expect(screen.getByText(/^21\/08\/2026/)).toBeTruthy();
    expect(screen.queryByText(/22\/08\/2026/)).toBeNull();
  });

  it("keeps the machine-readable instant on the line it dates", () => {
    // The rendered day is one zone's reading of the entry; anyone who needs
    // another zone's reading needs the instant itself, and a shared clock is
    // only defensible while that instant is still on the page.
    const occurredAt = "2026-08-21T18:00:00Z";
    wrap(<AuditEntryLine entry={entry({ occurred_at: occurredAt })} />);
    expect(screen.getByText(/21\/08\/2026/).getAttribute("datetime")).toBe(
      occurredAt,
    );
  });
});

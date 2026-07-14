/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { ActorTag, AuditEntryLine, humanizeToken } from "./audit";

afterEach(cleanup);

type AuditLogEntry = components["schemas"]["AuditLogEntry"];

const entry = (over: Partial<AuditLogEntry> = {}): AuditLogEntry => ({
  id: "a1",
  workspace_id: "w",
  actor_type: "human",
  actor_id: "u1",
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
  it("reads 'You' when the human actor is the viewer", () => {
    wrap(
      <ActorTag
        entry={entry({ actor_type: "human", actor_id: "u1" })}
        meUserId="u1"
      />,
    );
    expect(screen.getByText("You")).toBeTruthy();
  });

  it("reads 'A teammate' for another human, never the raw uuid", () => {
    wrap(
      <ActorTag
        entry={entry({ actor_type: "human", actor_id: "u-other" })}
        meUserId="u1"
      />,
    );
    expect(screen.getByText("A teammate")).toBeTruthy();
    expect(screen.queryByText("u-other")).toBeNull();
  });

  it("shows the agent slug and its on-behalf-of human", () => {
    wrap(
      <ActorTag
        entry={entry({
          actor_type: "agent",
          actor_id: "sdr",
          on_behalf_of: "u1",
        })}
        meUserId="u1"
      />,
    );
    expect(screen.getByText("sdr")).toBeTruthy();
    expect(screen.getByText(/on behalf of you/i)).toBeTruthy();
  });

  it("shows the connector slug", () => {
    wrap(
      <ActorTag
        entry={entry({ actor_type: "connector", actor_id: "gmail" })}
        meUserId="u1"
      />,
    );
    expect(screen.getByText("gmail")).toBeTruthy();
  });

  it("reads 'System' for a system actor", () => {
    wrap(
      <ActorTag
        entry={entry({ actor_type: "system", actor_id: "cron" })}
        meUserId="u1"
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
          actor_id: "u1",
          action: "create",
          entity_type: "custom_field",
          entity_id: "cf-1",
        })}
        meUserId="u1"
      />,
    );
    expect(screen.getByText("You")).toBeTruthy();
    expect(screen.getByText("create")).toBeTruthy();
    expect(screen.getByText("custom field")).toBeTruthy();
    // the opaque uuids never reach the reader
    expect(screen.queryByText(/cf-1/)).toBeNull();
    expect(screen.queryByText(/human:u1/)).toBeNull();
  });
});

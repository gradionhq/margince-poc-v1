import { describe, expect, it } from "vitest";
import {
  dirtyKeys,
  displayOn,
  initialDraft,
  type PurposeView,
  toChoices,
} from "./preferences.logic";

const purposes: PurposeView[] = [
  {
    key: "transactional",
    label: "Deal & service messages",
    state: "granted",
    locked: true,
  },
  {
    key: "marketing_email",
    label: "Product updates",
    state: "granted",
    locked: false,
  },
  { key: "events", label: "Events", state: "withdrawn", locked: false },
  { key: "research", label: "Surveys", state: "unknown", locked: false },
];

describe("display state", () => {
  // Default-deny: no record and a withdrawal both mean "we may not send".
  it("reads only an explicit grant as subscribed", () => {
    expect(displayOn("granted")).toBe(true);
    expect(displayOn("withdrawn")).toBe(false);
    expect(displayOn("unknown")).toBe(false);
  });
});

describe("draft diffing", () => {
  it("starts clean", () => {
    const draft = initialDraft(purposes);
    expect(draft).toEqual({
      transactional: true,
      marketing_email: true,
      events: false,
      research: false,
    });
    expect(dirtyKeys(purposes, draft)).toEqual([]);
  });

  it("reports only what the subject actually moved", () => {
    const draft = {
      ...initialDraft(purposes),
      marketing_email: false,
      events: true,
    };
    expect(dirtyKeys(purposes, draft)).toEqual(["marketing_email", "events"]);
  });

  it("never reports a locked purpose, even if a draft claims it moved", () => {
    const draft = { ...initialDraft(purposes), transactional: false };
    expect(dirtyKeys(purposes, draft)).toEqual([]);
  });
});

describe("choice building", () => {
  const wordingOf = (key: string) => `"Send me ${key}."`;

  // The load-bearing rule: an untouched purpose is never submitted. A choice
  // writes an append-only proof row, so submitting one the subject didn't
  // make would fabricate consent evidence.
  it("submits only changed purposes, with the wording shown", () => {
    const draft = {
      ...initialDraft(purposes),
      marketing_email: false,
      events: true,
    };
    expect(toChoices(purposes, draft, wordingOf)).toEqual([
      {
        purpose_key: "marketing_email",
        state: "withdrawn",
        wording: '"Send me marketing_email."',
      },
      { purpose_key: "events", state: "granted", wording: '"Send me events."' },
    ]);
  });

  it("submits nothing when nothing moved", () => {
    expect(toChoices(purposes, initialDraft(purposes), wordingOf)).toEqual([]);
  });
});

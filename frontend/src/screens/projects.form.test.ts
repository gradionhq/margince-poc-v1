// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { describe, expect, it } from "vitest";
import {
  mapProjectCreate,
  mapProjectUpdate,
  PROJECT_KEY_PATTERN,
  projectFields,
  projectKeyRefusal,
} from "./projects.form";

// The project form's pure half: the key rule the contract states as a
// pattern, and the two bodies the dialogs build from their values.

describe("projectKeyRefusal", () => {
  it("accepts an empty key, because the key is optional", () => {
    expect(projectKeyRefusal("")).toBeUndefined();
    expect(projectKeyRefusal("   ")).toBeUndefined();
  });

  it("accepts the contract's shape: letter-led, 2–24 of [A-Za-z0-9_-]", () => {
    for (const key of ["AB", "ACME-CRM", "p_1", "a".repeat(24)]) {
      expect(projectKeyRefusal(key), key).toBeUndefined();
      expect(PROJECT_KEY_PATTERN.test(key)).toBe(true);
    }
  });

  it("refuses a bare number, a one-character key, a space and an overlong key", () => {
    for (const key of [
      "2026",
      "A",
      "ACME CRM",
      "1ABC",
      "a".repeat(25),
      "é-x",
    ]) {
      expect(projectKeyRefusal(key), key).toBe("project.keyInvalid");
    }
  });
});

describe("projectFields", () => {
  const t = (key: string) => `<${key}>`;

  it("asks for the company at birth and never on edit", () => {
    const create = projectFields(t, {
      companies: [{ id: "o-1", display_name: "Brandt" }],
      me: "u-me",
      currentOwner: null,
      mode: "create",
    });
    const edit = projectFields(t, {
      companies: [],
      me: "u-me",
      currentOwner: "u-other",
      mode: "edit",
    });
    expect(create.map((field) => field.key)).toEqual([
      "name",
      "key",
      "organization_id",
      "owner_id",
      "description",
      "target_end_date",
    ]);
    expect(edit.map((field) => field.key)).not.toContain("organization_id");
  });

  it("lets an edit keep an owner who is not the reader", () => {
    const edit = projectFields(t, {
      companies: [],
      me: "u-me",
      currentOwner: "u-other",
      mode: "edit",
    });
    const owner = edit.find((field) => field.key === "owner_id");
    expect(owner?.options?.map((option) => option.value)).toEqual([
      "u-other",
      "u-me",
      "",
    ]);
  });

  it("carries the key rule into the field's own validation", () => {
    const create = projectFields(t, {
      companies: [],
      me: "u-me",
      currentOwner: null,
      mode: "create",
    });
    const key = create.find((field) => field.key === "key");
    expect(key?.validate?.("2026")).toBe("<project.keyInvalid>");
    expect(key?.validate?.("ACME-CRM")).toBeUndefined();
    expect(key?.hint).toBe("<project.keyHint>");
  });
});

describe("mapProjectCreate / mapProjectUpdate", () => {
  it("builds the birth body with blanks as null and a manual source", () => {
    expect(
      mapProjectCreate({
        name: " Rollout ",
        key: "",
        organization_id: "o-1",
        owner_id: "",
        description: "",
        target_end_date: "2026-12-31",
      }),
    ).toEqual({
      name: "Rollout",
      key: null,
      organization_id: "o-1",
      owner_id: null,
      description: null,
      target_end_date: "2026-12-31",
      source: "manual",
    });
  });

  it("clears a blanked scalar on update but never sends an empty name", () => {
    const patch = mapProjectUpdate({
      name: "",
      key: "ACME",
      owner_id: "",
      description: "Phase two",
      target_end_date: "",
    });
    expect(patch).toEqual({
      name: undefined,
      key: "ACME",
      owner_id: null,
      description: "Phase two",
      target_end_date: null,
    });
    expect("phase" in patch).toBe(false);
  });
});

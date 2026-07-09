/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { type Locale, LocaleProvider } from "../i18n";
import { FieldGuard, RoleBadge } from "./rbac";

afterEach(cleanup);

const renderIn = (locale: Locale, ui: ReactNode) =>
  render(<LocaleProvider initial={locale}>{ui}</LocaleProvider>);

describe("RoleBadge", () => {
  it("renders seeded role keys as localized labels", () => {
    renderIn(
      "en",
      <>
        <RoleBadge roleKey="admin" />
        <RoleBadge roleKey="read_only" />
      </>,
    );
    expect(screen.getByText("Admin")).toBeTruthy();
    expect(screen.getByText("Read-only")).toBeTruthy();
    expect(screen.queryByText("read_only")).toBeNull();
  });

  it("localizes to German", () => {
    renderIn(
      "de",
      <>
        <RoleBadge roleKey="rep" />
        <RoleBadge roleKey="read_only" />
      </>,
    );
    expect(screen.getByText("Vertrieb")).toBeTruthy();
    expect(screen.getByText("Nur Lesen")).toBeTruthy();
  });

  it("falls back to the raw key for a workspace-defined role — never invented copy", () => {
    renderIn("en", <RoleBadge roleKey="field_marketing" />);
    expect(screen.getByText("field_marketing")).toBeTruthy();
  });
});

describe("FieldGuard", () => {
  it("masked reads as withheld — a visible mask with a11y semantics, the value never rendered", () => {
    renderIn("en", <FieldGuard mode="masked">mgp_secret</FieldGuard>);
    expect(screen.getByRole("img", { name: "Masked value" })).toBeTruthy();
    expect(screen.queryByText(/mgp_secret/)).toBeNull();
  });

  it("visible passes the value through without a mask node", () => {
    renderIn("en", <FieldGuard mode="visible">plain value</FieldGuard>);
    expect(screen.getByText("plain value")).toBeTruthy();
    expect(screen.queryByRole("img", { name: "Masked value" })).toBeNull();
  });
});

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  previewedOidcProviders,
  previewedPasswordReset,
  previewedUnavailableProviders,
  uiPreviewOidcEnabled,
  uiPreviewResetEnabled,
} from "./ui-preview";

// The UI-preview switch, pinned in BOTH positions. Off is the one that matters
// most: a preview switch nobody checks the default of is how presentation
// scaffolding ships to production.

afterEach(() => {
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

// Stands in for the screen's `t("auth.continueWith", …)`. It records its calls so
// the pass-through cases can assert the label was never asked for: on a served
// provider the wire owns the button text, and a preview that consulted this on
// that path would be composing copy the contract says is the server's.
function labelFactory() {
  const asked: string[] = [];
  return {
    asked,
    label: (providerKey: string) => {
      asked.push(providerKey);
      return `Weiter mit ${providerKey}`;
    },
  };
}

describe("VITE_UI_PREVIEW_OIDC", () => {
  it("is off with no env var, and passes the server's answer through verbatim", () => {
    expect(import.meta.env.VITE_UI_PREVIEW_OIDC).toBeUndefined();
    expect(uiPreviewOidcEnabled()).toBe(false);
    // The empty capability the real server serves reaches the screen unchanged,
    // which is what keeps ProviderButtons rendering nothing (§19).
    const served: { key: string; label: string }[] = [];
    const { asked, label } = labelFactory();
    expect(previewedOidcProviders(served, label)).toBe(served);
    expect(asked).toEqual([]);
  });

  it("is off for any value that is not an explicit yes", () => {
    for (const value of ["", "0", "false", "no", "yes", "on"]) {
      vi.stubEnv("VITE_UI_PREVIEW_OIDC", value);
      expect(uiPreviewOidcEnabled(), value).toBe(false);
    }
  });

  it("substitutes two inert providers when explicitly enabled", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    for (const value of ["1", "true"]) {
      vi.stubEnv("VITE_UI_PREVIEW_OIDC", value);
      expect(uiPreviewOidcEnabled()).toBe(true);
      const { label } = labelFactory();
      expect(previewedOidcProviders([], label)).toEqual([
        { key: "google", label: "Weiter mit google" },
        { key: "microsoft", label: "Weiter mit microsoft" },
      ]);
    }
    // Warned, so a preview build says out loud that it is one. Once, not per
    // render — the override site is called on every paint of the login screen.
    expect(warn).toHaveBeenCalledTimes(1);
  });

  it("never overrides an installation that does serve providers", () => {
    vi.stubEnv("VITE_UI_PREVIEW_OIDC", "1");
    const served = [{ key: "corp-sso", label: "Anmeldung über Werk-IT" }];
    const { asked, label } = labelFactory();
    expect(previewedOidcProviders(served, label)).toBe(served);
    // The installation's own wording survives, untouched and unconsulted.
    expect(asked).toEqual([]);
  });

  // The per-provider marker rides the SAME switch, and the empty default is the
  // assertion that matters: an empty set is what makes ProviderButtons' product
  // behaviour identical to what it was before the marker existed.
  it("marks no provider unavailable with the switch off", () => {
    expect(previewedUnavailableProviders().size).toBe(0);
  });

  it("marks exactly microsoft unavailable with the switch on", () => {
    vi.stubEnv("VITE_UI_PREVIEW_OIDC", "1");
    expect([...previewedUnavailableProviders()]).toEqual(["microsoft"]);
  });
});

describe("VITE_UI_PREVIEW_RESET", () => {
  it("is off with no env var, and passes the server's answer through verbatim", () => {
    expect(import.meta.env.VITE_UI_PREVIEW_RESET).toBeUndefined();
    expect(uiPreviewResetEnabled()).toBe(false);
    // `false` is what the running installation reports — no `email:` block, so no
    // mailer, so no reset flow — and it has to reach the screen unchanged, which
    // is what keeps the forgot-password link absent.
    expect(previewedPasswordReset(false)).toBe(false);
  });

  it("is off for any value that is not an explicit yes", () => {
    for (const value of ["", "0", "false", "no", "yes", "on"]) {
      vi.stubEnv("VITE_UI_PREVIEW_RESET", value);
      expect(uiPreviewResetEnabled(), value).toBe(false);
      expect(previewedPasswordReset(false), value).toBe(false);
    }
  });

  it("draws the reset link when explicitly enabled", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    for (const value of ["1", "true"]) {
      vi.stubEnv("VITE_UI_PREVIEW_RESET", value);
      expect(uiPreviewResetEnabled()).toBe(true);
      expect(previewedPasswordReset(false)).toBe(true);
    }
    // Once, not per render — the override site is called on every paint.
    expect(warn).toHaveBeenCalledTimes(1);
  });

  it("never overrides an installation that does serve the reset flow", () => {
    vi.stubEnv("VITE_UI_PREVIEW_RESET", "1");
    // A served `true` is the truth and comes back untouched; the preview only
    // ever fills a genuine `false`.
    expect(previewedPasswordReset(true)).toBe(true);
    vi.unstubAllEnvs();
    expect(previewedPasswordReset(true)).toBe(true);
  });
});

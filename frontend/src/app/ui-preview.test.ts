import { afterEach, describe, expect, it, vi } from "vitest";
import { previewedOidcProviders, uiPreviewOidcEnabled } from "./ui-preview";

// The UI-preview switch, pinned in BOTH positions. Off is the one that matters
// most: a preview switch nobody checks the default of is how presentation
// scaffolding ships to production.

afterEach(() => {
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

describe("VITE_UI_PREVIEW_OIDC", () => {
  it("is off with no env var, and passes the server's answer through verbatim", () => {
    expect(import.meta.env.VITE_UI_PREVIEW_OIDC).toBeUndefined();
    expect(uiPreviewOidcEnabled()).toBe(false);
    // The empty capability the real server serves reaches the screen unchanged,
    // which is what keeps ProviderButtons rendering nothing (§19).
    const served: { key: string; label: string }[] = [];
    expect(previewedOidcProviders(served)).toBe(served);
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
      expect(previewedOidcProviders([]).map((p) => p.key)).toEqual([
        "google",
        "microsoft",
      ]);
    }
    // Warned, so a preview build says out loud that it is one. Once, not per
    // render — the override site is called on every paint of the login screen.
    expect(warn).toHaveBeenCalledTimes(1);
  });

  it("never overrides an installation that does serve providers", () => {
    vi.stubEnv("VITE_UI_PREVIEW_OIDC", "1");
    const served = [{ key: "corp-sso", label: "Anmeldung über Werk-IT" }];
    expect(previewedOidcProviders(served)).toBe(served);
  });
});

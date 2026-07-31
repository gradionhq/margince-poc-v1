import { KeyRound } from "lucide-react";
import "./provider-mark.css";

/*
 * SANCTIONED LITERALS — third-party brand marks.
 *
 * Google's and Microsoft's own colours, identifying their sign-in providers.
 * These are not design-system colours and must not be tokenised: a provider
 * mark rendered in Ledger Green is a wrong mark, and re-tinting another
 * company's logo is not ours to do. This file is the ONE named exclusion in
 * both colour gates — `scripts/check-ds-purity.sh` and the
 * `keeps literal colours in tokens.css only` case in
 * `design-system/conformance.test.ts` — each carrying that reason.
 *
 * Lucide ships no brand logos, so the two marks are inline SVG. They live in the
 * design system rather than in the auth screen because the mailbox-connect step
 * needs the same two: a second copy of Google's palette is a second place for
 * the rule above to quietly stop holding.
 *
 * The mark is chosen from the provider's `key`, which is the ONLY part of a
 * provider this frontend decides. The button's label is the installation's own
 * string, served by `/auth/capabilities` — see `ProviderButtons` in
 * `screens/auth.tsx`.
 */

/**
 * The brand word for a key this frontend recognises, or null.
 *
 * Same knowledge as the mark, so it lives beside it: recognising `google` well
 * enough to draw its logo is recognising it well enough to name it. A narrow
 * layout can then show "Google" where the installation's full label
 * ("Continue with Google") does not fit on one line.
 *
 * Two things this deliberately is NOT. It is not a translation — these are proper
 * nouns and stay identical in every locale, the same rule the served label
 * follows (§11.5). And it is not derived from the label by trimming a leading
 * "Continue with": the next installation's label is "Sign in with Okta" or a
 * German sentence, and a trim would mangle both. An unrecognised key returns
 * null, and the caller shows the server's own words — which are the only thing
 * that can be right for a provider this file has never heard of.
 */
export function providerBrandName(providerKey: string): string | null {
  if (providerKey === "google") {
    return "Google";
  }
  if (providerKey === "microsoft") {
    return "Microsoft";
  }
  return null;
}

/**
 * An unrecognised key still gets a mark.
 *
 * `AuthCapabilities.oidc_providers[].key` is an open string in the contract, so
 * a self-hosted installation can name a provider this frontend has never heard
 * of. Rendering nothing for it would hide a working sign-in path; a neutral key
 * icon says "a provider, not one I have a logo for", which is the honest answer.
 */
export function ProviderMark({
  providerKey,
}: Readonly<{ providerKey: string }>) {
  if (providerKey === "google") {
    return (
      <svg
        className="provider-mark"
        viewBox="0 0 24 24"
        aria-hidden="true"
        focusable="false"
      >
        <path
          fill="#4285F4"
          d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92a5.06 5.06 0 0 1-2.2 3.32v2.76h3.56c2.08-1.92 3.28-4.74 3.28-8.09Z"
        />
        <path
          fill="#34A853"
          d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.56-2.76c-.98.66-2.24 1.06-3.72 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84A11 11 0 0 0 12 23Z"
        />
        <path
          fill="#FBBC05"
          d="M5.84 14.11a6.6 6.6 0 0 1 0-4.22V7.05H2.18a11 11 0 0 0 0 9.9l3.66-2.84Z"
        />
        <path
          fill="#EA4335"
          d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1A11 11 0 0 0 2.18 7.05l3.66 2.84C6.71 7.31 9.14 5.38 12 5.38Z"
        />
      </svg>
    );
  }
  if (providerKey === "microsoft") {
    return (
      <svg
        className="provider-mark"
        viewBox="0 0 24 24"
        aria-hidden="true"
        focusable="false"
      >
        <path fill="#F25022" d="M2 2h9.5v9.5H2z" />
        <path fill="#7FBA00" d="M12.5 2H22v9.5h-9.5z" />
        <path fill="#00A4EF" d="M2 12.5h9.5V22H2z" />
        <path fill="#FFB900" d="M12.5 12.5H22V22h-9.5z" />
      </svg>
    );
  }
  return <KeyRound className="provider-mark" aria-hidden="true" />;
}

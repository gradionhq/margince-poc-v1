// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useState } from "react";
import { Button } from "../design-system/atoms";
import { useT } from "../i18n";
import {
  clearPendingAuthorize,
  readPendingAuthorize,
} from "./pendingauthorize";

// The other half of the promise the empty consent screen makes (I7/I8): a
// human sent away to mint a passport is brought back here — mounted once in
// settings.tsx above the tab content, so it shows on whichever tab they land
// on, not only the `ai` deep link. Without this, minting a passport strands
// the human with a connection they started and no way back to it.
export function ResumeConnectBanner() {
  const t = useT();
  // sessionStorage is the source of truth for whether a connection is
  // pending; local state only tracks whether THIS mount has since cleared
  // it (resumed or cancelled), so the banner can disappear without a
  // remount forcing a re-read.
  const [pending, setPending] = useState(readPendingAuthorize);

  // I9: no stash, no banner. Offering to resume a request that was already
  // approved, denied, or abandoned is an invitation to a confusing failure
  // at the end of a flow that was never pending.
  if (!pending) {
    return null;
  }

  return (
    <div
      role="status"
      className="card card-inset"
      style={{
        marginBottom: "var(--space-4)",
        display: "flex",
        flexDirection: "column",
        gap: "var(--space-3)",
      }}
    >
      <strong>
        {t("consent.resumeTitle", { client: pending.clientName })}
      </strong>
      <p>{t("consent.resumeBody", { client: pending.clientName })}</p>
      <div style={{ display: "flex", gap: "var(--space-2)" }}>
        <Button
          variant="primary"
          onClick={() => {
            // /oauth/authorize is a server endpoint, not a hash route:
            // re-entering it re-validates the request and arms a FRESH
            // nonce, since the one this stash was made under has a 300s
            // TTL that minting a passport can easily outlast. A hash
            // navigation back to the consent screen would hand the human a
            // request that has quietly died.
            const url = pending.url;
            clearPendingAuthorize();
            globalThis.location.assign(url);
          }}
        >
          {t("consent.resume")}
        </Button>
        <Button
          onClick={() => {
            clearPendingAuthorize();
            setPending(null);
          }}
        >
          {t("consent.resumeDismiss")}
        </Button>
      </div>
    </div>
  );
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { LicenseHolderCard } from "./licenseholder";
import { StoryProviders } from "./story-utils";

// Settings → License, the card above the seat meter. It answers who holds the
// license and how long it lasts.

type LicenseHolder = components["schemas"]["LicenseHolder"];

const HOLDER: LicenseHolder = {
  id: "0199c4f2-1d6e-7a41-9f0b-7b2a2c1d5e30",
  subject: "acme-prod",
  org: "Acme GmbH",
  contact_name: "Ada Lovelace",
  contact_email: "ada@acme.example",
  expiry: "2027-08-14T09:00:00Z",
  in_grace: false,
  renewal_due: false,
};

function story(holder: LicenseHolder) {
  return () => (
    <StoryProviders>
      <LicenseHolderCard holder={holder} />
    </StoryProviders>
  );
}

const meta: Meta<typeof LicenseHolderCard> = {
  title: "Settings/Admin settings/License/Licensee",
  component: LicenseHolderCard,
};
export default meta;
type Story = StoryObj<typeof LicenseHolderCard>;

// Every claim present, and a year to run.
export const Complete: Story = { render: story(HOLDER) };

// A license issued before the licensee claims existed. It verifies exactly like
// the one above, and the rows it cannot fill are absent rather than empty — the
// state this card is most likely to meet on an installation that has been
// licensed for a while.
export const WithoutLicenseeDetail: Story = {
  render: story({
    id: HOLDER.id,
    subject: HOLDER.subject,
    expiry: HOLDER.expiry,
    in_grace: false,
    renewal_due: false,
  }),
};

// Inside the warning window. Amber, and not an alert: nothing has gone wrong,
// and a renewal is a thing to plan.
export const RenewalDue: Story = {
  render: story({ ...HOLDER, renewal_due: true }),
};

// Past expiry, still accepted. The one state that interrupts, because the
// installation will stop working. The renewal warning stands down for it: two
// notices about one date is one notice too many.
export const InGrace: Story = {
  render: story({ ...HOLDER, in_grace: true, renewal_due: true }),
};

// The grace notice in dark. A danger callout above a plain definition list is
// the pairing dark compresses — the tinted panel and the card under it are two
// mixes of the same canonical token.
export const InGraceDark: Story = {
  globals: { theme: "dark" },
  render: story({ ...HOLDER, in_grace: true, renewal_due: true }),
};

// At 390px, where the two-column list has to fold. The license id is the row
// that decides it: it does not break at a space, so this story is what shows
// whether it wraps or pushes the column.
export const Narrow: Story = {
  globals: { viewport: { value: "phone" } },
  render: story(HOLDER),
};

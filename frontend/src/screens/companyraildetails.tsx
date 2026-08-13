// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { components } from "../api/schema";
import { useCan } from "../app/capability";
import { FieldGrid, FieldRow } from "../design-system/fieldgrid";
import { InlineChoice, InlineText } from "../design-system/inlinechoice";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import {
  CompanyLifecycleControl,
  CompanyOwnerControl,
  useCompanyFieldPatch,
  useCompanyReadOnlyReason,
} from "./companyheader";
import { SIZE_BAND_OPTIONS } from "./companylookups";

// The rail's own Details grid (companyrail.tsx's DetailsGrid), split into
// this file so the rail file stays under the 500-line ceiling: one panel
// section (Details) and its field rows is a natural seam, not an arbitrary
// cut.

type Organization = components["schemas"]["Organization"];
type OrganizationDomain = NonNullable<Organization["domains"]>[number];
// Not `keyof Address`: the wire type carries a `[key: string]: unknown` catch-all
// alongside its six named parts (schema.d.ts's own escape hatch for a future
// field), which collapses `keyof` down to bare `string` and loses every part's
// actual value type. The six literals are the only ones this row ever writes.
type AddressPart =
  | "line1"
  | "line2"
  | "city"
  | "region"
  | "postal_code"
  | "country";
type UpdateOrganizationRequest =
  components["schemas"]["UpdateOrganizationRequest"];

// The column's own CHECK bound (core 0203) — stops the reader at the limit
// rather than letting the server refuse the save. Same figure companyheader.tsx
// used to cap its own (now-removed) description control at.
const DESCRIPTION_MAX_LENGTH = 500;

/**
 * DetailsGrid draws the account's own fields as a label/value grid: legal
 * name, where it stands with us, who owns it, its domain, its address (one
 * row per part), industry, size, LinkedIn page and description. EVERY known
 * field draws a row, whether or not the account carries a value: an absent
 * field is a fact about the record (nobody has filled it in yet), and hiding
 * the row along with the fact erases the "yet" — a reader can only add what
 * they can see is missing. An empty row reads as a quiet add affordance
 * (InlineText/InlineChoice's own empty-state button) rather than a blank
 * line.
 *
 * Writability gates the VERBS only, never the values: an archived or
 * overlay-mirrored account still shows every field, it simply shows them
 * without the edit affordance (InlineText/InlineChoice's own
 * `canEdit={false}` path). Derived internally from `useCan("organization",
 * "update")` and `useCompanyReadOnlyReason` — the same RBAC grant and the
 * same archived/overlay reasoning the header's own inline controls already
 * gate on — rather than threaded down as a prop, so a caller cannot render
 * this grid writable on a record it should not be able to write.
 *
 * Every field here edits in place, including lifecycle, domain and address —
 * none of the three is scalar the way legal name or industry is, so each
 * keeps its own rule about what "editing this" is allowed to touch:
 *
 *   - Lifecycle and owner both reuse the header's OWN control
 *     (`CompanyLifecycleControl` / `CompanyOwnerControl`) rather than a
 *     second InlineChoice PATCHing the same field down its own path — one
 *     implementation of "how this field is written," mounted here and in
 *     the header, so the two can never disagree about what they last wrote.
 *   - Domain edits the PRIMARY domain's name only, and always sends the
 *     record's full domain array back with that one entry changed: `domains`
 *     is a replace-set on the wire (interfaces.md), so a write that sent
 *     just the edited entry would silently drop every other domain the
 *     account has. Clearing the field to empty is refused client-side
 *     (`field.domainRequired`) rather than sent as a delete — removing a
 *     domain outright stays in the full editor, which can retarget which
 *     domain is primary and prompts for confirmation.
 *   - Address is six rows, one per part (line 1, line 2, city, region,
 *     postal code, country) rather than one grouped editor: each part reuses
 *     InlineText exactly as it stands, with zero new commit machinery, at
 *     the cost of a longer grid than a single "Address" row would be. Every
 *     write sends the WHOLE address object back with only that one part
 *     changed (`{...current, [part]: next}`) — `address` replaces the
 *     object wholesale on the wire, so omitting the untouched parts would
 *     blank them.
 *
 * ABSENT VS WITHHELD, stated rather than built: this grid does not today
 * distinguish a field nobody has filled in from one the viewer's role cannot
 * see, because `Organization` carries no field-level grant signal to draw
 * that distinction from — only `computed_fields` does (STATE-4), and it is
 * not one of these fields. `FieldGuard` (design-system/rbac.tsx) is the
 * presentation primitive for a withheld value once one exists; its own
 * comment names B-EP03.4 as the wire change this grid is waiting on. Until
 * then every empty row here reads as absent, which is the only fact this
 * grid can currently tell.
 */
export function DetailsGrid({
  organization,
}: Readonly<{ organization?: Organization }>) {
  if (!organization) {
    return null;
  }
  // Split into its own component (rather than returning early above and
  // calling the hooks below unconditionally) so every hook in this file runs
  // on every render of THIS component and stays absent entirely on the
  // no-organization one — an early return between hook calls fails the Rules
  // of Hooks the moment `organization` flips between defined and not, which a
  // 360 read that answers slower than the shell mount does routinely.
  return <DetailsGridBody organization={organization} />;
}

// The four props every DetailsGrid row needs off the record: the value to
// read, the verb to write it back, and the two reasons that verb might not
// be offered. One shape rather than each row re-deriving it from
// `organization` keeps the RBAC/read-only wiring in DetailsGridBody's single
// pair of hook calls, not scattered across nine row components each running
// its own.
type DetailsRowProps = Readonly<{
  organization: Organization;
  canEdit: boolean;
  readOnlyReason: string | undefined;
  patch: (body: UpdateOrganizationRequest) => Promise<void>;
}>;

function LegalNameRow({
  organization,
  canEdit,
  readOnlyReason,
  patch,
}: DetailsRowProps) {
  const t = useT();
  return (
    <FieldRow label={t("create.legalName")}>
      <InlineText
        label={t("create.legalName")}
        value={organization.legal_name ?? ""}
        placeholder={t("field.addLegalName")}
        canEdit={canEdit}
        readOnlyReason={readOnlyReason}
        onSave={(next) => patch({ legal_name: next || null })}
      />
    </FieldRow>
  );
}

// Lifecycle and owner both reuse the header's OWN control rather than a
// second InlineChoice wired to the same field — see the docblock above.
// `hideLabel` leaves the visible label to FieldGrid's own label column, the
// same way SizeBandRow below suppresses InlineChoice's own prefix.
function LifecycleRow({
  organization,
}: Readonly<{ organization: Organization }>) {
  const t = useT();
  return (
    // The badge is a box, not a line of text, so it centres against its label
    // rather than sharing the row's top edge with it.
    <FieldRow label={t("org.lifecycle")} align="middle">
      <CompanyLifecycleControl org={organization} />
    </FieldRow>
  );
}

function OwnerRow({ organization }: Readonly<{ organization: Organization }>) {
  const t = useT();
  return (
    <FieldRow label={t("co.pulse.owner")}>
      <CompanyOwnerControl org={organization} hideLabel />
    </FieldRow>
  );
}

// The account's current primary domain — same fallback the header's own read
// paths use (flagged primary, else the first row): a record with no domain
// flagged primary still has to name ONE entry as "the" domain this row edits.
function primaryDomainOf(
  domains: readonly OrganizationDomain[],
): OrganizationDomain | undefined {
  return domains.find((domain) => domain.is_primary) ?? domains[0];
}

// Renames the primary domain in place, sending the FULL set back with every
// other entry untouched — see the docblock above for why `domains` cannot
// take a single-entry write. Clearing the field is refused rather than
// treated as a delete: this row renames, it does not remove, and a removal
// here would drop an entry with no confirmation and no way to reconsider.
function DomainRow({
  organization,
  canEdit,
  readOnlyReason,
  patch,
}: DetailsRowProps) {
  const t = useT();
  const domains = organization.domains ?? [];
  const primary = primaryDomainOf(domains);
  return (
    <FieldRow label={t("field.domain")}>
      <InlineText
        label={t("field.domain")}
        value={primary?.domain ?? ""}
        placeholder={t("field.addDomain")}
        canEdit={canEdit}
        readOnlyReason={readOnlyReason}
        onSave={(next) => {
          if (!next) {
            throw new Error(t("field.domainRequired"));
          }
          const rest = domains.filter((domain) => domain !== primary);
          return patch({
            domains: [...rest, { domain: next, is_primary: true }],
          });
        }}
      />
    </FieldRow>
  );
}

// The six address parts this grid draws, in the order the create form's own
// `ADDRESS_FIELDS` shows them. Label and placeholder are separate keys per
// part: a label NAMES the fact and an empty row INVITES one, and reusing the
// label as the placeholder made every unfilled row read as a second, quieter
// copy of its own label rather than as something to press. Country is the one
// row whose label is not the create form's own — the form's carries the
// ISO-3166 hint inline ("Country (ISO-3166, e.g. DE)"), which is guidance for
// someone typing, not the name of the field; here the hint sits in the
// placeholder, where the person about to type is the only one who reads it.
// `normalize` is likewise only non-trivial for country: ISO-3166 alpha-2,
// canonicalized the same way the full editor's own `addressPatch` does — the
// server compares on the uppercase spelling, so "de" typed here and "DE"
// typed in the edit modal read as the same value.
const ADDRESS_PARTS: ReadonlyArray<{
  part: AddressPart;
  labelKey: MessageKey;
  placeholderKey: MessageKey;
  normalize?: (next: string) => string;
}> = [
  {
    part: "line1",
    labelKey: "create.addressLine1",
    placeholderKey: "field.addAddressLine1",
  },
  {
    part: "line2",
    labelKey: "create.addressLine2",
    placeholderKey: "field.addAddressLine2",
  },
  {
    part: "postal_code",
    labelKey: "create.postalCode",
    placeholderKey: "field.addPostalCode",
  },
  { part: "city", labelKey: "create.city", placeholderKey: "field.addCity" },
  {
    part: "region",
    labelKey: "create.region",
    placeholderKey: "field.addRegion",
  },
  {
    part: "country",
    labelKey: "field.country",
    placeholderKey: "field.addCountry",
    normalize: (next) => next.toUpperCase(),
  },
];

// One InlineText per address part, each sending the WHOLE object back with
// only its own part changed — see the docblock above for why a part cannot
// PATCH alone.
function AddressPartRow({
  organization,
  canEdit,
  readOnlyReason,
  patch,
  part,
  labelKey,
  placeholderKey,
  normalize = (next) => next,
}: DetailsRowProps & {
  part: AddressPart;
  labelKey: MessageKey;
  placeholderKey: MessageKey;
  normalize?: (next: string) => string;
}) {
  const t = useT();
  return (
    <FieldRow label={t(labelKey)}>
      <InlineText
        label={t(labelKey)}
        value={organization.address?.[part] ?? ""}
        placeholder={t(placeholderKey)}
        canEdit={canEdit}
        readOnlyReason={readOnlyReason}
        onSave={(next) =>
          patch({
            address: {
              ...organization.address,
              [part]: normalize(next) || null,
            },
          })
        }
      />
    </FieldRow>
  );
}

function IndustryRow({
  organization,
  canEdit,
  readOnlyReason,
  patch,
}: DetailsRowProps) {
  const t = useT();
  return (
    <FieldRow label={t("create.industry")}>
      <InlineText
        label={t("create.industry")}
        value={organization.industry ?? ""}
        placeholder={t("field.addIndustry")}
        canEdit={canEdit}
        readOnlyReason={readOnlyReason}
        onSave={(next) => patch({ industry: next || null })}
      />
    </FieldRow>
  );
}

function SizeBandRow({
  organization,
  canEdit,
  readOnlyReason,
  patch,
}: DetailsRowProps) {
  const t = useT();
  return (
    <FieldRow label={t("create.sizeBand")}>
      <InlineChoice
        label={t("create.sizeBand")}
        hideLabel
        value={organization.size_band ?? ""}
        options={SIZE_BAND_OPTIONS.map((band) => ({
          value: band,
          label: band,
        }))}
        canEdit={canEdit}
        readOnlyReason={readOnlyReason}
        render={(value) => value || t("field.unset")}
        onSave={(next) =>
          patch({
            size_band: (next || null) as UpdateOrganizationRequest["size_band"],
          })
        }
      />
    </FieldRow>
  );
}

// Always InlineText, whether or not the account has a URL yet: the header's
// own LinkedIn chip (companyheader.tsx) already gives a reader the clickable
// link once one is set, so this row's job is writing the value, not a second
// place to click through to it.
function LinkedinRow({
  organization,
  canEdit,
  readOnlyReason,
  patch,
}: DetailsRowProps) {
  const t = useT();
  return (
    <FieldRow label={t("create.linkedinUrl")}>
      <InlineText
        label={t("create.linkedinUrl")}
        value={organization.linkedin_url ?? ""}
        placeholder={t("field.addLinkedinUrl")}
        canEdit={canEdit}
        readOnlyReason={readOnlyReason}
        onSave={(next) => patch({ linkedin_url: next || null })}
      />
    </FieldRow>
  );
}

function DescriptionRow({
  organization,
  canEdit,
  readOnlyReason,
  patch,
}: DetailsRowProps) {
  const t = useT();
  return (
    <FieldRow label={t("co.description.label")}>
      <InlineText
        label={t("co.description.label")}
        value={organization.description ?? ""}
        placeholder={t("co.description.placeholder")}
        maxLength={DESCRIPTION_MAX_LENGTH}
        canEdit={canEdit}
        readOnlyReason={readOnlyReason}
        onSave={(next) => patch({ description: next || null })}
      />
    </FieldRow>
  );
}

function DetailsGridBody({
  organization,
}: Readonly<{ organization: Organization }>) {
  const canUpdate = useCan("organization", "update");
  const readOnlyReason = useCompanyReadOnlyReason(organization);
  const patch = useCompanyFieldPatch(organization);
  const row: DetailsRowProps = {
    organization,
    canEdit: canUpdate && !readOnlyReason,
    readOnlyReason,
    patch,
  };
  return (
    <FieldGrid>
      <LegalNameRow {...row} />
      <OwnerRow organization={organization} />
      <LifecycleRow organization={organization} />
      <DomainRow {...row} />
      {ADDRESS_PARTS.map((field) => (
        <AddressPartRow key={field.part} {...row} {...field} />
      ))}
      <IndustryRow {...row} />
      <SizeBandRow {...row} />
      <LinkedinRow {...row} />
      <DescriptionRow {...row} />
    </FieldGrid>
  );
}

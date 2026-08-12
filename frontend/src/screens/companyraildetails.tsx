// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { components } from "../api/schema";
import { useCan } from "../app/capability";
import { FieldGrid, FieldRow } from "../design-system/fieldgrid";
import { InlineChoice, InlineText } from "../design-system/inlinechoice";
import { useT } from "../i18n";
import {
  CompanyOwnerControl,
  useCompanyFieldPatch,
  useCompanyReadOnlyReason,
} from "./companyheader";
import { LIFECYCLE_LABELS, SIZE_BAND_OPTIONS } from "./companylookups";

// The rail's own Details grid (companyrail.tsx's DetailsGrid), split into
// this file so the rail file stays under the 500-line ceiling: one panel
// section (Details) and its nine field rows is a natural seam, not an
// arbitrary cut.

type Organization = components["schemas"]["Organization"];
type Lifecycle = NonNullable<Organization["lifecycle"]>;
type UpdateOrganizationRequest =
  components["schemas"]["UpdateOrganizationRequest"];

// The column's own CHECK bound (core 0203) — stops the reader at the limit
// rather than letting the server refuse the save. Same figure companyheader.tsx
// used to cap its own (now-removed) description control at.
const DESCRIPTION_MAX_LENGTH = 500;

/**
 * DetailsGrid draws the account's own fields as a label/value grid: legal
 * name, where it stands with us, who owns it, its primary domain, address,
 * industry, size, LinkedIn page and description. EVERY known field draws a
 * row, whether or not the account carries a value: an absent field is a fact
 * about the record (nobody has filled it in yet), and hiding the row along
 * with the fact erases the "yet" — a reader can only add what they can see is
 * missing. An empty row reads as a quiet add affordance (InlineText/
 * InlineChoice's own empty-state button) rather than a blank line.
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
 * Lifecycle, domain and address stay read-only here. Lifecycle already has
 * its own control in the header (`CompanyLifecycleControl`) — a second
 * editable lifecycle picker here PATCHes the same field through a second
 * path, and a page with two "Change Account lifecycle" controls is a page
 * offering the reader two different ways to do the one thing, wired to two
 * independent bits of local edit state. The header is where a reader SETS
 * lifecycle; this grid shows the value the header owns rather than a second
 * way to write it. Owner is the one exception: it reuses the header's own
 * `CompanyOwnerControl` (roster read, not-in-roster fallback,
 * unowned-only-while-unowned rule, all shared) rather than duplicating that
 * logic, so the grid and the header write owner through the identical
 * control — two mount points, one implementation, not two independent ones.
 * Domain and address are not scalar either, for an unrelated reason:
 * `domains` is a replace-set on the wire (an edit here that wrote only the
 * primary domain back would silently drop every other one) and `address` is
 * a multi-field object no single InlineText round-trips. Both need a
 * purpose-built editor this grid does not attempt.
 *
 * ABSENT VS WITHHELD, stated rather than built: this grid does not today
 * distinguish a field nobody has filled in from one the viewer's role cannot
 * see, because `Organization` carries no field-level grant signal to draw
 * that distinction from — only `computed_fields` does (STATE-4), and it is
 * not one of these nine fields. `FieldGuard` (design-system/rbac.tsx) is the
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

// Lifecycle, domain and address stay read-only here — see the docblock above
// for why each one does. Grouped in one component (rather than each getting
// its own row function like the editable fields) because none of them needs
// `DetailsRowProps`' write-side props: no `canEdit`, no `readOnlyReason`, no
// `patch`, just the record to read from.
function ReadOnlyFactRows({
  organization,
}: Readonly<{ organization: Organization }>) {
  const t = useT();
  const primaryDomain =
    organization.domains?.find((domain) => domain.is_primary)?.domain ??
    organization.domains?.[0]?.domain;
  const location = [organization.address?.city, organization.address?.country]
    .filter(Boolean)
    .join(", ");
  return (
    <>
      <FieldRow label={t("org.lifecycle")}>
        {t(
          LIFECYCLE_LABELS[(organization.lifecycle ?? "unknown") as Lifecycle],
        )}
      </FieldRow>
      <FieldRow label={t("field.domain")}>
        {primaryDomain ?? t("field.unset")}
      </FieldRow>
      <FieldRow label={t("co.details.address")}>
        {location || t("field.unset")}
      </FieldRow>
    </>
  );
}

// Owner reuses the header's own `CompanyOwnerControl` rather than a second
// InlineChoice wired to the same roster — see the docblock above. `hideLabel`
// leaves the visible "Owner" label to FieldGrid's own label column, the same
// way SizeBandRow below suppresses InlineChoice's own prefix.
function OwnerRow({ organization }: Readonly<{ organization: Organization }>) {
  const t = useT();
  return (
    <FieldRow label={t("co.pulse.owner")}>
      <CompanyOwnerControl org={organization} hideLabel />
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
      <ReadOnlyFactRows organization={organization} />
      <IndustryRow {...row} />
      <SizeBandRow {...row} />
      <LinkedinRow {...row} />
      <DescriptionRow {...row} />
    </FieldGrid>
  );
}

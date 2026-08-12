// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { components } from "../api/schema";
import { useCan } from "../app/capability";
import { FieldGrid, FieldRow } from "../design-system/fieldgrid";
import { InlineChoice, InlineText } from "../design-system/inlinechoice";
import { useT } from "../i18n";
import {
  useCompanyFieldPatch,
  useCompanyReadOnlyReason,
} from "./companyheader";
import {
  LIFECYCLE_LABELS,
  LIFECYCLE_OPTIONS,
  SIZE_BAND_OPTIONS,
} from "./companylookups";
import { EntityRef } from "./entityref";

// The rail's own Details grid (companyrail.tsx's DetailsGrid), split into
// this file so the rail file stays under the 500-line ceiling: one panel
// section (Details) and its nine field rows is a natural seam, not an
// arbitrary cut.

type Organization = components["schemas"]["Organization"];
type Lifecycle = NonNullable<Organization["lifecycle"]>;
type UpdateOrganizationRequest =
  components["schemas"]["UpdateOrganizationRequest"];

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
 * Owner, domain and address stay read-only here. Owner already has its own
 * roster-backed control in the header (`CompanyOwnerControl`) — repeating a
 * SECOND owner picker in the same page, wired to the same roster fetch, is
 * the duplication the header file exists to own once. Domain and address are
 * not scalar: `domains` is a replace-set on the wire (an edit here that wrote
 * only the primary domain back would silently drop every other one) and
 * `address` is a multi-field object no single InlineText round-trips. Both
 * need a purpose-built editor this grid does not attempt.
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

function LifecycleRow({
  organization,
  canEdit,
  readOnlyReason,
  patch,
}: DetailsRowProps) {
  const t = useT();
  return (
    <FieldRow label={t("org.lifecycle")}>
      <InlineChoice
        label={t("org.lifecycle")}
        hideLabel
        value={organization.lifecycle ?? "unknown"}
        options={LIFECYCLE_OPTIONS.map((value) => ({
          value,
          label: t(LIFECYCLE_LABELS[value]),
        }))}
        canEdit={canEdit}
        readOnlyReason={readOnlyReason}
        render={(value) => t(LIFECYCLE_LABELS[value as Lifecycle])}
        onSave={(next) =>
          patch({
            lifecycle: next as NonNullable<
              UpdateOrganizationRequest["lifecycle"]
            >,
          })
        }
      />
    </FieldRow>
  );
}

// Owner, domain and address stay read-only here. Owner already has its own
// roster-backed control in the header (`CompanyOwnerControl`) — repeating a
// SECOND owner picker in the same page, wired to the same roster fetch, is
// the duplication the header file exists to own once. Domain and address are
// not scalar: `domains` is a replace-set on the wire (an edit here that wrote
// only the primary domain back would silently drop every other one) and
// `address` is a multi-field object no single InlineText round-trips. Both
// need a purpose-built editor this grid does not attempt.
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
      <FieldRow label={t("co.pulse.owner")}>
        {organization.owner_id ? (
          <EntityRef kind="user" id={organization.owner_id} />
        ) : (
          t("co.pulse.unowned")
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

function LinkedinRow({
  organization,
  canEdit,
  readOnlyReason,
  patch,
}: DetailsRowProps) {
  const t = useT();
  return (
    <FieldRow label={t("create.linkedinUrl")}>
      {organization.linkedin_url ? (
        <a href={organization.linkedin_url} target="_blank" rel="noreferrer">
          {t("co.chip.linkedin")}
        </a>
      ) : (
        <InlineText
          label={t("create.linkedinUrl")}
          value=""
          placeholder={t("field.addLinkedinUrl")}
          canEdit={canEdit}
          readOnlyReason={readOnlyReason}
          onSave={(next) => patch({ linkedin_url: next || null })}
        />
      )}
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
      <LifecycleRow {...row} />
      <ReadOnlyFactRows organization={organization} />
      <IndustryRow {...row} />
      <SizeBandRow {...row} />
      <LinkedinRow {...row} />
      <DescriptionRow {...row} />
    </FieldGrid>
  );
}

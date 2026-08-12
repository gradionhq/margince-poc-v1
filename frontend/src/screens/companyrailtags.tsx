// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { components } from "../api/schema";
import { Badge, Disclosure } from "../design-system/atoms";
import { PanelBody } from "../design-system/panel";
import { useT } from "../i18n";
import { SectionPart, sectionState } from "./company360";
import { ListAction, TagAction } from "./companyactions";
import { useCompanyReadOnlyReason } from "./companyheader";
import { SectionSummary, sectionAnswered } from "./companyrailshared";

// TagsSection (companyrail.tsx's own section, split into this file so the
// rail stays under the 500-line ceiling): the lists this account belongs to
// and the tags applied to it, plus the verbs that write them.

type Organization360 = components["schemas"]["Organization360"];
type Organization = components["schemas"]["Organization"];

/**
 * TagsSection carries two independently-governed halves: the lists this
 * account belongs to, and the tags applied to it. Each states its own
 * withheld/empty/ready rather than one verdict speaking for both.
 *
 * The add-tag / add-to-list verbs live HERE, one under each half, rather
 * than in a separate strip elsewhere on the page (CompanyFilingActions,
 * organizations.tsx — retired once this landed): reading and writing the
 * same fact from two different columns is the confusion this section exists
 * to close. Each verb keeps the gate the old strip enforced — rendered only
 * once its OWN half has answered (ready or empty) — so a caller who cannot
 * read tags is never offered a button to add one, and a half that failed to
 * load cannot say whether the write makes sense.
 */
export function TagsSection({
  view,
  orgId,
}: Readonly<{ view?: Organization360; orgId: string }>) {
  const t = useT();
  const tags = view?.tags ?? [];
  const lists = view?.list_memberships ?? [];
  const listState = sectionState(
    view,
    "list_memberships",
    Boolean(view?.list_memberships),
    lists.length,
  );
  const tagState = sectionState(view, "tags", Boolean(view?.tags), tags.length);
  const listsAnswered = sectionAnswered(listState);
  const tagsAnswered = sectionAnswered(tagState);
  // Absent, not zero, until at least one half has actually answered: two
  // withheld halves must not read as a confirmed empty account.
  const count =
    listsAnswered || tagsAnswered
      ? (listsAnswered ? lists.length : 0) + (tagsAnswered ? tags.length : 0)
      : undefined;
  // Verbs, not values: an archived account still SHOWS its tags and lists
  // above, it just does not offer to change them. `useCompanyReadOnlyReason`
  // needs a resolved Organization, so this reads it off `TagsSectionVerb`
  // below, a component mounted only once `view.organization` exists —
  // keeping that hook call unconditional (Rules of Hooks) without
  // fabricating a stand-in record just to satisfy its signature while the
  // 360 read is still in flight.
  return (
    <Disclosure
      className="co-sect"
      summary={<SectionSummary title={t("co.tags.title")} count={count} />}
    >
      <PanelBody>
        <SectionPart
          label={t("co.tags.lists")}
          state={listState}
          emptyLabel={t("co.tags.noLists")}
        >
          <p className="co-row-meta">
            {lists.map((list) => (
              <Badge key={list.id} tone="accent">
                {list.name}
              </Badge>
            ))}
          </p>
        </SectionPart>
        {listsAnswered && view?.organization && (
          <TagsSectionVerb
            organization={view.organization}
            orgId={orgId}
            action="list"
          />
        )}
        <SectionPart
          label={t("co.tags.tags")}
          state={tagState}
          emptyLabel={t("co.tags.noTags")}
        >
          <p className="co-row-meta">
            {tags.map((tag) => (
              <Badge key={tag.id}>{tag.name}</Badge>
            ))}
          </p>
        </SectionPart>
        {tagsAnswered && view?.organization && (
          <TagsSectionVerb
            organization={view.organization}
            orgId={orgId}
            action="tag"
          />
        )}
      </PanelBody>
    </Disclosure>
  );
}

// The add-list / add-tag verb, gated on writability. Split out so
// `useCompanyReadOnlyReason` — which needs a resolved Organization — is only
// ever called once one exists (the caller above renders this component only
// when `view.organization` is set), rather than called conditionally inside
// TagsSection itself or fed a stand-in record just to satisfy its signature.
function TagsSectionVerb({
  organization,
  orgId,
  action,
}: Readonly<{
  organization: Organization;
  orgId: string;
  action: "list" | "tag";
}>) {
  const readOnlyReason = useCompanyReadOnlyReason(organization);
  if (readOnlyReason) {
    return null;
  }
  return (
    <div className="co-card-actions">
      {action === "list" ? (
        <ListAction orgId={orgId} />
      ) : (
        <TagAction orgId={orgId} />
      )}
    </div>
  );
}

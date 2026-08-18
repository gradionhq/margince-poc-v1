import type { components } from "../api/schema";
import { EvidenceMark } from "../design-system/evidencemark";
import { Panel, PanelBody, PanelRow } from "../design-system/panel";
import { SurfaceState, sectionState } from "../design-system/surfacestate";
import { ProvenanceTag } from "../design-system/trust";
import { formatDateAbbrev } from "../format/format";
import { useLocale, useT } from "../i18n";
import { provenanceOf } from "./common";
import { RECORD_ZONE } from "./company360";
import { confidenceLevel } from "./inbox";
import { omitted } from "./person360";
import { PersonProviderSection } from "./personprovider";
import "./person360.css";

// The Research tab (a PURE READ of the 360, like its siblings in
// persontabs.tsx): what a machine read about this person, kept beside the
// canonical record rather than folded into it. Two different kinds of
// research sit here — a bought provider snapshot and the enrichment evidence
// this app's own capture read off a page or a signature — and each carries
// its own receipt, because a value the reader cannot check is a claim, not a
// fact.

type Person360 = components["schemas"]["Person360"];
type ProfileField = components["schemas"]["PersonProfileField"];

export function PersonResearchTab({
  view,
  loading = false,
}: Readonly<{ view?: Person360; loading?: boolean }>) {
  const t = useT();
  const rows = view?.profile_fields ?? [];
  const fieldsState = sectionState(
    view,
    "profile_fields",
    Boolean(view?.profile_fields),
    rows.length,
    loading,
  );
  const providerWithheld = view ? omitted(view, "provider_profile") : false;
  // PersonProviderSection itself renders nothing once a profile is absent
  // (no grant issue — the caller HAS the grant, there is simply no provider
  // behind it), so "nothing to show" for the provider half is exactly this:
  // present, not withheld, and no profile.
  const providerHasNothing =
    view != null && !providerWithheld && !view.provider_profile;

  // The tab-wide empty state: neither half has anything to say about this
  // person. Two stacked empty panels would say the same "nothing here" twice;
  // this says it once.
  if (view && providerHasNothing && fieldsState === "empty") {
    return (
      <Panel title={t("person.tab.research")}>
        <PanelBody>
          <SurfaceState state="empty" emptyLabel={t("person.research.empty")}>
            {null}
          </SurfaceState>
        </PanelBody>
      </Panel>
    );
  }

  return (
    <div className="pe-overview-stack">
      {view &&
        (providerWithheld ? (
          <Panel title={t("provider.profile.title")}>
            <PanelBody>
              <SurfaceState
                state="withheld"
                emptyLabel={t("person.research.empty")}
              >
                {null}
              </SurfaceState>
            </PanelBody>
          </Panel>
        ) : (
          <PersonProviderSection
            personId={view.person.id}
            profile={view.provider_profile}
          />
        ))}
      <Panel title={t("person.research.fields")}>
        <PanelBody>
          <SurfaceState
            state={fieldsState}
            emptyLabel={t("person.research.fieldsEmpty")}
          >
            {rows.map((field) => (
              <ProfileFieldRow
                key={field.claim_key ?? `${field.field}:${field.captured_at}`}
                field={field}
              />
            ))}
          </SurfaceState>
        </PanelBody>
      </Panel>
    </div>
  );
}

/**
 * One enrichment-evidence row: the field's label, the value it holds with its
 * provenance mark, and who captured it. The label reuses the lookup the
 * correction card already carries (person.enriched.field.*) rather than a
 * second spelling of the same five field names.
 */
function ProfileFieldRow({ field }: Readonly<{ field: ProfileField }>) {
  const t = useT();
  const { locale } = useLocale();
  const provenance = provenanceOf(field.captured_by);
  return (
    <PanelRow className="pe-row">
      <span className="pe-row-label">
        {t(`person.enriched.field.${field.field}`)}
      </span>
      <span className="pe-row-value">
        <EvidenceMark
          value={field.value}
          source={{
            provenance,
            confidence: confidenceLevel(field.confidence) ?? undefined,
            snippet: field.evidence_snippet,
            at: formatDateAbbrev(field.captured_at, locale, RECORD_ZONE),
          }}
        />
      </span>
      <span className="pe-row-label">
        {t("person.research.capturedBy")}:{" "}
        <ProvenanceTag provenance={provenance} />
      </span>
    </PanelRow>
  );
}

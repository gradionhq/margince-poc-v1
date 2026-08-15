import { CalendarClock, TriangleAlert } from "lucide-react";
import type { components } from "../api/schema";
import { Card } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { formatDateAbbrev } from "../format/format";
import { useLocale, useT } from "../i18n";
import "./licenseholder.css";
import { RECORD_ZONE } from "./company360";

// Who holds the license, and how long it lasts. The card above the seat meter:
// two subjects, two cards — who this license belongs to, then what it grants.
//
// Every claim except the identifiers and the expiry is optional. A license
// issued before those claims existed verifies exactly like any other, so each
// row renders only when the token carries it. Empty rows would tell a reader
// that something is missing from THEIR license rather than from the vocabulary
// it was issued under.
//
// Two states interrupt, and they are ordered against the seat warning below by
// what they cost. Expiry stops the installation eventually; being over the seat
// count never stops anything. So the expiry notice lives here, above.

type LicenseHolder = components["schemas"]["LicenseHolder"];

export function LicenseHolderCard({
  holder,
}: Readonly<{ holder: LicenseHolder }>) {
  const t = useT();
  const { locale } = useLocale();
  // RECORD_ZONE is the one zone this product renders dates in, so an expiry and
  // an activity beside it can never be read in two different zones.
  const expiry = formatDateAbbrev(holder.expiry, locale, RECORD_ZONE);

  return (
    <Card className="card-stack" title={t("license.holder.title")}>
      {holder.in_grace ? (
        // The license stopped being current and still works. This is the one
        // state upstream calls out: it passes today and will stop passing.
        <Callout
          tone="danger"
          live="alert"
          icon={TriangleAlert}
          title={t("license.grace.title")}
        >
          {t("license.grace.body", { expiry })}
        </Callout>
      ) : (
        holder.renewal_due && (
          // Inside the warning window. Amber, and not `alert`: nothing has gone
          // wrong yet, and a renewal is a thing to plan rather than to fix now.
          <Callout
            tone="warn"
            icon={CalendarClock}
            title={t("license.renewal.title")}
          >
            {t("license.renewal.body", { expiry })}
          </Callout>
        )
      )}
      <dl className="license-holder">
        {holder.org && (
          <div>
            <dt>{t("license.holder.org")}</dt>
            <dd>{holder.org}</dd>
          </div>
        )}
        {(holder.contact_name || holder.contact_email) && (
          <div>
            <dt>{t("license.holder.contact")}</dt>
            <dd>
              {holder.contact_name}
              {holder.contact_name && holder.contact_email && " · "}
              {holder.contact_email}
            </dd>
          </div>
        )}
        <div>
          <dt>{t("license.holder.installation")}</dt>
          <dd>{holder.subject}</dd>
        </div>
        <div>
          <dt>{t("license.holder.validUntil")}</dt>
          <dd>{expiry}</dd>
        </div>
        <div>
          <dt>{t("license.holder.id")}</dt>
          {/* The support reference. Monospace because somebody reads it aloud
              or copies it into a ticket, and a proportional font turns a
              character into a guess. */}
          <dd className="license-id">{holder.id}</dd>
        </div>
      </dl>
    </Card>
  );
}

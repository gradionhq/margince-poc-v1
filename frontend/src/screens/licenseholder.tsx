import { CalendarClock, TriangleAlert } from "lucide-react";
import type { components } from "../api/schema";
import { Callout } from "../design-system/callout";
import { Panel, PanelBody } from "../design-system/panel";
import { SettingList, SettingRow } from "../design-system/settingrow";
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
    <Panel title={t("license.holder.title")}>
      <PanelBody>
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
        {/* Each claim is a fact the license asserts, so it reads as a row: the
            claim on the left and its value on the right, at the one x every
            other answer on these pages sits at. It was a two-column `<dl>` with
            its own grid and its own gaps — a third layout for the shape
            `SettingRow` is. `control={null}` because there is nothing to press:
            a license is changed by changing the deployment.

            Every claim except the identifiers and the expiry is optional. A
            license issued before those claims existed verifies exactly like any
            other, so each row renders only when the token carries it — an empty
            row would say something is missing from THIS license rather than from
            the vocabulary it was issued under. */}
        <SettingList>
          {holder.org && (
            <SettingRow
              label={t("license.holder.org")}
              value={holder.org}
              control={null}
            />
          )}
          {(holder.contact_name || holder.contact_email) && (
            <SettingRow
              label={t("license.holder.contact")}
              value={
                <>
                  {holder.contact_name}
                  {holder.contact_name && holder.contact_email && " · "}
                  {holder.contact_email}
                </>
              }
              control={null}
            />
          )}
          <SettingRow
            label={t("license.holder.installation")}
            value={holder.subject}
            control={null}
          />
          <SettingRow
            // The label follows the fact. "Valid until" beside a date that has
            // already passed states the opposite of what the notice above says.
            label={
              holder.in_grace
                ? t("license.holder.expiredOn")
                : t("license.holder.validUntil")
            }
            value={expiry}
            control={null}
          />
          <SettingRow
            label={t("license.holder.id")}
            // The support reference. Monospace because somebody reads it aloud
            // or copies it into a ticket, and a proportional font turns a
            // character into a guess.
            value={<span className="license-id">{holder.id}</span>}
            control={null}
          />
        </SettingList>
      </PanelBody>
    </Panel>
  );
}

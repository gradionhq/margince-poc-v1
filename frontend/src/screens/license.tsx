import { useQuery } from "@tanstack/react-query";
import { TriangleAlert } from "lucide-react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { StatCard } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Panel, PanelBody } from "../design-system/panel";
import { Meter } from "../design-system/readings";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { StatStrip } from "../design-system/statstrip";
import { useT } from "../i18n";
import { QueryGate, throwProblem } from "./common";
import { LicenseHolderCard } from "./licenseholder";

// The entitlement surface: what the license grants, and how many seats are using
// it. Read-only, because there is nothing here to write — the token is resolved
// from the deployment file at boot, so an operator changes their entitlement by
// changing the deployment, not by typing into a form.
//
// Three states the server distinguishes, and the reading has to as well:
//
//   valid, with a seat count   the strip reads used beside granted, then a meter
//   valid, with no seat count  a license that caps nothing: a count, no meter
//   absent                     no license configured; nothing to measure against
//
// The middle case is why `seats_granted` is nullable rather than zero, and why
// the granted slot says "no limit" instead of a number: a meter filled against a
// limit nobody set would invent the limit.
//
// A strip rather than two cards, because the two numbers are ONE comparison —
// used against granted is the whole question this screen answers, and cards are
// read one at a time. It sits in one stacked `SettingRow`, for the same reason:
// a row per figure would split the comparison the strip exists to make.
//
// Over the limit is REPORTED, never enforced. The workspace keeps working — P7's
// warning-then-grace, not a silent mid-month lockout — so the notice says what is
// true and what to do, and nothing on this screen blocks anybody.

type LicenseEntitlement = components["schemas"]["LicenseEntitlement"];

/**
 * What the license grants and how much of it is used.
 *
 * Exported because the shell's own foot reports the same posture (app/shell.tsx)
 * — one query key, so the strip and this screen can never disagree about how many
 * seats are in use, and the second reader pays no second request.
 */
export function useLicenseEntitlement(enabled = true) {
  return useQuery({
    queryKey: ["installation-license"],
    // A principal without `license:read` would get a 403 on every route. The
    // caller that knows the grant passes it, and the request is never made.
    enabled,
    // The posture changes when somebody is invited or a license is replaced, not
    // between two page opens. The chrome reads it on every route, so a short
    // staleTime would put a request behind every navigation.
    staleTime: 5 * 60_000,
    queryFn: async () => {
      const { data, error, response } = await api.GET("/installation/license");
      if (error || !response.ok) {
        throwProblem(error);
      }
      return data;
    },
  });
}

export function LicenseCard() {
  const query = useLicenseEntitlement();
  return (
    <QueryGate query={query}>
      {(entitlement) => (
        <>
          {/* Only a verified license has a holder. An unlicensed installation
              has nobody to name, and a refused one proved nothing about who
              holds it — so the card is absent rather than empty. */}
          {entitlement.license && (
            <LicenseHolderCard holder={entitlement.license} />
          )}
          <LicenseReading entitlement={entitlement} />
        </>
      )}
    </QueryGate>
  );
}

// Exported for its story: the states worth looking at are states of the READING,
// and a story that had to stub a query to reach them would be testing the fetch.
export function LicenseReading({
  entitlement,
}: Readonly<{ entitlement: LicenseEntitlement }>) {
  const t = useT();
  const granted = entitlement.seats_granted;
  const capped = granted !== undefined && granted !== null;

  return (
    // A `Panel`, like every other card on every other settings page. This card
    // was the last `Card` on the surface, and a `Card` draws its title INSIDE
    // the padded body with no band and no rule — so on a page of panels it read
    // as a different kind of object rather than as one more card.
    <Panel title={t("license.card.title")}>
      <PanelBody>
        {/* The state is said in words rather than as a coloured pill alone: "no
            license" and "licensed" are different facts about the installation,
            and a reader should not have to learn a colour to tell them apart. */}
        <p className="settings-panel-sub">
          {capped
            ? t("license.state.licensed")
            : entitlement.state === "valid"
              ? t("license.state.uncapped")
              : t("license.state.unlicensed")}
        </p>
        {entitlement.over_limit && (
          // `alert` interrupts, which is right here and nowhere else on this
          // screen: the installation is past what it is entitled to, and that is a
          // thing the admin has to act on rather than notice eventually.
          <Callout
            tone="danger"
            live="alert"
            icon={TriangleAlert}
            title={t("license.over.title")}
          >
            {t("license.over.body", {
              used: String(entitlement.seats_used),
              granted: String(granted),
            })}
          </Callout>
        )}
        <SettingList>
          {/* ONE row, stacked. The two figures and the bar under them are the
            card's SUBJECT rather than an answer that fits in a right-hand
            column (design-system README, `SettingList` / `SettingRow`): used
            against granted is the whole question this screen answers, and
            splitting it into two rows would make it two readings. What counts
            as a seat rides as the row's description — it is the rule the
            figures are drawn under, which is exactly what a description is
            for, and it used to sit at the foot of the card where a reader met
            it after taking the numbers at face value. */}
          <SettingRow
            label={t("license.seats.title")}
            description={t("license.counting")}
            layout="stack"
            control={
              <div className="form-stack">
                <StatStrip>
                  <StatCard
                    label={t("license.seats.used")}
                    value={String(entitlement.seats_used)}
                    // The slot itself is the bad news when the count is past the
                    // grant, so `alert` rather than `tone`, which would only
                    // colour the figure.
                    alert={entitlement.over_limit}
                  />
                  <StatCard
                    label={t("license.seats.granted")}
                    // Absent, not zero, and it says which absence it is: an
                    // unlicensed installation and a license that caps nothing
                    // both have no number here, and only the first is something
                    // an admin might want to change.
                    value={
                      capped ? String(granted) : t("license.seats.uncapped")
                    }
                  />
                </StatStrip>
                {capped && (
                  <Meter
                    value={entitlement.seats_used}
                    max={granted}
                    // A role="meter" takes no accessible name from the slots
                    // beside it, so the reading is named here rather than by the
                    // row's own label.
                    label={t("license.meter.label", {
                      used: String(entitlement.seats_used),
                      granted: String(granted),
                    })}
                  />
                )}
              </div>
            }
          />
        </SettingList>
      </PanelBody>
    </Panel>
  );
}

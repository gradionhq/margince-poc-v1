import { useQuery } from "@tanstack/react-query";
import { TriangleAlert } from "lucide-react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Card } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Meter } from "../design-system/readings";
import { useT } from "../i18n";
import "./license.css";
import { problemMessage, QueryGate } from "./common";

// The entitlement surface: what the license grants, and how many seats are using
// it. Read-only, because there is nothing here to write — the token is resolved
// from the deployment file at boot, so an operator changes their entitlement by
// changing the deployment, not by typing into a form.
//
// Three states the server distinguishes, and the meter has to as well:
//
//   valid, with a seat count   the meter reads used / granted
//   valid, with no seat count  a license that caps nothing: no meter at all
//   absent                     no license configured; nothing to measure against
//
// The middle case is why `seats_granted` is nullable rather than zero, and why
// this screen renders the count on its own instead of a meter against it: a bar
// filled against a limit nobody set would invent the limit.
//
// Over the limit is REPORTED, never enforced. The workspace keeps working — P7's
// warning-then-grace, not a silent mid-month lockout — so the notice says what is
// true and what to do, and nothing on this screen blocks anybody.

type LicenseEntitlement = components["schemas"]["LicenseEntitlement"];

function useLicenseEntitlement() {
  return useQuery({
    queryKey: ["installation-license"],
    queryFn: async () => {
      const { data, error, response } = await api.GET("/installation/license");
      if (error || !response.ok) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
}

export function LicenseCard() {
  const query = useLicenseEntitlement();
  return (
    <QueryGate query={query}>
      {(entitlement) => <LicenseReading entitlement={entitlement} />}
    </QueryGate>
  );
}

function LicenseReading({
  entitlement,
}: Readonly<{ entitlement: LicenseEntitlement }>) {
  const t = useT();
  const granted = entitlement.seats_granted;
  const capped = granted !== undefined && granted !== null;

  return (
    <Card
      className="card-stack"
      title={t("license.card.title")}
      // The state is said in words rather than as a coloured pill alone: "no
      // license" and "licensed" are different facts about the installation, and a
      // reader should not have to learn a colour to tell them apart.
      sub={
        capped
          ? t("license.state.licensed")
          : entitlement.state === "valid"
            ? t("license.state.uncapped")
            : t("license.state.unlicensed")
      }
    >
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
      <dl className="license-reading">
        <div>
          <dt>{t("license.seats.used")}</dt>
          <dd>{entitlement.seats_used}</dd>
        </div>
        {capped ? (
          <div>
            <dt>{t("license.seats.granted")}</dt>
            <dd>{granted}</dd>
          </div>
        ) : (
          // Absent, not zero, and it says which absence it is: an unlicensed
          // installation and a license that caps nothing both have no number
          // here, and only the first is something an admin might want to change.
          <div>
            <dt>{t("license.seats.granted")}</dt>
            <dd>{t("license.seats.uncapped")}</dd>
          </div>
        )}
      </dl>
      {capped && (
        <Meter
          value={entitlement.seats_used}
          max={granted}
          // A role="meter" takes no accessible name from its surroundings, so the
          // reading is named here rather than by the term above it.
          label={t("license.meter.label", {
            used: String(entitlement.seats_used),
            granted: String(granted),
          })}
        />
      )}
      <p className="muted">{t("license.counting")}</p>
    </Card>
  );
}

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCan, useCanWrite } from "../app/capability";
import {
  Badge,
  Button,
  DataTable,
  EmptyState,
  Field,
  Textarea,
  TextInput,
} from "../design-system/atoms";
import { CardBoundary } from "../design-system/cardboundary";
import { ConfirmModal } from "../design-system/confirmmodal";
import { Panel, PanelBody } from "../design-system/panel";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { formatDate } from "../format/format";
import { viewerZone } from "../format/timezone";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { humanizeToken } from "./audit";
import {
  problemMessageOf,
  QueryGate,
  QueryStates,
  throwProblem,
  useMe,
} from "./common";
import "./retention.css";

// Settings → Privacy → Restricted records (A165/ADR-0114 §4): what a
// statutory obligation is holding after an erasure — which record, why, and
// until when — stated without the correspondence itself. The audit log proves
// what happened; this answers what is being held right now, which is the
// question a supervisory authority asks the controller.
//
// It reads through the same authority as the retention ladder above it, so a
// role that may not see how long records are kept may not see which are being
// kept either.

export type RestrictedRecord = components["schemas"]["RestrictedRecord"];

export const RESTRICTED_RECORDS_KEY = ["retention", "restrictions"] as const;

// A pin names its record by id, and the id has to be well-formed BEFORE the
// confirm opens: the dialog behind it warns about an irreversible act on a
// record the controller cannot otherwise see, so letting a typo through means
// reading that warning, typing a reason, and only then learning they named
// nothing. Shape only — whether the record exists is the server's answer.
const RECORD_ID_RE =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

// The obligation's class is the first token of the server's reason
// ("commercial_correspondence · §257 HGB / §147 AO"); the statute after the
// separator is shown as written, because it is the citation and not a label.
function splitReason(reason: string): { cls: string; basis: string } {
  const [cls, ...rest] = reason.split(" · ");
  return { cls, basis: rest.join(" · ") };
}

// The classes the schema admits today, by name. A class this build has not
// heard of renders as its own token rather than a missing key — a newer
// server must not make the list unreadable.
const CLASS_LABEL: Readonly<Record<string, MessageKey>> = {
  commercial_correspondence: "restricted.class.commercialCorrespondence",
};

// The interaction kinds the timeline knows, by name; same fallback.
const KIND_LABEL: Readonly<Record<string, MessageKey>> = {
  email: "restricted.kind.email",
  call: "restricted.kind.call",
  meeting: "restricted.kind.meeting",
  message: "restricted.kind.message",
};

// The two decisions a controller can make about a held record: RELEASE ends
// the obligation by erasing the record — it does not return it to ordinary
// use, because the erasure request it suspended is still outstanding — and
// PIN places a record under the floor the derivation missed. Both are the
// same shape on screen (a typed reason, then an irreversible act), so they
// are one component with the words swapped rather than two.
type Override = "release" | "pin";

// What an override acts on: a row from the list for a release, a record id
// typed in for a pin. A pin is BY DEFINITION about a record this list does not
// hold — the derivation missed it — so there is no row to hang the action off,
// and the id is how a controller names it. They reach it from the audit trail
// or the record page, where the id is on screen.
type OverrideTarget = Readonly<{ activityId: string }>;

function OverrideModal({
  target,
  kind,
  onClose,
}: Readonly<{
  target: OverrideTarget | null;
  kind: Override;
  onClose: () => void;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [reason, setReason] = useState("");

  const decide = useMutation({
    mutationFn: async (stated: string) => {
      if (!target) {
        return;
      }
      const path =
        kind === "release"
          ? ("/retention/restrictions/{activityId}/release" as const)
          : ("/retention/restrictions/{activityId}/pin" as const);
      const { error } = await api.POST(path, {
        params: { path: { activityId: target.activityId } },
        body: { reason: stated },
      });
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: RESTRICTED_RECORDS_KEY });
      setReason("");
      onClose();
    },
  });

  // The reason is what makes this a decision rather than a toggle, so the
  // confirm stays disabled until one is actually typed — the server refuses a
  // blank one, and a button that fires into that refusal teaches nothing.
  return (
    <ConfirmModal
      open={target !== null}
      onClose={() => {
        setReason("");
        onClose();
      }}
      title={t(`restricted.${kind}.title`)}
      confirmLabel={t(`restricted.${kind}.confirm`)}
      confirmVariant="danger"
      confirmDisabled={reason.trim() === ""}
      onConfirm={() => decide.mutate(reason)}
      pending={decide.isPending}
      error={decide.error ? problemMessageOf(decide.error, t) : null}
    >
      <p className="t-small">{t(`restricted.${kind}.body`)}</p>
      <Field
        label={t("restricted.reasonLabel")}
        hint={t("restricted.reasonHint")}
        required
      >
        {(control) => (
          <Textarea
            {...control}
            rows={3}
            value={reason}
            onChange={(event) => setReason(event.target.value)}
          />
        )}
      </Field>
    </ConfirmModal>
  );
}

export function RestrictedRecordsCard() {
  const t = useT();
  const me = useMe();
  const { locale } = useLocale();
  const tz = viewerZone();
  const canRead = useCan("retention_policy", "read");
  // Reading what is held and DECIDING about it are separate grants, so the
  // row action appears only for the authority that can carry it out.
  const canDecide = useCanWrite("retention_policy", "update");
  const [releasing, setReleasing] = useState<OverrideTarget | null>(null);
  const [pinning, setPinning] = useState<OverrideTarget | null>(null);
  const [pinId, setPinId] = useState("");
  const pinErrorId = useId();
  const pinIdIsWellFormed = RECORD_ID_RE.test(pinId.trim());
  const pinIdIsMalformed = pinId.trim() !== "" && !pinIdIsWellFormed;

  const records = useQuery({
    queryKey: RESTRICTED_RECORDS_KEY,
    enabled: canRead,
    queryFn: async () => {
      const { data, error } = await api.GET("/retention/restrictions");
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  if (!canRead) {
    return (
      <Panel title={t("restricted.title")}>
        <PanelBody>
          <p className="settings-panel-sub">{t("restricted.sub")}</p>
          <QueryGate query={me}>
            {() => (
              <EmptyState>
                <p className="t-small">{t("restricted.withheld")}</p>
              </EmptyState>
            )}
          </QueryGate>
        </PanelBody>
      </Panel>
    );
  }

  const columns = [
    {
      key: "kind",
      header: t("restricted.kind"),
      render: (row: RestrictedRecord) => (
        <Badge>
          {KIND_LABEL[row.kind]
            ? t(KIND_LABEL[row.kind])
            : humanizeToken(row.kind)}
        </Badge>
      ),
    },
    {
      key: "occurred",
      header: t("restricted.occurred"),
      render: (row: RestrictedRecord) =>
        formatDate(row.occurred_at, locale, tz),
    },
    {
      key: "deals",
      header: t("restricted.deals"),
      render: (row: RestrictedRecord) =>
        row.deals.length === 0
          ? t("restricted.noDeal")
          : row.deals.map((deal) => deal.name).join(", "),
    },
    {
      key: "reason",
      header: t("restricted.reason"),
      render: (row: RestrictedRecord) => {
        const { cls, basis } = splitReason(row.reason);
        return (
          <span className="retention-scope">
            <span>
              {CLASS_LABEL[cls] ? t(CLASS_LABEL[cls]) : humanizeToken(cls)}
            </span>
            <span className="t-caption">{basis}</span>
          </span>
        );
      },
    },
    {
      key: "until",
      header: t("restricted.until"),
      render: (row: RestrictedRecord) =>
        formatDate(row.restricted_until, locale, tz),
    },
    {
      key: "redacted",
      header: t("restricted.redacted"),
      render: (row: RestrictedRecord) =>
        (row.redacted_fields ?? []).length === 0
          ? t("restricted.nothingRedacted")
          : t("restricted.redactedCount", {
              count: (row.redacted_fields ?? []).length,
            }),
    },
  ];
  if (canDecide) {
    columns.push({
      key: "decide",
      header: t("restricted.decide"),
      render: (row: RestrictedRecord) => (
        <Button
          small
          variant="danger"
          onClick={() => setReleasing({ activityId: row.activity_id })}
        >
          {t("restricted.release.action")}
        </Button>
      ),
    });
  }

  return (
    <Panel title={t("restricted.title")}>
      <PanelBody>
        <p className="settings-panel-sub">{t("restricted.sub")}</p>
        <CardBoundary>
          <SettingList>
            {/* The table is the SUBJECT of this card rather than an answer to a
                question beside it, so it takes the full width under its naming
                — never the right column, and never a dialog. */}
            <SettingRow
              label={t("restricted.heldLabel")}
              layout="stack"
              control={
                <QueryStates query={records}>
                  {records.data &&
                    (records.data.data.length === 0 ? (
                      <EmptyState>{t("restricted.empty")}</EmptyState>
                    ) : (
                      <DataTable
                        label={t("restricted.heldLabel")}
                        columns={columns}
                        rows={records.data.data}
                        rowKey={(row) => row.activity_id}
                      />
                    ))}
                </QueryStates>
              }
            />
            {/* One input and the verb that submits it, so it stays a row: the
                second half of the decision — the reason, and the warning it is
                typed against — is the confirm dialog behind it. Absent without
                the decide grant, exactly as the row-level Release column is. */}
            {canDecide && (
              <SettingRow
                label={t("restricted.pin.action")}
                description={t("restricted.pin.idHint")}
                control={(control) => (
                  <form
                    className="restricted-pin"
                    onSubmit={(event) => {
                      event.preventDefault();
                      setPinning({ activityId: pinId.trim() });
                    }}
                  >
                    <TextInput
                      {...control}
                      // The row already describes the field; a malformed id adds
                      // the refusal to that description rather than replacing
                      // it, so a reader hears the rule and how they broke it.
                      aria-describedby={
                        [
                          control["aria-describedby"],
                          pinIdIsMalformed ? pinErrorId : null,
                        ]
                          .filter(Boolean)
                          .join(" ") || undefined
                      }
                      aria-invalid={pinIdIsMalformed || undefined}
                      value={pinId}
                      onChange={(event) => setPinId(event.target.value)}
                      placeholder={t("restricted.pin.idPlaceholder")}
                    />
                    {/* The short verb, because the row's label already says
                        what the form does: the button carried the same three
                        words a hand to the left of it. */}
                    <Button small type="submit" disabled={!pinIdIsWellFormed}>
                      {t("restricted.pin.submit")}
                    </Button>
                    {pinIdIsMalformed && (
                      <p
                        className="t-caption restricted-pin-error"
                        id={pinErrorId}
                        role="alert"
                      >
                        {t("restricted.pin.idMalformed")}
                      </p>
                    )}
                  </form>
                )}
              />
            )}
          </SettingList>
          <OverrideModal
            target={releasing}
            kind="release"
            onClose={() => setReleasing(null)}
          />
          <OverrideModal
            target={pinning}
            kind="pin"
            onClose={() => {
              setPinning(null);
              setPinId("");
            }}
          />
        </CardBoundary>
      </PanelBody>
    </Panel>
  );
}

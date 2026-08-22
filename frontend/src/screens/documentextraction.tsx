import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Button, TextInput } from "../design-system/atoms";
import { EvidenceMark } from "../design-system/evidencemark";
import type { ConfidenceLevel } from "../design-system/trust";
import { StagingCard } from "../design-system/trust";
import { formatMoney } from "../format/format";
import { minorUnitDigits, toMajorUnits } from "../format/minorunits";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { throwProblem } from "./common";

// What a document says, staged for a human to accept onto the deal.
//
// The panel reads one attached file for four deal facts — its name, its amount,
// its currency, its close date — and offers each one with the text it was read
// from. Nothing here writes anything: accepting is what writes, and accepting is
// a human's click.
//
// THREE STATES, AND THEY MUST NOT COLLAPSE INTO EACH OTHER. A reading that has
// not answered yet, a reading that answered and could ground nothing, and a
// reading that could not read the file at all are three different things to a
// rep, and only the middle one means "this document does not say". Rendering the
// last two the same way is how a rep learns to distrust a correct empty answer —
// or, worse, trusts a broken one.
//
// WHAT THE PANEL WILL NOT DO. It does not offer a value the reading omitted,
// however plausible: a field below the confidence floor is reported as omitted
// with its reason, not shown greyed out with a number in it. And it does not
// re-read the document to check itself before accepting — it sends the id of the
// reading it is showing, so what lands on the deal is the value on this screen.

type Extraction = components["schemas"]["AttachmentExtraction"];
type ExtractedField = components["schemas"]["ExtractedField"];
type OmittedField = components["schemas"]["OmittedExtractionField"];

// The reason a field was not offered, in words. The two are genuinely different
// answers: one sends a rep to the document, the other tells them not to bother.
const OMITTED_REASONS: Record<OmittedField["reason"], MessageKey> = {
  not_stated_in_file: "extraction.omitted.notStated",
  not_confidently_stated: "extraction.omitted.notConfident",
};

// The two field names this panel treats specially: money is rendered as money,
// and the currency it is rendered IN comes from the same reading.
const AMOUNT_FIELD = "amount_minor";
const CURRENCY_FIELD = "currency";

// groundedCurrency is the currency THIS reading grounded, or "" when it
// grounded none — in which case it grounded no amount either, because the two
// stand or fall together server-side (an amount scaled by a guessed currency is
// wrong by a factor nobody notices).
function groundedCurrency(extraction: Extraction): string {
  return extraction.fields.find((f) => f.field === CURRENCY_FIELD)?.value ?? "";
}

const FIELD_LABELS: Record<string, MessageKey> = {
  name: "extraction.field.name",
  amount_minor: "extraction.field.amount",
  currency: "extraction.field.currency",
  expected_close_date: "extraction.field.closeDate",
};

// A reading still moving is worth polling; a terminal one never changes again.
// The interval is the client's only cost for the whole asynchronous shape, so it
// is deliberately unhurried: a document reading takes seconds, and a rep who
// started it is looking at the panel.
const POLL_MS = 2000;

// The contract says `medium`, the design language says `med`. Two vocabularies
// for one band, and the mapping lives here rather than either being renamed:
// the contract word is what a document reading means, the design word is what
// every confidence affordance in the product already renders.
const CONFIDENCE: Record<ExtractedField["confidence"], ConfidenceLevel> = {
  high: "high",
  medium: "med",
};

// The message layer substitutes {name} and does not speak ICU, so a count picks
// its own key. Written out per message rather than built by concatenation: a
// key assembled from fragments is a key no search for it will find.
function plural(count: number, one: MessageKey, other: MessageKey): MessageKey {
  return count === 1 ? one : other;
}

// Money is stored in MINOR units and must never be shown in them.
//
// "14850000" under a label reading "Amount" is not a rendering of €148,500.00,
// it is a different number — and the danger is not that a rep misreads it but
// that they CORRECT it: typing the figure they expected turns €148,500 into
// €1,485. So the amount renders as money, is edited as money, and is converted
// back at the boundary.
//
// The currency is always there to convert with: a reading omits an amount it
// could not pair with one, precisely so no figure is ever scaled by a guess.
// majorUnits renders a stored minor-unit amount as the figure a person types.
// Plain digits rather than a formatted amount: this is what goes INTO an input,
// and a grouped "148,500.00" would come back out as something to re-parse.
export function majorUnits(minor: string, currency: string): string {
  const value = Number(minor);
  if (!Number.isFinite(value)) {
    return minor;
  }
  return toMajorUnits(value, currency).toFixed(minorUnitDigits(currency));
}

// minorUnits is its inverse, and it REFUSES rather than rounds: a figure with
// more decimals than the currency has is a misread, and silently dropping a
// digit is how an amount becomes wrong by an order of magnitude.
export function minorUnits(major: string, currency: string): string | null {
  const trimmed = major.trim().replace(/[\s,]/g, "");
  if (!/^-?\d+(\.\d+)?$/.test(trimmed)) {
    return null;
  }
  const digits = minorUnitDigits(currency);
  const [whole, fraction = ""] = trimmed.split(".");
  if (fraction.length > digits) {
    return null;
  }
  return `${whole}${fraction.padEnd(digits, "0")}`;
}

function isLive(extraction: Extraction | undefined): boolean {
  return extraction?.status === "queued" || extraction?.status === "running";
}

export function DocumentExtractionPanel({
  attachmentId,
  canAccept,
}: Readonly<{ attachmentId: string; canAccept: boolean }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [edits, setEdits] = useState<Record<string, string>>({});
  const [accepted, setAccepted] = useState<number | null>(null);
  const [dismissed, setDismissed] = useState(false);

  const query = useQuery({
    queryKey: ["attachment-extraction", attachmentId],
    queryFn: async () => {
      const { data, error, response } = await api.GET(
        "/attachments/{id}/extraction",
        { params: { path: { id: attachmentId } } },
      );
      // 404 is the honest "nobody has read this file", not a failure — it is
      // what the offer to read one is rendered from.
      if (response.status === 404) {
        return null;
      }
      if (error) {
        throwProblem(error);
      }
      return data ?? null;
    },
    refetchInterval: (q) =>
      isLive(q.state.data ?? undefined) ? POLL_MS : false,
  });

  const read = useMutation({
    mutationFn: async () => {
      const { error } = await api.POST("/attachments/{id}/extraction", {
        params: { path: { id: attachmentId } },
      });
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: async () => {
      setDismissed(false);
      setAccepted(null);
      // A draft typed against the PREVIOUS reading must not survive into this
      // one: it would pre-fill the new field, differ from the new value, and be
      // sent as a deliberate human edit of a reading nobody typed it against.
      setEdits({});
      await queryClient.invalidateQueries({
        queryKey: ["attachment-extraction", attachmentId],
      });
    },
  });

  const extraction = query.data ?? null;

  const accept = useMutation({
    mutationFn: async (fields: readonly ExtractedField[]) => {
      if (!extraction) {
        return;
      }
      const { error } = await api.POST("/attachments/{id}/extraction:accept", {
        params: { path: { id: attachmentId } },
        body: {
          // The reading THIS panel is showing. Not "the latest": a reading
          // somebody else started while this one was on screen must not decide
          // what gets written.
          extraction_id: extraction.id,
          field_keys: fields.map((f) => f.field),
          edits: editedOnly(fields, edits, groundedCurrency(extraction)),
        },
      });
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: async (_data, fields) => {
      setAccepted(fields.length);
      await queryClient.invalidateQueries({ queryKey: ["deals"] });
    },
  });

  if (dismissed) {
    return <p className="t-small">{t("extraction.dismissed")}</p>;
  }
  if (accepted !== null) {
    return (
      <section className="real-card" aria-label={t("extraction.acceptedLabel")}>
        <p className="t-small">
          {t(
            plural(
              accepted,
              "extraction.acceptedHeading.one",
              "extraction.acceptedHeading.other",
            ),
            { count: accepted },
          )}
        </p>
      </section>
    );
  }
  if (query.isLoading) {
    return <p className="t-caption">{t("extraction.loading")}</p>;
  }
  if (!extraction) {
    return (
      <ReadOffer
        onRead={() => read.mutate()}
        pending={read.isPending}
        failed={read.isError}
      />
    );
  }
  return (
    <ExtractionBody
      extraction={extraction}
      canAccept={canAccept}
      edits={edits}
      onEdit={(field, value) => setEdits({ ...edits, [field]: value })}
      onAccept={(fields) => accept.mutate(fields)}
      onDismiss={() => setDismissed(true)}
      onReadAgain={() => read.mutate()}
      accepting={accept.isPending}
      acceptFailed={accept.isError}
    />
  );
}

// editedOnly narrows the draft map to the fields actually being accepted. An
// edit typed and then left out of the accept is inert, and sending it anyway
// would flip the provenance of a field nobody accepted.
function editedOnly(
  fields: readonly ExtractedField[],
  edits: Readonly<Record<string, string>>,
  currency: string,
): Record<string, string> {
  const out: Record<string, string> = {};
  for (const field of fields) {
    const draft = edits[field.field];
    if (draft === undefined) {
      continue;
    }
    // The amount was shown and typed in MAJOR units and is stored in minor:
    // converting at this boundary is what stops a rep's "148500" landing as
    // €1,485. An unconvertible figure is left as typed so the server refuses it
    // by name, rather than being silently rounded into something plausible.
    const value =
      field.field === AMOUNT_FIELD && currency !== ""
        ? (minorUnits(draft, currency) ?? draft)
        : draft;
    if (value !== field.value) {
      out[field.field] = value;
    }
  }
  return out;
}

function ReadOffer({
  onRead,
  pending,
  failed,
}: Readonly<{ onRead: () => void; pending: boolean; failed: boolean }>) {
  const t = useT();
  return (
    <div className="staging-card">
      <p className="t-small">{t("extraction.neverRead")}</p>
      <Button onClick={onRead} disabled={pending}>
        {pending ? t("extraction.starting") : t("extraction.readIt")}
      </Button>
      {failed && <p className="t-caption">{t("extraction.startFailed")}</p>}
    </div>
  );
}

function ExtractionBody({
  extraction,
  canAccept,
  edits,
  onEdit,
  onAccept,
  onDismiss,
  onReadAgain,
  accepting,
  acceptFailed,
}: Readonly<{
  extraction: Extraction;
  canAccept: boolean;
  edits: Readonly<Record<string, string>>;
  onEdit: (field: string, value: string) => void;
  onAccept: (fields: readonly ExtractedField[]) => void;
  onDismiss: () => void;
  onReadAgain: () => void;
  accepting: boolean;
  acceptFailed: boolean;
}>) {
  const t = useT();

  if (extraction.status === "queued" || extraction.status === "running") {
    return <p className="t-caption">{t("extraction.reading")}</p>;
  }
  if (extraction.status === "failed") {
    // The reason is the product here. "It failed" tells a rep nothing they can
    // act on; "this installation's model cannot read a PDF" tells them exactly
    // who to ask.
    return (
      <div className="staging-card">
        <p className="t-small">{t("extraction.failed")}</p>
        {extraction.status_detail && (
          <p className="t-caption">{extraction.status_detail}</p>
        )}
        <Button onClick={onReadAgain}>{t("extraction.readAgain")}</Button>
      </div>
    );
  }
  if (extraction.fields.length === 0) {
    // Read it, and it states none of them. A CORRECT answer, and the detail is
    // what keeps it from reading as a broken feature.
    return (
      <div className="staging-card">
        <p className="t-small">{t("extraction.groundedNothing")}</p>
        {extraction.status_detail && (
          <p className="t-caption">{extraction.status_detail}</p>
        )}
        <OmittedList omitted={extraction.omitted} />
      </div>
    );
  }

  return (
    <StagingCard>
      <p className="t-small">
        {t(
          plural(
            extraction.fields.length,
            "extraction.heading.one",
            "extraction.heading.other",
          ),
          { count: extraction.fields.length },
        )}
      </p>
      <ul className="extraction-fields">
        {extraction.fields.map((field) => (
          <GroundedField
            key={field.field}
            field={field}
            currency={groundedCurrency(extraction)}
            draft={edits[field.field]}
            onEdit={(value) => onEdit(field.field, value)}
            canEdit={canAccept}
          />
        ))}
      </ul>
      <OmittedList omitted={extraction.omitted} />
      {canAccept && (
        <div className="approval-gate">
          <Button
            onClick={() => onAccept(extraction.fields)}
            disabled={accepting}
          >
            {t(
              plural(
                extraction.fields.length,
                "extraction.accept.one",
                "extraction.accept.other",
              ),
              { count: extraction.fields.length },
            )}
          </Button>
          <Button variant="ghost" onClick={onDismiss}>
            {t("extraction.dismiss")}
          </Button>
        </div>
      )}
      {acceptFailed && (
        <p className="t-caption">{t("extraction.acceptFailed")}</p>
      )}
    </StagingCard>
  );
}

// One grounded field: its value carrying the mark that opens onto the quote it
// was read from, and a way to type over it.
//
// The evidence is on the VALUE rather than beside it, which is the design
// language's one provenance affordance — three chips under a number read as
// clutter and the number gets lost among them.
function GroundedField({
  field,
  currency,
  draft,
  onEdit,
  canEdit,
}: Readonly<{
  field: ExtractedField;
  currency: string;
  draft: string | undefined;
  onEdit: (value: string) => void;
  canEdit: boolean;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const label = FIELD_LABELS[field.field];
  const money = field.field === AMOUNT_FIELD && currency !== "";
  const shown = money
    ? formatMoney(Number(field.value), currency, locale)
    : field.value;
  return (
    <li className="extraction-field">
      <span className="t-caption">{label ? t(label) : field.field}</span>
      {draft === undefined ? (
        <EvidenceMark
          value={shown}
          source={{
            // The agent's NAME, not its principal id: the tag says "Automated
            // by <this>", and a prefix carried in here read as "Automated by
            // agent:document-extractor". Parsing an id is provenanceOf's job,
            // and this value never went through it.
            provenance: { kind: "agent", agent: "document-extractor" },
            confidence: CONFIDENCE[field.confidence],
            snippet: field.source_quote,
            at: null,
          }}
        />
      ) : (
        <TextInput
          aria-label={t("extraction.editValue", {
            field: label ? t(label) : field.field,
          })}
          value={draft}
          onChange={(event) => onEdit(event.target.value)}
        />
      )}
      <span className="t-caption">{field.page_or_section}</span>
      {canEdit && draft === undefined && (
        <Button
          variant="ghost"
          onClick={() =>
            onEdit(money ? majorUnits(field.value, currency) : field.value)
          }
        >
          {t("extraction.edit")}
        </Button>
      )}
    </li>
  );
}

// What the document does not say, said out loud.
//
// An omission is an ANSWER, not an absence — "this order form states no close
// date" is something a rep acts on — so it is rendered rather than left off the
// panel for the reader to notice on their own.
function OmittedList({
  omitted,
}: Readonly<{ omitted: readonly OmittedField[] }>) {
  const t = useT();
  if (omitted.length === 0) {
    return null;
  }
  return (
    <ul className="extraction-omitted">
      {omitted.map((field) => {
        const label = FIELD_LABELS[field.field];
        return (
          <li key={field.field} className="t-caption">
            {label ? t(label) : field.field} —{" "}
            {t(OMITTED_REASONS[field.reason])}
          </li>
        );
      })}
    </ul>
  );
}

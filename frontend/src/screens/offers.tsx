import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch } from "../api/version";
import { navigate } from "../app/router";
import {
  Badge,
  Button,
  DataTable,
  Modal,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import { MoneyInput } from "../design-system/moneyinput";
import {
  RecordPicker,
  type RecordPickerCandidate,
} from "../design-system/recordpicker";
import { formatMoney } from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import {
  isVersionSkew,
  ProblemError,
  problemMessage,
  QueryGate,
  throwProblem,
} from "./common";
import { searchProductCandidates } from "./products";

// The offer 360 skeleton (OP-5/OP-6): header, read-only totals, and a
// draft-only header edit. buyer_org_id needs the shared RecordPicker and
// template_id is a server-sourced select, neither of which the
// field-driven EditAction/CreateField machinery (edit.tsx, create.tsx) has
// a slot for — so the edit surface here is a small purpose-built modal,
// not a migration onto that machinery.

type Offer = components["schemas"]["Offer"];
type OfferTemplate = components["schemas"]["OfferTemplate"];
type OfferLineItem = components["schemas"]["OfferLineItem"];
type OfferLineItemInput = components["schemas"]["OfferLineItemInput"];
type UpdateOfferLineItemRequest =
  components["schemas"]["UpdateOfferLineItemRequest"];

async function searchOrganizationCandidates(
  q: string,
): Promise<RecordPickerCandidate[]> {
  const { data, error } = await api.GET("/organizations", {
    params: { query: { q, limit: 10 } },
  });
  if (error) {
    throwProblem(error);
  }
  return data.data.map((org) => ({ id: org.id, name: org.display_name }));
}

function useOfferTemplates() {
  return useQuery({
    queryKey: ["offer-templates-all"],
    queryFn: async () => {
      const { data, error } = await api.GET("/offer-templates", {
        params: { query: { limit: 100 } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data.data;
    },
  });
}

type HeaderEditValues = {
  currency: string;
  buyer_org_id: string | null;
  valid_until: string;
  template_id: string | null;
  intro_text: string;
  terms_text: string;
};

// RecordPicker only highlights a selection among candidates its OWN search
// turned up — it has no way to preview a value set outside its session. A
// freshly reopened header-edit modal only has `offer.buyer_org_id` (a bare
// id), so the picker would otherwise render empty even though a buyer org
// IS set. This resolves that id to a name (the same GET /organizations/{id}
// lookup entityref.tsx uses for the same bare-id-to-name problem), and
// prefers the caller's own override — set once the user actively picks a
// different org — over the resolved incumbent.
function useBuyerOrgPreview(buyerOrgId: string | null, open: boolean) {
  const [override, setOverride] = useState<RecordPickerCandidate | null>(null);
  const existingQuery = useQuery({
    queryKey: ["organization", "ref", buyerOrgId],
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations/{id}", {
        params: { path: { id: buyerOrgId ?? "" } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return {
        id: buyerOrgId ?? "",
        name: data.display_name ?? "",
      } satisfies RecordPickerCandidate;
    },
    enabled: Boolean(buyerOrgId) && open,
    staleTime: 60_000,
  });
  const buyerOrg =
    override ?? (buyerOrgId ? (existingQuery.data ?? null) : null);
  return { buyerOrg, setBuyerOrgOverride: setOverride };
}

function EditOfferHeaderModal({
  open,
  onClose,
  offer,
}: Readonly<{ open: boolean; onClose: () => void; offer: Offer }>) {
  const t = useT();
  const headingId = useId();
  const queryClient = useQueryClient();
  const templatesQuery = useOfferTemplates();
  const [values, setValues] = useState<HeaderEditValues>({
    currency: offer.currency,
    buyer_org_id: offer.buyer_org_id ?? null,
    valid_until: offer.valid_until ?? "",
    template_id: offer.template_id ?? null,
    intro_text: offer.intro_text ?? "",
    terms_text: offer.terms_text ?? "",
  });
  const { buyerOrg, setBuyerOrgOverride } = useBuyerOrgPreview(
    offer.buyer_org_id ?? null,
    open,
  );
  // Only the closed→open transition reprimes the form — a background
  // refetch handing this component a fresh `offer` reference mid-edit must
  // never clobber what the user is typing (same convention as
  // EditRecordModal, edit.tsx).
  const wasOpen = useRef(false);
  useEffect(() => {
    if (open && !wasOpen.current) {
      setValues({
        currency: offer.currency,
        buyer_org_id: offer.buyer_org_id ?? null,
        valid_until: offer.valid_until ?? "",
        template_id: offer.template_id ?? null,
        intro_text: offer.intro_text ?? "",
        terms_text: offer.terms_text ?? "",
      });
      setBuyerOrgOverride(null);
    }
    wasOpen.current = open;
  }, [open, offer, setBuyerOrgOverride]);

  const mutation = useMutation({
    mutationFn: async (input: HeaderEditValues) => {
      const { data, error } = await api.PATCH("/offers/{id}", {
        params: { path: { id: offer.id }, ...ifMatch(offer.version) },
        body: {
          currency: input.currency,
          buyer_org_id: input.buyer_org_id,
          valid_until: input.valid_until || null,
          template_id: input.template_id,
          intro_text: input.intro_text || null,
          terms_text: input.terms_text || null,
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (data) => {
      queryClient.setQueryData(["offer", offer.id], data);
      onClose();
    },
  });

  const skew =
    mutation.error instanceof ProblemError &&
    isVersionSkew(mutation.error.problem);
  const errorMessage = mutation.isError
    ? skew
      ? t("edit.versionSkew")
      : mutation.error?.message
    : null;

  return (
    <Modal open={open} onClose={onClose} labelledBy={headingId}>
      <h2 id={headingId} className="t-h2" style={{ marginBottom: 12 }}>
        {t("offer.edit")}
      </h2>
      <div className="field">
        <span className="t-label" id="offer-currency-label">
          {t("offer.currency")}
        </span>
        <select
          aria-labelledby="offer-currency-label"
          className="input"
          value={values.currency}
          onChange={(event) =>
            setValues((prev) => ({ ...prev, currency: event.target.value }))
          }
        >
          {["EUR", "USD", "GBP", "CHF"].map((c) => (
            <option key={c} value={c}>
              {c}
            </option>
          ))}
        </select>
      </div>
      <div className="field" style={{ marginTop: 10 }}>
        <span className="t-label" id="offer-valid-until-label">
          {t("offer.validUntil")}
        </span>
        <TextInput
          type="date"
          aria-labelledby="offer-valid-until-label"
          value={values.valid_until}
          onChange={(event) =>
            setValues((prev) => ({ ...prev, valid_until: event.target.value }))
          }
        />
      </div>
      <div className="field" style={{ marginTop: 10 }}>
        <span className="t-label">{t("offer.buyerOrg")}</span>
        <RecordPicker
          label={t("offer.buyerOrg")}
          searchTargets={searchOrganizationCandidates}
          selected={buyerOrg}
          onPick={(candidate) => {
            setBuyerOrgOverride(candidate);
            setValues((prev) => ({ ...prev, buyer_org_id: candidate.id }));
          }}
        />
        {buyerOrg && (
          <p className="t-caption">
            {t("offer.buyerOrgConfirm", { name: buyerOrg.name })}
          </p>
        )}
      </div>
      <div className="field" style={{ marginTop: 10 }}>
        <span className="t-label" id="offer-template-label">
          {t("offer.template")}
        </span>
        <select
          aria-labelledby="offer-template-label"
          className="input"
          value={values.template_id ?? ""}
          onChange={(event) =>
            setValues((prev) => ({
              ...prev,
              template_id: event.target.value || null,
            }))
          }
        >
          <option value="">—</option>
          {(templatesQuery.data ?? []).map((template: OfferTemplate) => (
            <option key={template.id} value={template.id}>
              {template.name}
            </option>
          ))}
        </select>
      </div>
      <div className="field" style={{ marginTop: 10 }}>
        <span className="t-label" id="offer-intro-label">
          {t("offer.introText")}
        </span>
        <TextInput
          aria-labelledby="offer-intro-label"
          value={values.intro_text}
          onChange={(event) =>
            setValues((prev) => ({ ...prev, intro_text: event.target.value }))
          }
        />
      </div>
      <div className="field" style={{ marginTop: 10 }}>
        <span className="t-label" id="offer-terms-label">
          {t("offer.termsText")}
        </span>
        <TextInput
          aria-labelledby="offer-terms-label"
          value={values.terms_text}
          onChange={(event) =>
            setValues((prev) => ({ ...prev, terms_text: event.target.value }))
          }
        />
      </div>
      {errorMessage && (
        <p
          className="t-caption"
          style={{ color: "var(--danger)", marginTop: 10 }}
        >
          {errorMessage}
        </p>
      )}
      <div className="actions">
        <Button onClick={onClose}>{t("deals.cancel")}</Button>
        <Button
          variant="primary"
          disabled={mutation.isPending}
          onClick={() => mutation.mutate(values)}
        >
          {t("record.save")}
        </Button>
      </div>
    </Modal>
  );
}

// The line-item editor (OP-7/OP-13). The server-driven-totals
// invariant (P11) governs every mutation below — add/edit/remove all return
// the FULL updated Offer (recomputed line_items + net/tax/gross), and the
// only thing this component ever does with that response is
// queryClient.setQueryData(["offer", ...]) it straight through. Nothing here
// sums, multiplies, or otherwise derives a money figure client-side.

const EMPTY_NEW_LINE = {
  description: "",
  unit: "",
  quantity: "",
  unit_price_minor: 0,
  discount_pct: "",
  tax_rate: "",
};

type NewLineState = typeof EMPTY_NEW_LINE;

function DescriptionCell({
  line,
  onSave,
}: Readonly<{
  line: OfferLineItem;
  onSave: (patch: UpdateOfferLineItemRequest) => void;
}>) {
  const [value, setValue] = useState(line.description);
  return (
    <TextInput
      data-testid={`line-description-${line.id}`}
      style={{ width: 180 }}
      value={value}
      onChange={(event) => setValue(event.target.value)}
      onBlur={() => {
        if (value !== line.description) {
          onSave({ description: value });
        }
      }}
    />
  );
}

function UnitCell({
  line,
  onSave,
}: Readonly<{
  line: OfferLineItem;
  onSave: (patch: UpdateOfferLineItemRequest) => void;
}>) {
  const [value, setValue] = useState(line.unit);
  return (
    <TextInput
      data-testid={`line-unit-${line.id}`}
      style={{ width: 70 }}
      value={value}
      onChange={(event) => setValue(event.target.value)}
      onBlur={() => {
        if (value !== line.unit) {
          onSave({ unit: value });
        }
      }}
    />
  );
}

// The shared numeric-line-field editor (position/quantity/discount/tax rate
// all reduce to the same "local text buffer, commit a parsed number on
// blur" shape). Blurring an emptied field restores the last saved value
// instead of parsing "" as 0 and silently committing it — Number("") is 0,
// not NaN, so without this guard clearing the field to retype a value (a
// normal edit gesture) would zero it out the moment focus left the input.
function NumericCell({
  value: savedValue,
  step,
  width,
  testId,
  onSave,
}: Readonly<{
  value: number;
  step: string;
  width: number;
  testId: string;
  onSave: (num: number) => void;
}>) {
  const [value, setValue] = useState(String(savedValue));
  return (
    <input
      type="number"
      step={step}
      className="input"
      style={{ width }}
      data-testid={testId}
      value={value}
      onChange={(event) => setValue(event.target.value)}
      onBlur={() => {
        if (value.trim() === "") {
          setValue(String(savedValue));
          return;
        }
        const num = Number(value);
        if (!Number.isNaN(num) && num !== savedValue) {
          onSave(num);
        }
      }}
    />
  );
}

function UnitPriceCell({
  line,
  onSave,
}: Readonly<{
  line: OfferLineItem;
  onSave: (patch: UpdateOfferLineItemRequest) => void;
}>) {
  const [minor, setMinor] = useState(line.unit_price_minor);
  return (
    <MoneyInput
      data-testid={`line-unit-price-${line.id}`}
      style={{ width: 90 }}
      valueMinor={minor}
      onChangeMinor={setMinor}
      onBlur={() => {
        if (minor !== line.unit_price_minor) {
          onSave({ unit_price_minor: minor });
        }
      }}
    />
  );
}

// An ungrounded (price_grounded === false) line has no unit-price/line-total
// input wired to it anywhere below — deliberately: grounding a price is a
// server/AI concern, not something a human free-types over. The line stays
// ungrounded until the server re-grounds it on a future regenerate, or the
// human removes it and re-adds it with an explicit price.
function UnpricedCaption({ label }: Readonly<{ label: string }>) {
  return (
    <span className="t-caption" style={{ color: "var(--muted)" }}>
      {label}
    </span>
  );
}

function OfferLineEditor({ offer }: Readonly<{ offer: Offer }>) {
  const t = useT();
  const { locale } = useLocale();
  const queryClient = useQueryClient();
  const [newLine, setNewLine] = useState<NewLineState>(EMPTY_NEW_LINE);
  const [priceTouched, setPriceTouched] = useState(false);
  const [product, setProduct] = useState<RecordPickerCandidate | null>(null);

  const applyOffer = (updated: Offer) => {
    queryClient.setQueryData(["offer", offer.id], updated);
  };

  const addMutation = useMutation({
    mutationFn: async (input: OfferLineItemInput) => {
      const { data, error } = await api.POST("/offers/{id}/line-items", {
        params: { path: { id: offer.id } },
        body: input,
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (data) => {
      applyOffer(data);
      setNewLine(EMPTY_NEW_LINE);
      setPriceTouched(false);
      setProduct(null);
    },
  });

  // The generated contract (crm.yaml: updateOfferLineItem) declares no
  // If-Match parameter on this operation — unlike the header-level Offer
  // PATCH, a line item's own `version` is not a concurrency precondition
  // the API accepts here. Sending one would fail to type-check against the
  // generated client; contract wins over an assumed convention (P3).
  const updateMutation = useMutation({
    mutationFn: async (variables: {
      lineItemId: string;
      patch: UpdateOfferLineItemRequest;
    }) => {
      const { data, error } = await api.PATCH(
        "/offers/{id}/line-items/{lineItemId}",
        {
          params: { path: { id: offer.id, lineItemId: variables.lineItemId } },
          body: variables.patch,
        },
      );
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (data) => {
      applyOffer(data);
    },
  });

  const removeMutation = useMutation({
    mutationFn: async (lineItemId: string) => {
      const { data, error } = await api.DELETE(
        "/offers/{id}/line-items/{lineItemId}",
        { params: { path: { id: offer.id, lineItemId } } },
      );
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (data) => {
      applyOffer(data);
    },
  });

  const activeError =
    addMutation.error ?? updateMutation.error ?? removeMutation.error;
  const skew =
    activeError instanceof ProblemError && isVersionSkew(activeError.problem);
  const errorMessage = activeError
    ? skew
      ? t("edit.versionSkew")
      : activeError.message
    : null;

  const saveLine =
    (lineItemId: string) => (patch: UpdateOfferLineItemRequest) => {
      updateMutation.mutate({ lineItemId, patch });
    };

  const columns = [
    {
      key: "position",
      header: t("offer.position"),
      render: (line: OfferLineItem) => (
        <NumericCell
          value={line.position}
          step="1"
          width={60}
          testId={`line-position-${line.id}`}
          onSave={(num) => saveLine(line.id)({ position: num })}
        />
      ),
    },
    {
      key: "description",
      header: t("offer.description"),
      render: (line: OfferLineItem) => (
        <DescriptionCell line={line} onSave={saveLine(line.id)} />
      ),
    },
    {
      key: "unit",
      header: t("offer.unit"),
      render: (line: OfferLineItem) => (
        <UnitCell line={line} onSave={saveLine(line.id)} />
      ),
    },
    {
      key: "quantity",
      header: t("offer.quantity"),
      render: (line: OfferLineItem) => (
        <NumericCell
          value={line.quantity}
          step="0.001"
          width={90}
          testId={`line-quantity-${line.id}`}
          onSave={(num) => saveLine(line.id)({ quantity: num })}
        />
      ),
    },
    {
      key: "unitPrice",
      header: t("offer.unitPrice"),
      render: (line: OfferLineItem) =>
        line.price_grounded === false ? (
          <UnpricedCaption label={t("offer.unpriced")} />
        ) : (
          <UnitPriceCell line={line} onSave={saveLine(line.id)} />
        ),
    },
    {
      key: "discountPct",
      header: t("offer.discountPct"),
      render: (line: OfferLineItem) => (
        <NumericCell
          value={line.discount_pct}
          step="0.01"
          width={90}
          testId={`line-discount-${line.id}`}
          onSave={(num) => saveLine(line.id)({ discount_pct: num })}
        />
      ),
    },
    {
      key: "taxRate",
      header: t("offer.taxRate"),
      render: (line: OfferLineItem) => (
        <NumericCell
          value={line.tax_rate}
          step="0.01"
          width={90}
          testId={`line-tax-rate-${line.id}`}
          onSave={(num) => saveLine(line.id)({ tax_rate: num })}
        />
      ),
    },
    {
      key: "lineTotal",
      header: t("offer.lineTotal"),
      render: (line: OfferLineItem) =>
        line.price_grounded === false ? (
          <UnpricedCaption label={t("offer.unpriced")} />
        ) : (
          <span className="t-mono">
            {formatMoney(line.line_total_minor, offer.currency, locale)}
          </span>
        ),
    },
    {
      key: "remove",
      header: "",
      render: (line: OfferLineItem) => (
        <Button
          small
          data-testid={`remove-line-${line.id}`}
          disabled={removeMutation.isPending}
          onClick={() => removeMutation.mutate(line.id)}
        >
          {t("offer.removeLine")}
        </Button>
      ),
    },
  ];

  return (
    <section
      className="card"
      data-testid="offer-line-editor"
      style={{ marginBottom: 16 }}
    >
      <SectionHeader title={t("offer.lines")} />
      <DataTable
        columns={columns}
        rows={offer.line_items}
        rowKey={(line) => `${line.id}:${line.version ?? 0}`}
      />
      <div style={{ marginTop: 16 }}>
        <span className="t-label">{t("offer.addLine")}</span>
        <div
          style={{
            display: "flex",
            gap: 8,
            flexWrap: "wrap",
            alignItems: "flex-end",
            marginTop: 8,
          }}
        >
          <div className="field">
            <span className="t-label" id="new-line-description-label">
              {t("offer.description")}
            </span>
            <TextInput
              aria-labelledby="new-line-description-label"
              data-testid="new-line-description"
              style={{ width: 180 }}
              value={newLine.description}
              onChange={(event) =>
                setNewLine((prev) => ({
                  ...prev,
                  description: event.target.value,
                }))
              }
            />
          </div>
          <div className="field">
            <span className="t-label" id="new-line-unit-label">
              {t("offer.unit")}
            </span>
            <TextInput
              aria-labelledby="new-line-unit-label"
              data-testid="new-line-unit"
              style={{ width: 70 }}
              value={newLine.unit}
              onChange={(event) =>
                setNewLine((prev) => ({ ...prev, unit: event.target.value }))
              }
            />
          </div>
          <div className="field">
            <span className="t-label" id="new-line-quantity-label">
              {t("offer.quantity")}
            </span>
            <input
              aria-labelledby="new-line-quantity-label"
              data-testid="new-line-quantity"
              type="number"
              step="0.001"
              className="input"
              style={{ width: 90 }}
              value={newLine.quantity}
              onChange={(event) =>
                setNewLine((prev) => ({
                  ...prev,
                  quantity: event.target.value,
                }))
              }
            />
          </div>
          <div className="field">
            <span className="t-label" id="new-line-price-label">
              {t("offer.unitPrice")}
            </span>
            <MoneyInput
              aria-labelledby="new-line-price-label"
              data-testid="new-line-unit-price"
              valueMinor={newLine.unit_price_minor}
              onChangeMinor={(minor) => {
                setPriceTouched(true);
                setNewLine((prev) => ({ ...prev, unit_price_minor: minor }));
              }}
            />
          </div>
          <div className="field">
            <span className="t-label" id="new-line-discount-label">
              {t("offer.discountPct")}
            </span>
            <input
              aria-labelledby="new-line-discount-label"
              data-testid="new-line-discount"
              type="number"
              step="0.01"
              className="input"
              style={{ width: 90 }}
              value={newLine.discount_pct}
              onChange={(event) =>
                setNewLine((prev) => ({
                  ...prev,
                  discount_pct: event.target.value,
                }))
              }
            />
          </div>
          <div className="field">
            <span className="t-label" id="new-line-tax-label">
              {t("offer.taxRate")}
            </span>
            <input
              aria-labelledby="new-line-tax-label"
              data-testid="new-line-tax"
              type="number"
              step="0.01"
              className="input"
              style={{ width: 90 }}
              value={newLine.tax_rate}
              onChange={(event) =>
                setNewLine((prev) => ({
                  ...prev,
                  tax_rate: event.target.value,
                }))
              }
            />
          </div>
        </div>
        <div
          style={{
            display: "flex",
            gap: 8,
            alignItems: "flex-start",
            marginTop: 12,
          }}
        >
          <div className="field" style={{ minWidth: 220 }}>
            <span className="t-label">{t("offer.pickProduct")}</span>
            <RecordPicker
              label={t("offer.pickProduct")}
              searchTargets={searchProductCandidates}
              selected={product}
              onPick={setProduct}
            />
            {product && (
              <p className="t-caption">
                {t("offer.pickProductConfirm", { name: product.name })}
              </p>
            )}
          </div>
          <Button
            variant="primary"
            data-testid="add-line"
            disabled={addMutation.isPending}
            style={{ marginTop: 24 }}
            onClick={() =>
              addMutation.mutate({
                product_id: product?.id ?? undefined,
                description: newLine.description || undefined,
                unit: newLine.unit || undefined,
                quantity: Number(newLine.quantity),
                unit_price_minor: priceTouched
                  ? newLine.unit_price_minor
                  : undefined,
                discount_pct:
                  newLine.discount_pct === ""
                    ? undefined
                    : Number(newLine.discount_pct),
                tax_rate:
                  newLine.tax_rate === ""
                    ? undefined
                    : Number(newLine.tax_rate),
              })
            }
          >
            {t("offer.addLine")}
          </Button>
        </div>
        {errorMessage && (
          <p
            className="t-caption"
            style={{ color: "var(--danger)", marginTop: 10 }}
          >
            {errorMessage}
          </p>
        )}
      </div>
    </section>
  );
}

// The send/accept/reject lifecycle (OP-8/OP-9/OP-10). All three
// return the FULL updated Offer (P11 — server-truth totals/status), so the
// only client-side work on success is queryClient.setQueryData(["offer",
// ...]) — never a locally-derived status flip. Send is the confirm-first
// (🟡) action: a human's own click on this REST path IS the approval
// (ADR-0055), so no ApprovalToken/Idempotency-Key header is sent here — that
// plumbing belongs to the agent/passport path, out of scope for this screen.

function SendOfferAction({ offer }: Readonly<{ offer: Offer }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);

  const mutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/offers/{id}/send", {
        params: { path: { id: offer.id }, ...ifMatch(offer.version) },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (data) => {
      queryClient.setQueryData(["offer", offer.id], data);
      setOpen(false);
    },
  });

  const skew =
    mutation.error instanceof ProblemError &&
    isVersionSkew(mutation.error.problem);
  const errorMessage = mutation.isError
    ? skew
      ? t("edit.versionSkew")
      : (mutation.error?.message ?? null)
    : null;

  return (
    <>
      <Button
        variant="primary"
        small
        data-testid="send-offer"
        onClick={() => setOpen(true)}
      >
        {t("offer.send")}
      </Button>
      <ConfirmModal
        open={open}
        onClose={() => setOpen(false)}
        title={t("offer.sendConfirm")}
        tier="confirm"
        confirmLabel={t("deals.confirm")}
        onConfirm={() => mutation.mutate()}
        pending={mutation.isPending}
        error={errorMessage}
      >
        <p className="t-body">{t("offer.sendBody")}</p>
      </ConfirmModal>
    </>
  );
}

// Accept (OP-9) is human-only — the deal's amount/currency sync server-side
// on acceptance, so the deal screen's amount card and offers panel (the
// exact ["deal", id] / ["deal-offers", id] keys deals.tsx's DealScreen
// reads) must be told to refetch; setQueryData alone would leave the deal
// screen showing stale figures if the user navigates back to it.
function AcceptOfferAction({ offer }: Readonly<{ offer: Offer }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);

  const mutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/offers/{id}/accept", {
        params: { path: { id: offer.id }, ...ifMatch(offer.version) },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (data) => {
      queryClient.setQueryData(["offer", offer.id], data);
      queryClient.invalidateQueries({ queryKey: ["deal", offer.deal_id] });
      queryClient.invalidateQueries({
        queryKey: ["deal-offers", offer.deal_id],
      });
      setOpen(false);
    },
  });

  const skew =
    mutation.error instanceof ProblemError &&
    isVersionSkew(mutation.error.problem);
  const errorMessage = mutation.isError
    ? skew
      ? t("edit.versionSkew")
      : (mutation.error?.message ?? null)
    : null;

  return (
    <>
      <Button
        variant="primary"
        small
        data-testid="accept-offer"
        onClick={() => setOpen(true)}
      >
        {t("offer.accept")}
      </Button>
      <ConfirmModal
        open={open}
        onClose={() => setOpen(false)}
        title={t("offer.acceptConfirm")}
        confirmLabel={t("deals.confirm")}
        onConfirm={() => mutation.mutate()}
        pending={mutation.isPending}
        error={errorMessage}
      >
        <p className="t-body">{t("offer.acceptBody")}</p>
      </ConfirmModal>
    </>
  );
}

// Reject (OP-10) never touches the deal's amount, so unlike accept it only
// ever needs the offer's own query updated. The optional reason is a plain
// text field, not a bespoke form — proportionate to a non-money-moving
// action, but still routed through the shared ConfirmModal chrome (no
// `tier`: rejecting isn't a confirm-first 🟡 operation) for the same
// disable-while-pending / inline-error affordances Send and Accept get.
function RejectOfferAction({ offer }: Readonly<{ offer: Offer }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");

  const mutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/offers/{id}/reject", {
        params: { path: { id: offer.id }, ...ifMatch(offer.version) },
        body: { reason: reason || null },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (data) => {
      queryClient.setQueryData(["offer", offer.id], data);
      setOpen(false);
      setReason("");
    },
  });

  const skew =
    mutation.error instanceof ProblemError &&
    isVersionSkew(mutation.error.problem);
  const errorMessage = mutation.isError
    ? skew
      ? t("edit.versionSkew")
      : (mutation.error?.message ?? null)
    : null;

  return (
    <>
      <Button
        variant="danger"
        small
        data-testid="reject-offer"
        onClick={() => setOpen(true)}
      >
        {t("offer.reject")}
      </Button>
      <ConfirmModal
        open={open}
        onClose={() => setOpen(false)}
        title={t("offer.rejectConfirm")}
        confirmLabel={t("deals.confirm")}
        onConfirm={() => mutation.mutate()}
        pending={mutation.isPending}
        error={errorMessage}
      >
        <div className="field">
          <span className="t-label" id="reject-reason-label">
            {t("offer.rejectReason")}
          </span>
          <TextInput
            aria-labelledby="reject-reason-label"
            data-testid="reject-reason"
            value={reason}
            onChange={(event) => setReason(event.target.value)}
          />
        </div>
      </ConfirmModal>
    </>
  );
}

// Regenerate a new draft revision from a sent offer (OP-11). The 201
// response is the ONLY place the Art. 50 disclosure and diff summary ever
// appear (every later read of the same offer returns them null), so the
// cache for the NEW draft's id is seeded directly from this response —
// before navigating — and OfferScreen's own query for that id skips its
// refetch-on-mount for exactly this reason (see its `refetchOnMount: false`
// below). Regenerate is non-destructive to the current (sent) offer, which
// stays sent/superseded server-side rather than being deleted, so unlike
// Send it isn't gated behind a confirm modal.
function RegenerateOfferAction({ offer }: Readonly<{ offer: Offer }>) {
  const t = useT();
  const queryClient = useQueryClient();

  const mutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/offers/{id}/regenerate", {
        params: { path: { id: offer.id } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (newDraft) => {
      queryClient.setQueryData(["offer", newDraft.id], newDraft);
      // The new draft revision is a new offer row on the same deal — the
      // deal's offers panel (deals.tsx) must see it too, same as
      // AcceptOfferAction invalidates after its own new-row-shaped change.
      queryClient.invalidateQueries({
        queryKey: ["deal-offers", newDraft.deal_id],
      });
      navigate({ screen: "offers", id: newDraft.id });
    },
  });

  const errorMessage = mutation.isError ? mutation.error?.message : null;

  return (
    <>
      <Button
        small
        data-testid="regenerate-offer"
        disabled={mutation.isPending}
        onClick={() => mutation.mutate()}
      >
        {t("offer.regenerate")}
      </Button>
      {errorMessage && (
        <p
          className="t-caption"
          style={{ color: "var(--danger)", marginTop: 4 }}
        >
          {errorMessage}
        </p>
      )}
    </>
  );
}

// The Art. 50 disclosure + diff-from-previous summary (OP-11). Both fields
// are transient — populated only on the regenerate response that produced
// this draft — but the offer object here may be a stale/refetched read
// (ai_generated back to false), so the banner degrades to nothing rather
// than an empty/broken shell whenever ai_generated isn't true. The
// disclosure text is a compliance-mandated string: rendered verbatim, never
// reworded or wrapped in translated copy.
function DiffLine({
  line,
  currency,
  locale,
}: Readonly<{ line: OfferLineItem; currency: string; locale: Locale }>) {
  return (
    <li>
      <span>{line.description}</span>
      {line.price_grounded === false ? null : (
        <>
          {" — "}
          <span className="t-mono">
            {formatMoney(line.line_total_minor, currency, locale)}
          </span>
        </>
      )}
    </li>
  );
}

function AiDisclosureBanner({ offer }: Readonly<{ offer: Offer }>) {
  const t = useT();
  const { locale } = useLocale();
  if (!offer.ai_generated) {
    return null;
  }
  const diff = offer.diff_from_previous;
  const added = diff?.added ?? [];
  const removed = diff?.removed ?? [];
  const changed = diff?.changed ?? [];

  return (
    <section
      className="card"
      data-testid="ai-disclosure-banner"
      style={{ marginBottom: 16 }}
    >
      <SectionHeader title={t("offer.aiDisclosureTitle")} />
      {offer.ai_disclosure && <p className="t-body">{offer.ai_disclosure}</p>}
      {diff && (
        <div data-testid="offer-diff-summary" style={{ marginTop: 8 }}>
          {added.length > 0 && (
            <div>
              <p className="t-label">
                {t("offer.diffAdded", { count: added.length })}
              </p>
              <ul>
                {added.map((line) => (
                  <DiffLine
                    key={line.id}
                    line={line}
                    currency={offer.currency}
                    locale={locale}
                  />
                ))}
              </ul>
            </div>
          )}
          {removed.length > 0 && (
            <div>
              <p className="t-label">
                {t("offer.diffRemoved", { count: removed.length })}
              </p>
              <ul>
                {removed.map((line) => (
                  <DiffLine
                    key={line.id}
                    line={line}
                    currency={offer.currency}
                    locale={locale}
                  />
                ))}
              </ul>
            </div>
          )}
          {changed.length > 0 && (
            <div>
              <p className="t-label">
                {t("offer.diffChanged", { count: changed.length })}
              </p>
              <ul>
                {changed.map((pair) =>
                  pair.after ? (
                    <DiffLine
                      key={pair.after.id}
                      line={pair.after}
                      currency={offer.currency}
                      locale={locale}
                    />
                  ) : null,
                )}
              </ul>
            </div>
          )}
        </div>
      )}
    </section>
  );
}

// Render the offer's branded PDF (OP-12). Per the contract's own
// doc comment, a 501 here means the deployment has no blobstore wired — the
// same unwired-by-omission posture as the attachments seam — which is a
// deliberate, expected outcome, not an error: it is read off the raw
// `response.status` (openapi-fetch's third destructured field, the same
// idiom home.tsx's useMorningBrief uses for its 404) BEFORE the `error`
// branch, so it never reaches throwProblem/ProblemError. Every other
// response (401/403/404/409/422) falls through to that verbatim path
// unchanged. On 200 the full Offer comes back with pdf_asset_ref populated;
// queryClient.setQueryData seeds the cache the same way every other action
// in this file does, so the link below reads straight off the `offer` prop
// once react-query re-renders this component.
function RenderOfferPdfAction({ offer }: Readonly<{ offer: Offer }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const pdfHref = `${globalThis.location.origin}/v1/offers/${offer.id}/pdf`;

  const mutation = useMutation({
    mutationFn: async () => {
      const { data, error, response } = await api.POST("/offers/{id}/render", {
        params: { path: { id: offer.id } },
      });
      if (response.status === 501) {
        return { available: false as const };
      }
      if (error) {
        throwProblem(error);
      }
      return { available: true as const, offer: data };
    },
    onSuccess: (result) => {
      if (result.available) {
        queryClient.setQueryData(["offer", offer.id], result.offer);
      }
    },
  });

  const unavailable = mutation.data?.available === false;
  const errorMessage = mutation.isError ? mutation.error?.message : null;

  return (
    <section
      className="card"
      data-testid="offer-pdf-card"
      style={{ marginBottom: 16 }}
    >
      <SectionHeader title={t("offer.renderPdf")} />
      <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
        <Button
          small
          data-testid="render-pdf"
          disabled={mutation.isPending}
          onClick={() => mutation.mutate()}
        >
          {t("offer.renderPdf")}
        </Button>
        {offer.pdf_asset_ref && (
          <a
            href={pdfHref}
            target="_blank"
            rel="noreferrer"
            data-testid="pdf-link"
          >
            {t("offer.viewPdf")}
          </a>
        )}
      </div>
      {unavailable && (
        <p
          className="t-caption"
          data-testid="pdf-unavailable"
          style={{ marginTop: 8 }}
        >
          {t("offer.pdfUnavailable")}
        </p>
      )}
      {errorMessage && (
        <p
          className="t-caption"
          style={{ color: "var(--danger)", marginTop: 8 }}
        >
          {errorMessage}
        </p>
      )}
    </section>
  );
}

export function OfferScreen({ id }: Readonly<{ id: string }>) {
  const t = useT();
  const { locale } = useLocale();
  const [editing, setEditing] = useState(false);
  const offerQuery = useQuery({
    queryKey: ["offer", id],
    queryFn: async () => {
      const { data, error } = await api.GET("/offers/{id}", {
        params: { path: { id } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    // RegenerateOfferAction seeds this exact query key with its 201
    // response — the only place ai_disclosure/diff_from_previous are ever
    // populated — right before navigating here. The default
    // refetchOnMount would otherwise immediately re-GET and silently wipe
    // both fields the instant this screen mounts, contradicting that
    // seeding's whole purpose. This never blocks a genuinely fresh
    // navigation (no cache entry yet): refetchOnMount only skips a refetch
    // when there IS cached data for this key already.
    refetchOnMount: false,
  });

  return (
    <div className="wrap narrow">
      <QueryGate query={offerQuery}>
        {(offer) => (
          <>
            <section className="card" style={{ marginBottom: 16 }}>
              <div className="list-head">
                <SectionHeader
                  title={offer.offer_number}
                  sub={t("offer.revision", {
                    revision: String(offer.revision),
                  })}
                />
                <Badge>{offer.status}</Badge>
              </div>
              <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
                <Button
                  small
                  onClick={() =>
                    navigate({ screen: "deals", id: offer.deal_id })
                  }
                >
                  {t("offer.backToDeal")}
                </Button>
                {offer.status === "draft" && (
                  <Button
                    small
                    data-testid="edit-offer-header"
                    onClick={() => setEditing(true)}
                  >
                    {t("offer.edit")}
                  </Button>
                )}
                {offer.status === "draft" && <SendOfferAction offer={offer} />}
                {offer.status === "sent" && (
                  <>
                    <AcceptOfferAction offer={offer} />
                    <RejectOfferAction offer={offer} />
                    <RegenerateOfferAction offer={offer} />
                  </>
                )}
              </div>
            </section>
            <AiDisclosureBanner offer={offer} />
            <section className="card" style={{ marginBottom: 16 }}>
              <SectionHeader title={t("offer.totals")} />
              <div style={{ display: "flex", gap: 24 }}>
                <div>
                  <span className="t-label">{t("offer.net")}</span>
                  <div className="t-mono">
                    {formatMoney(offer.net_minor, offer.currency, locale)}
                  </div>
                </div>
                <div>
                  <span className="t-label">{t("offer.tax")}</span>
                  <div className="t-mono">
                    {formatMoney(offer.tax_minor, offer.currency, locale)}
                  </div>
                </div>
                <div>
                  <span className="t-label">{t("offer.gross")}</span>
                  <div className="t-mono">
                    {formatMoney(offer.gross_minor, offer.currency, locale)}
                  </div>
                </div>
              </div>
            </section>
            <RenderOfferPdfAction offer={offer} />
            {offer.status === "draft" && <OfferLineEditor offer={offer} />}
            {offer.status === "draft" && (
              <EditOfferHeaderModal
                open={editing}
                onClose={() => setEditing(false)}
                offer={offer}
              />
            )}
          </>
        )}
      </QueryGate>
    </div>
  );
}

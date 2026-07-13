import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch } from "../api/version";
import { navigate } from "../app/router";
import {
  Badge,
  Button,
  Modal,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
import {
  RecordPicker,
  type RecordPickerCandidate,
} from "../design-system/recordpicker";
import { formatMoney } from "../format/format";
import { useLocale, useT } from "../i18n";
import {
  isVersionSkew,
  ProblemError,
  problemMessage,
  QueryGate,
  throwProblem,
} from "./common";

// Task 2.3 (OP-5/OP-6, Phase 2 close-out): the offer 360 skeleton — header,
// read-only totals, and a draft-only header edit. buyer_org_id needs the
// shared RecordPicker and template_id is a server-sourced select, neither of
// which the field-driven EditAction/CreateField machinery (edit.tsx,
// create.tsx) has a slot for — so the edit surface here is a small
// purpose-built modal, not a migration onto that machinery. Line items,
// send/accept/reject, and regenerate/render are later Phase-3/4 tasks; this
// screen never touches them.

type Offer = components["schemas"]["Offer"];
type OfferTemplate = components["schemas"]["OfferTemplate"];

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
  const [buyerOrg, setBuyerOrg] = useState<RecordPickerCandidate | null>(null);
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
      setBuyerOrg(null);
    }
    wasOpen.current = open;
  }, [open, offer]);

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
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["offer", offer.id] });
      onClose();
    },
  });

  const skew =
    mutation.error instanceof ProblemError &&
    isVersionSkew(mutation.error.problem);
  const errorMessage = mutation.isError
    ? skew
      ? t("edit.versionSkew")
      : (mutation.error as Error).message
    : null;

  return (
    <Modal open={open} onClose={onClose} labelledBy={headingId}>
      <h2 id={headingId} className="t-h2" style={{ marginBottom: 12 }}>
        {t("offer.edit")}
      </h2>
      <div className="field">
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
            setBuyerOrg(candidate);
            setValues((prev) => ({ ...prev, buyer_org_id: candidate.id }));
          }}
        />
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
              </div>
            </section>
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

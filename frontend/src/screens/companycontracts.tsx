import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Fragment, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCan } from "../app/capability";
import {
  Badge,
  Button,
  EmptyState,
  OverflowMenu,
} from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import { Panel, PanelBody, PanelRow } from "../design-system/panel";
import { type SectionState, SurfaceState } from "../design-system/surfacestate";
import { formatDate, formatMoney } from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { throwProblem } from "./common";
import { RECORD_ZONE } from "./company360";
import { ContractForm } from "./contractform";

// The account's agreements: what it signed, what each is worth, and when the
// next one has to be decided.
//
// TWO READINGS THAT ARE NOT THE SAME READING, and the whole card turns on
// keeping them apart.
//
// `status` is what a human asserted. `under_contract` is computed from the
// dates. They disagree exactly when a term has run out and nobody has moved the
// status yet — the normal state of an account whose expiry proposal is sitting
// in an approval queue. Showing only the status would render that account as a
// live customer; showing only the derived reading would erase the pending
// decision. So a row that has ended while still marked active says both.

type Contract = components["schemas"]["Contract"];
type ContractStatus = NonNullable<Contract["status"]>;

const STATUS_LABELS: Record<ContractStatus, MessageKey> = {
  draft: "contracts.status.draft",
  active: "contracts.status.active",
  expired: "contracts.status.expired",
  cancelled: "contracts.status.cancelled",
  superseded: "contracts.status.superseded",
};

// Only two states change how a row should READ. A superseded agreement is
// history; a cancelled one is a fact the reader needs to notice. The rest are
// equal citizens and get no tone, because tone on everything is tone on nothing.
const STATUS_TONE: Partial<Record<ContractStatus, "warn" | "danger">> = {
  superseded: "warn",
  cancelled: "danger",
};

function contractsState(
  loading: boolean,
  failed: boolean,
  mayRead: boolean,
  count: number,
): SectionState {
  // A reader without the grant is WITHHELD, never empty: "this account has no
  // agreements" and "you may not see them" are different sentences, and only
  // one of them is about the account.
  if (!mayRead) {
    return "withheld";
  }
  if (loading) {
    return "loading";
  }
  if (failed) {
    return "unavailable";
  }
  if (count === 0) {
    return "empty";
  }
  return "ready";
}

export function CompanyContractsCard({ orgId }: Readonly<{ orgId: string }>) {
  const t = useT();
  const mayRead = useCan("contract", "read");
  const mayWrite = useCan("contract", "update");
  const mayArchive = useCan("contract", "delete");
  const [activeOnly, setActiveOnly] = useState(false);
  // `editing` carries the contract being corrected; undefined means the form is
  // adding a new one. One form serves both, because "record what we agreed" and
  // "fix what I typed wrong" are the same fields.
  const [editing, setEditing] = useState<Contract | undefined>();
  const [formOpen, setFormOpen] = useState(false);

  const query = useQuery({
    queryKey: ["orgContracts", orgId, activeOnly],
    enabled: mayRead,
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations/{id}/contracts", {
        params: {
          path: { id: orgId },
          query: activeOnly ? { under_contract_only: true } : {},
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data?.data ?? [];
    },
  });
  const contracts = query.data ?? [];
  const state = contractsState(
    query.isPending,
    query.isError,
    mayRead,
    contracts.length,
  );
  const present = state === "ready" || state === "empty";

  return (
    <>
      {/* A SIBLING of the panel, not a child: a panel draws its rows only when
          the section has them, so a modal nested inside would never mount on an
          account with no agreements — the account the add button most exists
          for. */}
      <ContractForm
        orgId={orgId}
        contract={editing}
        open={formOpen}
        onClose={() => {
          setFormOpen(false);
          setEditing(undefined);
        }}
      />
      <Panel
        title={t("contracts.title")}
        titleAction={
          mayWrite ? (
            <Button
              small
              onClick={() => {
                setEditing(undefined);
                setFormOpen(true);
              }}
            >
              {t("contracts.add")}
            </Button>
          ) : undefined
        }
      >
        {present && (
          <PanelBody className="docs-filters">
            <Button
              small
              aria-pressed={!activeOnly}
              onClick={() => setActiveOnly(false)}
            >
              {t("contracts.filter.all")}
            </Button>
            <Button
              small
              aria-pressed={activeOnly}
              onClick={() => setActiveOnly(true)}
            >
              {t("contracts.filter.active")}
            </Button>
          </PanelBody>
        )}
        {present ? (
          contracts.length === 0 ? (
            <PanelBody>
              <EmptyState>
                {t(activeOnly ? "contracts.noneActive" : "contracts.empty")}
              </EmptyState>
            </PanelBody>
          ) : (
            contracts.map((contract) => (
              <Fragment key={contract.id}>
                <ContractRow
                  contract={contract}
                  orgId={orgId}
                  mayWrite={mayWrite}
                  mayArchive={mayArchive}
                  onEdit={() => {
                    setEditing(contract);
                    setFormOpen(true);
                  }}
                />
              </Fragment>
            ))
          )
        ) : (
          <PanelBody>
            <SurfaceState state={state} emptyLabel={t("contracts.empty")}>
              {null}
            </SurfaceState>
          </PanelBody>
        )}
      </Panel>
    </>
  );
}

function ContractRow({
  contract,
  orgId,
  mayWrite,
  mayArchive,
  onEdit,
}: Readonly<{
  contract: Contract;
  orgId: string;
  mayWrite: boolean;
  mayArchive: boolean;
  onEdit: () => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const queryClient = useQueryClient();
  const [asking, setAsking] = useState(false);

  // The id is a VARIABLE, never closed over: a click landing before React
  // re-arms the mutation would otherwise archive whatever the previous render
  // held.
  const archive = useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.DELETE("/contracts/{id}", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: () => {
      setAsking(false);
      queryClient.invalidateQueries({ queryKey: ["orgContracts", orgId] });
      queryClient.invalidateQueries({ queryKey: ["org360", orgId] });
    },
  });

  return (
    <PanelRow className="docs-row">
      {/* The title opens the same form the add button does. A row a reader
          cannot open is a row they cannot correct, and a mistyped value is the
          most likely thing they came here to fix. */}
      <button type="button" className="co-rowlink" onClick={onEdit}>
        {contract.title}
      </button>
      {contract.contract_number && (
        <span className="t-caption">{contract.contract_number}</span>
      )}
      <span>{contractValue(contract, locale, perYearLabel(t))}</span>
      {contract.status && (
        <Badge tone={STATUS_TONE[contract.status]}>
          {t(STATUS_LABELS[contract.status])}
        </Badge>
      )}
      <ContractTermState contract={contract} />
      <ContractPaper contractId={contract.id} orgId={orgId} />
      {(mayWrite || mayArchive) && (
        <OverflowMenu label={t("contracts.rowMenu")}>
          {mayWrite && (
            <button type="button" onClick={onEdit}>
              {t("contracts.edit")}
            </button>
          )}
          {mayArchive && (
            <button type="button" onClick={() => setAsking(true)}>
              {t("contracts.archive")}
            </button>
          )}
        </OverflowMenu>
      )}
      <ConfirmModal
        open={asking}
        onClose={() => setAsking(false)}
        title={t("contracts.archive.title")}
        confirmLabel={t("contracts.archive.confirm")}
        confirmVariant="danger"
        pending={archive.isPending}
        onConfirm={() => archive.mutate(contract.id)}
      >
        {/* Archive is the delete, and the copy says what survives it: the row
            and its history stay, because deleting a contract would silently
            change whether this account ever counted as a customer. */}
        {t("contracts.archive.body", { title: contract.title })}
      </ConfirmModal>
    </PanelRow>
  );
}

/**
 * ContractPaper is the signed document itself, on the row for the agreement it
 * belongs to.
 *
 * The link is filed at upload as `attachment.contract_id`, so this asks the
 * documents endpoint for exactly that agreement's paper rather than guessing
 * from a matching title — a company with a 2024 and a 2026 framework agreement
 * has two files whose names differ by one digit, and matching on text would
 * hand a reader the wrong contract with full confidence.
 *
 * A contract with no paper renders NOTHING, not an error and not an empty
 * word. Recording what was agreed and filing the PDF are separate acts, and a
 * commercial record entered from an invoice is complete without a file.
 */
function ContractPaper({
  contractId,
  orgId,
}: Readonly<{ contractId: string; orgId: string }>) {
  const t = useT();
  const query = useQuery({
    queryKey: ["contractPaper", orgId, contractId],
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations/{id}/documents", {
        params: {
          path: { id: orgId },
          query: { contract_id: contractId },
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data?.data ?? [];
    },
  });

  // A failed read says nothing here. The row's own commercial facts are
  // already on screen and are what the reader came for; an error chip next to
  // them would report a document problem as though the agreement were doubtful.
  const files = query.data ?? [];
  if (files.length === 0) {
    return null;
  }
  return (
    <>
      {files.map((file) => (
        <a
          key={file.id}
          className="co-rowlink"
          href={`/v1/attachments/${file.id}`}
          download={file.filename}
        >
          {t("contracts.paper")}
        </a>
      ))}
    </>
  );
}

/**
 * ContractTermState is where the row says what is actually true about its
 * dates, which is not always what its status says.
 *
 * The order is deliberate: the pending status change is the most surprising
 * thing a reader can meet, so it leads. A cancellation that has not taken
 * effect comes next, because the customer is still under contract and a card
 * that read as though they had gone would be wrong on the day it matters most.
 * A renewal date is ordinary information and comes last.
 */
function ContractTermState({ contract }: Readonly<{ contract: Contract }>) {
  const t = useT();
  const { locale } = useLocale();

  // Its dates have run out and nobody has moved the status. Saying only
  // "active" here would present an approval queue as a live agreement.
  if (contract.under_contract === false && contract.status === "active") {
    return (
      <span className="t-caption">{t("contracts.endedPendingStatus")}</span>
    );
  }
  if (contract.cancellation_effective_on && contract.under_contract) {
    return (
      <Badge tone="warn">
        {t("contracts.endsOn", {
          when: formatDate(
            contract.cancellation_effective_on,
            locale,
            RECORD_ZONE,
          ),
        })}
      </Badge>
    );
  }
  if (contract.renewal_on) {
    return (
      <span className="t-caption">
        {t("contracts.renewsOn", {
          when: formatDate(contract.renewal_on, locale, RECORD_ZONE),
        })}
      </span>
    );
  }
  return null;
}

// perYearLabel hands the value formatter its translated suffix, so the "/ year"
// a reader sees is in their language rather than Latin shorthand.
function perYearLabel(t: ReturnType<typeof useT>) {
  return (amount: string) => t("contracts.perYear", { amount });
}

// One agreement's value, with the basis said in words.
//
// An annualized figure NEVER renders as a bare amount: a reader who cannot tell
// a three-year total from a per-year figure has been handed a number they will
// misuse, and the row is the last place that distinction can be made.
export function contractValue(
  contract: Contract,
  locale: Locale,
  perYear: (amount: string) => string,
): string {
  if (contract.value_minor == null || !contract.currency) {
    return "";
  }
  const amount = formatMoney(contract.value_minor, contract.currency, locale);
  return contract.value_basis === "annualized_12m" ? perYear(amount) : amount;
}

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type CSSProperties, type ReactNode, useState } from "react";
import { api } from "../api/client";
import { useCan, useCanWrite } from "../app/capability";
import {
  Badge,
  Button,
  Card,
  EmptyState,
  Field,
  Skeleton,
  TextInput,
} from "../design-system/atoms";
import { CardBoundary } from "../design-system/cardboundary";
import { ConfirmModal } from "../design-system/confirmmodal";
import { Panel, PanelBody } from "../design-system/panel";
import { Select } from "../design-system/select";
import { Switch } from "../design-system/switch";
import { useT } from "../i18n";
import {
  problemMessageOf,
  QueryGate,
  QueryStates,
  throwProblem,
  useMe,
} from "./common";
import {
  actionLabelKey,
  effectLabelKey,
  effectReasonKey,
  effectTone,
  parseRetainDays,
  policyEffect,
  RETENTION_ACTIONS,
  RETENTION_POLICIES_KEY,
  RETENTION_SCOPES,
  RETENTION_SETTINGS_KEY,
  type RetentionAction,
  type RetentionPolicy,
  scopeLabelKey,
} from "./retention.logic";
import { RetentionPolicyForm } from "./retentionpolicyform";
import "./retention.css";

// The gap under a panel's own subtitle. `Panel` has no `sub` prop, so the line
// is the body's first paragraph and owes its own separation from the content
// under it; it is a token rather than a number so it moves with the scale, and
// it lives here rather than in a screen sheet because it belongs to the panel
// shape, not to this surface. It folds away the day `Panel` takes a `sub`.
const PANEL_SUB: CSSProperties = { marginBottom: "var(--space-3)" };

// Settings → Privacy → Retention (GCS-WIRE-1..5): the storage-limitation
// ladder an admin now owns, and the retain-only posture that overrides its
// destructive half.
//
// The one thing this screen exists to say: an ENABLED policy can be inert.
// While the posture is on, an `anonymize` or `erase` rule stays exactly as
// authored and does nothing — so every row states what it is doing tonight,
// and why, on the row itself rather than in the nightly job's log.

// The scope's own wire spelling. Kept beside the translated label because it is
// what the audit log, the ops runbook and a support conversation all name the
// policy by — the words are for reading, the identifier is for matching.
function ScopeCell({ policy }: Readonly<{ policy: RetentionPolicy }>) {
  const t = useT();
  return (
    <span className="retention-scope">
      <span>{t(scopeLabelKey(policy.scope))}</span>
      <span className="t-mono t-caption">{policy.scope}</span>
    </span>
  );
}

// A row's two writes are one PATCH on one policy, so they stay one mutation —
// one pending state, one refusal. Where they part company is what a success
// means for the open editor, and `intent` is how the write says which of the two
// it is: inferring it from whichever fields the body happens to carry would make
// a future body field silently change what the panel does.
type PolicyWrite = Readonly<{
  /** `switch` is the Enabled flip; `save` commits the edited window and basis. */
  intent: "save" | "switch";
  body: Readonly<{
    retain_days?: number;
    action?: RetentionAction;
    lawful_basis?: string | null;
    enabled?: boolean;
  }>;
}>;

// One stored policy: what it does tonight, then (on Edit) its editable window,
// action, basis and on/off switch. `scope` is deliberately not editable — a
// different scope is a different policy, which is also why the contract's patch
// body has no scope field.
function PolicyRow({
  policy,
  canEdit,
  canDelete,
  onDelete,
}: Readonly<{
  policy: RetentionPolicy;
  canEdit: boolean;
  canDelete: boolean;
  onDelete: () => void;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [retainDays, setRetainDays] = useState(String(policy.retain_days));
  const [action, setAction] = useState<RetentionAction>(policy.action);
  const [lawfulBasis, setLawfulBasis] = useState(policy.lawful_basis ?? "");

  const patch = useMutation({
    mutationFn: async ({ body }: PolicyWrite) => {
      const { data, error } = await api.PATCH("/retention-policies/{id}", {
        params: { path: { id: policy.id } },
        body,
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (_data, write) => {
      queryClient.invalidateQueries({ queryKey: RETENTION_POLICIES_KEY });
      // Committing the edits is what the panel was opened for, so a save closes
      // it. The switch is a write in its own right and leaves the panel exactly
      // as it was: closing it there would unmount the window, action and basis
      // fields the operator may be mid-edit on — and the switch they just
      // flipped along with them, dropping focus to the document body.
      if (write.intent === "save") {
        setEditing(false);
      }
    },
  });

  const effect = policyEffect(policy);
  const reasonKey = effectReasonKey(effect);
  const days = parseRetainDays(retainDays);

  // Opening the panel re-seeds the three fields from the policy as it now
  // stands. They are local state that outlives a close — the row is keyed on
  // policy.id, so it is never remounted — so without this a draft typed before
  // somebody else moved the window would reappear over a summary that
  // contradicts it, and Save would send the older number.
  function toggleEditor() {
    if (!editing) {
      setRetainDays(String(policy.retain_days));
      setAction(policy.action);
      setLawfulBasis(policy.lawful_basis ?? "");
    }
    setEditing(!editing);
  }

  return (
    <li className="retention-row" data-testid={`retention-row-${policy.scope}`}>
      <div className="retention-summary">
        <ScopeCell policy={policy} />
        <span className="t-small">
          {t("retention.windowDays", { days: policy.retain_days })}
        </span>
        <Badge>{t(actionLabelKey(policy.action))}</Badge>
        <Badge tone={effectTone(effect)}>{t(effectLabelKey(effect))}</Badge>
        {canEdit && (
          <Button small onClick={toggleEditor}>
            {t("retention.edit")}
          </Button>
        )}
      </div>
      {/* Why a row is not acting, in full, unconditionally — not behind the
          Edit toggle and not only for an operator who holds the write grant.
          A reader who cannot change the posture still has to be able to tell a
          suppressed rule from a working one. */}
      {reasonKey && (
        <p className="t-caption retention-reason">{t(reasonKey)}</p>
      )}
      {editing && (
        <Card as="div" inset className="retention-edit">
          <div className="form-stack">
            <Field
              label={t("retention.window")}
              hint={
                days === null && retainDays.trim() !== ""
                  ? t("retention.windowInvalid")
                  : undefined
              }
            >
              {(control) => (
                <TextInput
                  {...control}
                  inputMode="numeric"
                  value={retainDays}
                  onChange={(event) => setRetainDays(event.target.value)}
                />
              )}
            </Field>
            <Field label={t("retention.action")}>
              {(control) => (
                <Select
                  {...control}
                  options={RETENTION_ACTIONS.map((value) => ({
                    value,
                    label: t(actionLabelKey(value)),
                  }))}
                  value={action}
                  onChange={(value) => {
                    const picked = RETENTION_ACTIONS.find(
                      (candidate) => candidate === value,
                    );
                    if (picked) {
                      setAction(picked);
                    }
                  }}
                />
              )}
            </Field>
            <Field label={t("retention.lawfulBasis")}>
              {(control) => (
                <TextInput
                  {...control}
                  value={lawfulBasis}
                  onChange={(event) => setLawfulBasis(event.target.value)}
                />
              )}
            </Field>
            {/* A Switch and not a Checkbox, because flipping it IS the pause:
                there is no Save to press afterwards, and the panel only renders
                for an operator who holds the update grant, so the one thing
                that can make it refuse a press is a write already in flight —
                which explains itself by finishing and needs no `reason`. It is
                `pending` rather than `disabled` precisely because of that: an
                unavailable control and one that is mid-write are different
                facts, and only the second one ends on its own. */}
            <Switch
              label={t("retention.enabled")}
              checked={policy.enabled}
              pending={patch.isPending}
              onChange={(next) =>
                patch.mutate({ intent: "switch", body: { enabled: next } })
              }
            />
            {patch.isError && (
              <p className="t-caption retention-error" role="alert">
                {problemMessageOf(patch.error, t)}
              </p>
            )}
            <div className="retention-actions">
              <Button
                small
                variant="primary"
                disabled={days === null || patch.isPending}
                onClick={() =>
                  days !== null &&
                  patch.mutate({
                    intent: "save",
                    body: {
                      retain_days: days,
                      action,
                      lawful_basis: lawfulBasis.trim() || null,
                    },
                  })
                }
              >
                {t("retention.save")}
              </Button>
              {canDelete && (
                <Button small variant="danger" onClick={onDelete}>
                  {t("retention.delete")}
                </Button>
              )}
            </div>
          </div>
        </Card>
      )}
    </li>
  );
}

// Deleting a policy is not the way to pause one — it drops the rule entirely,
// so records in that scope stop ageing out. The body says so and points at the
// Enabled switch, because that is what the operator usually meant.
function DeletePolicyModal({
  policy,
  onClose,
}: Readonly<{ policy: RetentionPolicy | null; onClose: () => void }>) {
  const t = useT();
  const queryClient = useQueryClient();

  // The staged policy arrives as the mutation's variable, never through this
  // closure: react-query re-arms a mutation's options in a passive effect, so a
  // confirm landing between the commit that stages a row and that effect would
  // otherwise read a stale (null) policy and delete nothing.
  const remove = useMutation({
    mutationFn: async (target: RetentionPolicy) => {
      const { error } = await api.DELETE("/retention-policies/{id}", {
        params: { path: { id: target.id } },
      });
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: RETENTION_POLICIES_KEY });
      onClose();
    },
  });

  function close() {
    onClose();
    remove.reset();
  }

  return (
    <ConfirmModal
      open={policy !== null}
      onClose={close}
      title={t("retention.deleteTitle")}
      confirmLabel={t("retention.delete")}
      confirmVariant="danger"
      onConfirm={() => policy && remove.mutate(policy)}
      pending={remove.isPending}
      error={remove.isError ? problemMessageOf(remove.error, t) : null}
    >
      <p>
        {policy
          ? t("retention.deleteBody", { scope: t(scopeLabelKey(policy.scope)) })
          : ""}
      </p>
    </ConfirmModal>
  );
}

// The posture, and its consequence in the words that matter: with it on the
// installation destroys nothing, and archiving still runs because an archived
// record is kept. Disabled (never hidden) without the update grant — every
// reader of this screen needs to know which posture is in force, and the
// control's `reason` is what tells them why it is theirs to read and not to
// change.
function PostureToggle({
  retainOnly,
  canManage,
}: Readonly<{ retainOnly: boolean; canManage: boolean }>) {
  const t = useT();
  const queryClient = useQueryClient();

  const update = useMutation({
    mutationFn: async (next: boolean) => {
      const { data, error } = await api.PATCH("/retention/settings", {
        body: { retain_only: next },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (data) => {
      queryClient.setQueryData(RETENTION_SETTINGS_KEY, data);
      // Every row's `suppressed_by_posture` is derived from the posture the
      // server just wrote, so the list is stale the moment this lands.
      queryClient.invalidateQueries({ queryKey: RETENTION_POLICIES_KEY });
    },
  });

  return (
    <div className="retention-posture">
      <Switch
        label={t("retention.retainOnly")}
        hint={t("retention.retainOnlyHelp")}
        // Two reasons this control can be unavailable, and only one of them is
        // worth words: a reader who may never change the posture needs to know
        // why, where a write already in flight explains itself by finishing.
        // On the control rather than beside it, so `aria-describedby` carries
        // the explanation to a reader who never sees the paragraph.
        reason={canManage ? undefined : t("retention.adminOnly")}
        checked={retainOnly}
        disabled={!canManage}
        pending={update.isPending}
        onChange={(next) => update.mutate(next)}
      />
      {update.isError && (
        <p className="t-caption retention-error" role="alert">
          {problemMessageOf(update.error, t)}
        </p>
      )}
    </div>
  );
}

export function RetentionCard() {
  const t = useT();
  const me = useMe();
  // Reading the ladder and authoring it are separate grants, and the card gates
  // each affordance on the one it actually needs — a role granted read without
  // update gets a card that tells the truth rather than buttons that 403.
  const canRead = useCan("retention_policy", "read");
  const canManage = useCanWrite("retention_policy", "update");
  const canCreate = useCanWrite("retention_policy", "create");
  const canDelete = useCanWrite("retention_policy", "delete");
  const [adding, setAdding] = useState(false);
  // Which policy is staged for deletion, so ONE modal lives at the card root
  // rather than one per row (share.tsx's revokingId shape).
  const [deleting, setDeleting] = useState<RetentionPolicy | null>(null);

  const settings = useQuery({
    queryKey: RETENTION_SETTINGS_KEY,
    enabled: canRead,
    queryFn: async () => {
      const { data, error } = await api.GET("/retention/settings");
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  const policies = useQuery({
    queryKey: RETENTION_POLICIES_KEY,
    enabled: canRead,
    queryFn: async () => {
      const { data, error } = await api.GET("/retention-policies");
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  // After every hook, so the hook call order stays unconditional.
  //
  // Withheld, not absent: a permission is what denies this, so the card keeps
  // its place and says so. It shares the Privacy & audit page with the consent
  // registry an ops seat comes here for, and beside a subject queue that
  // explains its own emptiness — a card that simply vanished would leave that
  // reader to conclude this installation keeps everything forever.
  //
  // No request either, and that half stands: both queries are `enabled: canRead`
  // because the answer is already known, and asking the server for a 403 in
  // order to render it would turn a settled denial into a failure with a Retry
  // that cannot succeed. Gated on the /me probe itself so the notice waits for
  // the grants rather than flashing while they are in flight.
  if (!canRead) {
    return (
      <Panel title={t("retention.title")}>
        <PanelBody>
          <p className="t-sub" style={PANEL_SUB}>
            {t("retention.sub")}
          </p>
          <QueryGate query={me}>
            {() => (
              <EmptyState>
                <p className="t-small">{t("retention.withheld")}</p>
              </EmptyState>
            )}
          </QueryGate>
        </PanelBody>
      </Panel>
    );
  }

  // No bottom margin of its own: `.settings-stack` owns the gap between cards.
  return (
    <Panel
      title={t("retention.title")}
      titleAction={
        canCreate ? (
          <Button small onClick={() => setAdding((open) => !open)}>
            {t("retention.addPolicy")}
          </Button>
        ) : undefined
      }
    >
      <PanelBody>
        <p className="t-sub" style={PANEL_SUB}>
          {t("retention.sub")}
        </p>
        <CardBoundary>
          {settings.isPending ? (
            <Skeleton width="70%" />
          ) : settings.isError ? (
            <p className="t-caption retention-error" role="alert">
              {problemMessageOf(settings.error, t)}
            </p>
          ) : (
            <PostureToggle
              retainOnly={settings.data.retain_only}
              canManage={canManage}
            />
          )}

          {adding && <RetentionPolicyForm onDone={() => setAdding(false)} />}

          <PolicyList
            query={policies}
            canEdit={canManage}
            canDelete={canDelete}
            onDelete={setDeleting}
          />

          <DeletePolicyModal
            policy={deleting}
            onClose={() => setDeleting(null)}
          />
        </CardBoundary>
      </PanelBody>
    </Panel>
  );
}

// The list's own honest state matrix. Split out so RetentionCard reads as the
// composition it is; the posture block above it has its own states because a
// posture that failed to load must not hide a policy list that did.
function PolicyList({
  query,
  canEdit,
  canDelete,
  onDelete,
}: Readonly<{
  query: Readonly<{
    isPending: boolean;
    isError: boolean;
    error: unknown;
    data?: Readonly<{ data: RetentionPolicy[] }>;
    refetch: () => unknown;
  }>;
  canEdit: boolean;
  canDelete: boolean;
  onDelete: (policy: RetentionPolicy) => void;
}>) {
  const t = useT();
  let body: ReactNode = null;
  if (query.data) {
    // Sorted by the authorable enum's order, not by the server's row order: the
    // ladder reads the same on every visit and after every edit.
    const rows = [...query.data.data].sort(
      (left, right) =>
        RETENTION_SCOPES.indexOf(left.scope) -
        RETENTION_SCOPES.indexOf(right.scope),
    );
    body =
      rows.length === 0 ? (
        <EmptyState>{t("retention.empty")}</EmptyState>
      ) : (
        <ul className="retention-list">
          {rows.map((policy) => (
            <PolicyRow
              key={policy.id}
              policy={policy}
              canEdit={canEdit}
              canDelete={canDelete}
              onDelete={() => onDelete(policy)}
            />
          ))}
        </ul>
      );
  }
  // The shared spelling of the loading and failure rungs, rather than a third
  // hand-rolled copy: the skeleton announces itself as busy, and the failure is
  // an assertive live region carrying the server's own explanation beside the
  // retry. The hand-rolled version said neither out loud.
  return <QueryStates query={query}>{body}</QueryStates>;
}

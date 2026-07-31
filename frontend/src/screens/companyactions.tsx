import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useT } from "../i18n";
import { problemMessage } from "./common";
import { CreateAction, type CreateField } from "./create";

// What a rep can START from the company page.
//
// The page could already say everything about an account and do nothing with
// it: opening a deal meant leaving for the pipeline board and re-finding the
// company there, and applying a tag or adding the account to a list had no
// control at all. Each action below is the record's OWN verb — the company is
// the subject, so it is never something the rep has to pick.

type Pipeline = components["schemas"]["Pipeline"];
type Stage = components["schemas"]["Stage"];

/**
 * useDealTarget resolves where a deal created from this page should land: the
 * default pipeline and its first OPEN stage.
 *
 * Open only. A deal is born open (INV-CLOSE-PAST), so won and lost are reached
 * through the confirmed advance and must never be a create-time choice — the
 * deal board applies the same rule to its own stage picker.
 */
function useDealTarget() {
  return useQuery({
    queryKey: ["pipelines", "dealTarget"],
    queryFn: async () => {
      const { data, error } = await api.GET("/pipelines", {
        params: { query: {} },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      const pipeline: Pipeline | undefined =
        data.data.find((candidate) => candidate.is_default) ?? data.data[0];
      const stages: Stage[] = pipeline?.stages ?? [];
      const open = stages.filter((stage) => stage.semantic === "open");
      return { pipeline, open };
    },
  });
}

/**
 * NewDealAction opens a deal on THIS company.
 *
 * The organization is not a field: the rep is standing on the record, so
 * asking them to name it again is asking them to confirm where they already
 * are — and it is the one value they could get wrong.
 */
export function NewDealAction({
  orgId,
  orgName,
}: Readonly<{ orgId: string; orgName: string }>) {
  const t = useT();
  const target = useDealTarget();
  const pipeline = target.data?.pipeline;
  const open = target.data?.open ?? [];
  // No pipeline, or none of its stages is open, means there is nowhere for a
  // deal to land. A button that can only fail is worse than no button.
  if (!pipeline || open.length === 0) {
    return null;
  }

  const fields: CreateField[] = [
    { key: "name", label: "create.dealName", required: true },
    { key: "amount", label: "create.amount", type: "number" },
    {
      key: "currency",
      label: "create.currency",
      type: "select",
      required: true,
      options: ["EUR", "USD", "GBP", "CHF"].map((code) => ({
        value: code,
        label: code,
      })),
    },
    {
      key: "stage_id",
      label: "create.stage",
      type: "select",
      required: true,
      options: open.map((stage) => ({ value: stage.id, label: stage.name })),
    },
    { key: "expected_close_date", label: "create.expectedClose", type: "date" },
  ];

  const createDeal = async (values: Record<string, string>) => {
    const amount = values.amount?.trim();
    const { data, error } = await api.POST("/deals", {
      body: {
        name: values.name.trim(),
        pipeline_id: pipeline.id,
        stage_id: values.stage_id,
        // The form takes major units; the wire is minor units.
        amount_minor: amount ? Math.round(Number(amount) * 100) : null,
        currency: values.currency || "EUR",
        organization_id: orgId,
        expected_close_date: values.expected_close_date || null,
        source: "manual",
      },
    });
    if (error) {
      throw new Error(problemMessage(error, t));
    }
    return data;
  };

  return (
    <CreateAction
      label={t("co.deal.new", { name: orgName })}
      invalidate="organization360"
      screen="deals"
      create={createDeal}
      fields={fields}
    />
  );
}

/**
 * TagAction puts a tag on this company, creating the tag when the name is new.
 *
 * One field, one name typed. Splitting it into "pick an existing tag" and
 * "make a new one" makes the rep answer a question about the workspace's tag
 * table before they can answer the one they actually have — and on a fresh
 * workspace the pick-only version has nothing to offer, so the control
 * disappears exactly when it is first needed.
 *
 * Matching is case-insensitive on the trimmed name, because "VIP" and "vip"
 * are one tag to everyone except the database.
 */
export function TagAction({ orgId }: Readonly<{ orgId: string }>) {
  const t = useT();

  const addTag = async (values: Record<string, string>) => {
    const name = values.name.trim();
    // Read the tags AT SUBMIT, never from a cache the component loaded
    // earlier. A first attempt that creates the tag and then fails to apply it
    // leaves a tag the cached list does not have, so a retry would mint a
    // second one under the same name — the duplicate this matching exists to
    // prevent, produced by the retry itself.
    const { data: known, error: readError } = await api.GET("/tags", {
      params: { query: {} },
    });
    if (readError) {
      throw new Error(problemMessage(readError, t));
    }
    const existing = known.data.find(
      (tag) => tag.name.trim().toLowerCase() === name.toLowerCase(),
    );
    let tagId = existing?.id;
    if (!tagId) {
      const { data, error } = await api.POST("/tags", { body: { name } });
      if (error) {
        throw new Error(problemMessage(error, t));
      }
      tagId = data.id;
    }
    const { data, error, response } = await api.POST("/tags/{id}/apply", {
      params: { path: { id: tagId } },
      body: { entity_type: "organization", entity_id: orgId },
    });
    // Already on this company is the state the rep asked for, so it is not an
    // error to report at them. The server says 409 because the row exists;
    // what the rep wanted was the tag present, and it is.
    if (response.status === 409) {
      return { id: orgId };
    }
    if (error) {
      throw new Error(problemMessage(error, t));
    }
    return { id: data.id };
  };

  return (
    <CreateAction
      label={t("co.tags.apply")}
      invalidate="organization360"
      screen="companies"
      stay
      create={addTag}
      fields={[{ key: "name", label: "co.tags.pick", required: true }]}
    />
  );
}

/**
 * ListAction adds this company to a static list, creating the list when the
 * name is new — the same one-field shape as TagAction, for the same reason.
 *
 * Only STATIC lists are matched or made. A dynamic segment is a stored filter
 * with no membership to add to: a row belongs to one exactly when it matches,
 * so offering one here would present a write the server must refuse.
 */
export function ListAction({ orgId }: Readonly<{ orgId: string }>) {
  const t = useT();

  const addToList = async (values: Record<string, string>) => {
    const name = values.name.trim();
    // Read at submit, for the reason spelled out in TagAction: a retry after a
    // half-completed attempt must find what that attempt created.
    const { data: known, error: readError } = await api.GET("/lists", {
      params: { query: { entity_type: "organization" } },
    });
    if (readError) {
      throw new Error(problemMessage(readError, t));
    }
    const existing = known.data.find(
      (list) =>
        list.list_type === "static" &&
        list.name.trim().toLowerCase() === name.toLowerCase(),
    );
    let listId = existing?.id;
    if (!listId) {
      const { data, error } = await api.POST("/lists", {
        body: { name, entity_type: "organization", list_type: "static" },
      });
      if (error) {
        throw new Error(problemMessage(error, t));
      }
      listId = data.id;
    }
    const { data, error, response } = await api.POST("/lists/{id}/members", {
      params: { path: { id: listId } },
      body: { entity_type: "organization", entity_id: orgId },
    });
    // Already a member is the asked-for state, not a failure. See TagAction.
    if (response.status === 409) {
      return { id: orgId };
    }
    if (error) {
      throw new Error(problemMessage(error, t));
    }
    return { id: data.id };
  };

  return (
    <CreateAction
      label={t("co.lists.add")}
      invalidate="organization360"
      screen="companies"
      stay
      create={addToList}
      fields={[{ key: "name", label: "co.lists.pick", required: true }]}
    />
  );
}

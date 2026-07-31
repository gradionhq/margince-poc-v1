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
/**
 * resolveTagId turns a typed name into the id of the ONE tag that carries it,
 * creating it when the workspace has none.
 *
 * Two collisions are resolved rather than reported, because neither is
 * something the rep did or can act on:
 *
 *   - the read finds nothing but the create answers 409, meaning somebody
 *     created the same name in between. tag names are unique per workspace
 *     (uq_tag_name on lower(name)), so THEIR row is the asked-for tag;
 *   - the name matches case-insensitively, because "VIP" and "vip" are one
 *     tag to everyone except the database.
 *
 * The catalog is read at call time, never from a cache the component loaded
 * earlier: a first attempt that created the tag and then failed to apply it
 * leaves a tag no snapshot knows about, so a retry would mint a second one.
 */
async function resolveTagId(
  name: string,
  t: ReturnType<typeof useT>,
): Promise<string> {
  const matching = (tags: readonly { id: string; name: string }[]) =>
    tags.find((tag) => tag.name.trim().toLowerCase() === name.toLowerCase())
      ?.id;

  const { data: known, error: readError } = await api.GET("/tags", {
    params: { query: {} },
  });
  if (readError) {
    throw new Error(problemMessage(readError, t));
  }
  const existing = matching(known.data);
  if (existing) {
    return existing;
  }

  const { data, error, response } = await api.POST("/tags", { body: { name } });
  if (response.status !== 409) {
    if (error) {
      throw new Error(problemMessage(error, t));
    }
    return data.id;
  }

  const { data: after, error: afterError } = await api.GET("/tags", {
    params: { query: {} },
  });
  if (afterError) {
    throw new Error(problemMessage(afterError, t));
  }
  const winner = matching(after.data);
  if (!winner) {
    // A 409 with no row carrying the name means the collision was about
    // something other than the name, and this function has no answer for it.
    throw new Error(problemMessage(error, t));
  }
  return winner;
}

/**
 * TagAction puts a tag on this company, creating the tag when the name is new.
 *
 * One field, one name typed. Splitting it into "pick an existing tag" and
 * "make a new one" makes the rep answer a question about the workspace's tag
 * table before they can answer the one they actually have — and on a fresh
 * workspace the pick-only version has nothing to offer, so the control
 * disappears exactly when it is first needed.
 */
export function TagAction({ orgId }: Readonly<{ orgId: string }>) {
  const t = useT();

  const addTag = async (values: Record<string, string>) => {
    const tagId = await resolveTagId(values.name.trim(), t);
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
    //
    // This narrows the race; it does not close it. `list` carries no
    // uniqueness on its name (unlike `tag`, which has uq_tag_name), so two
    // people typing the same new list name at the same moment get two lists
    // and no server error to resolve them. Closing it needs a constraint or a
    // find-or-create endpoint, neither of which a client can supply. The
    // damage is a duplicate list a human can merge, not a lost membership.
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

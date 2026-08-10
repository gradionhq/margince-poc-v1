import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Card, SectionHeader } from "../design-system/atoms";
import { useT } from "../i18n";
import { problemMessageOf, throwProblem } from "./common";

type Graph = components["schemas"]["PersonGraph"];
type Node = components["schemas"]["PersonGraphNode"];
type Edge = components["schemas"]["PersonGraphEdge"];

/**
 * usePersonGraph reads the local graph around one contact.
 *
 * Separate from the 360 on purpose: it answers a different question, it is only
 * asked when the reader opens Connections, and loading it with the record page
 * would make every person open slower for an answer most opens never need.
 */
export function usePersonGraph(id: string) {
  return useQuery({
    queryKey: ["person-graph", id],
    queryFn: async () => {
      const { data, error } = await api.GET("/people/{id}/graph", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
}

/**
 * PersonGraphPanel answers "who can open a door to this person, and through
 * whom".
 *
 * The recommended route leads, because it is the answer; the node list is the
 * working underneath it. Nodes are real buttons and selecting one drives a
 * live detail region — the graph is something the reader interrogates, not a
 * decoration to look at.
 */
export function PersonGraphPanel({ personId }: Readonly<{ personId: string }>) {
  const t = useT();
  const graph = usePersonGraph(personId);
  const [selected, setSelected] = useState<string | undefined>();

  if (graph.isPending) {
    return <p>{t("person.graph.loading")}</p>;
  }
  if (graph.isError) {
    return (
      <p role="alert" style={{ color: "var(--danger)" }}>
        {problemMessageOf(graph.error, t)}
      </p>
    );
  }
  // The arrays are required by the contract, and this guard is still load
  // bearing: a proxy error page, a version-skewed server, or a caller that
  // never reached the handler all deliver an object without them. Rendering
  // nothing beats taking the whole record page down with a TypeError.
  const data = graph.data;
  if (!data?.nodes) {
    return null;
  }
  const nodes = data.nodes;

  const anchor = nodes.find((n) => n.group === "anchor");
  const direct = nodes.filter((n) => n.group === "direct");
  const account = nodes.filter((n) => n.group === "account");

  return (
    <div style={{ display: "grid", gap: "var(--space-4)" }}>
      <RouteCard graph={data} />

      <Card>
        <div style={{ padding: "var(--space-4)" }}>
          <SectionHeader
            title={t("person.graph.direct")}
            sub={t("person.graph.directSub")}
          />
          <NodeList
            nodes={direct}
            selected={selected}
            onSelect={setSelected}
            emptyLabel={t("person.graph.noDirect")}
          />
          {omitted(data, "direct") && (
            <p style={{ margin: "var(--space-2) 0 0", opacity: 0.75 }}>
              {t("person.graph.omitted")}
            </p>
          )}
        </div>
      </Card>

      <Card>
        <div style={{ padding: "var(--space-4)" }}>
          <SectionHeader
            title={t("person.graph.account")}
            sub={t("person.graph.accountSub")}
          />
          <NodeList
            nodes={account}
            selected={selected}
            onSelect={setSelected}
            emptyLabel={t("person.graph.noAccount")}
          />
          {omitted(data, "account") && (
            <p style={{ margin: "var(--space-2) 0 0", opacity: 0.75 }}>
              {t("person.graph.omitted")}
            </p>
          )}
        </div>
      </Card>

      {/* The detail region is aria-live because selection changes it without
          moving focus: a reader on a screen reader would otherwise press a
          node and be told nothing happened. */}
      <div aria-live="polite">
        {selected && anchor && (
          <EdgeDetail graph={data} nodeId={selected} anchorId={anchor.id} />
        )}
      </div>

      <DroppedNote graph={data} />
    </div>
  );
}

function omitted(graph: Graph, group: string): boolean {
  return (graph.groups_omitted ?? []).some((g) => g === group);
}

/**
 * RouteCard is the answer: the warmest way in and the evidence for it. It is
 * first because a reader who reads nothing else should still leave knowing who
 * to ask.
 */
function RouteCard({ graph }: Readonly<{ graph: Graph }>) {
  const t = useT();
  const route = graph.route;
  if (!route) {
    return (
      <Card>
        <div style={{ padding: "var(--space-4)" }}>
          <SectionHeader title={t("person.graph.routeTitle")} />
          <p style={{ margin: "var(--space-2) 0 0", lineHeight: 1.5 }}>
            {t("person.graph.noRoute")}
          </p>
        </div>
      </Card>
    );
  }
  return (
    <Card>
      <div style={{ padding: "var(--space-4)" }}>
        <SectionHeader title={t("person.graph.routeTitle")} />
        <p style={{ margin: "var(--space-2) 0 0", lineHeight: 1.5 }}>
          {route.through_display_name
            ? t("person.graph.routeVia", {
                name: route.via_display_name,
                through: route.through_display_name,
              })
            : t("person.graph.routeDirect", { name: route.via_display_name })}
        </p>
        <p
          style={{
            margin: "var(--space-1) 0 0",
            fontSize: "0.9rem",
            opacity: 0.8,
          }}
        >
          {route.why}
        </p>
      </div>
    </Card>
  );
}

/**
 * NodeList renders each node as a real button with aria-pressed. The diagram
 * is the accessible list — there is no separate decorative rendering that
 * could disagree with it.
 */
function NodeList({
  nodes,
  selected,
  onSelect,
  emptyLabel,
}: Readonly<{
  nodes: Node[];
  selected: string | undefined;
  onSelect: (id: string) => void;
  emptyLabel: string;
}>) {
  if (nodes.length === 0) {
    return (
      <p style={{ margin: "var(--space-2) 0 0", opacity: 0.75 }}>
        {emptyLabel}
      </p>
    );
  }
  return (
    <ul
      style={{
        display: "flex",
        flexWrap: "wrap",
        gap: "var(--space-2)",
        margin: "var(--space-3) 0 0",
        padding: 0,
        listStyle: "none",
      }}
    >
      {nodes.map((node) => (
        <li key={node.id}>
          <button
            type="button"
            className="btn-ghost btn-small"
            aria-pressed={selected === node.id}
            onClick={() => onSelect(node.id)}
          >
            {node.label}
            {node.sublabel ? ` · ${node.sublabel}` : ""}
          </button>
        </li>
      ))}
    </ul>
  );
}

/**
 * EdgeDetail shows what the selected node's relationship is actually made of.
 *
 * The receipts are the point: pooled counts say a route exists, and the
 * messages say the reader is not being asked to trust a number. An edge that
 * carries none says so rather than rendering an empty list — on the account
 * arm that absence is the disclosure rule working, not missing data.
 */
function EdgeDetail({
  graph,
  nodeId,
  anchorId,
}: Readonly<{ graph: Graph; nodeId: string; anchorId: string }>) {
  const t = useT();
  const node = graph.nodes?.find((n) => n.id === nodeId);
  const edges = (graph.edges ?? []).filter(
    (e) => e.from === nodeId || e.to === nodeId,
  );
  if (!node || edges.length === 0) {
    return (
      <Card>
        <div style={{ padding: "var(--space-4)" }}>
          <p style={{ margin: 0 }}>
            {t("person.graph.noEdge", { name: node?.label ?? "" })}
          </p>
        </div>
      </Card>
    );
  }
  return (
    <Card>
      <div style={{ padding: "var(--space-4)" }}>
        <SectionHeader title={node.label} />
        {edges.map((edge) => (
          <EdgeFacts
            key={`${edge.from}->${edge.to}`}
            edge={edge}
            graph={graph}
            anchorId={anchorId}
          />
        ))}
      </div>
    </Card>
  );
}

function EdgeFacts({
  edge,
  graph,
  anchorId,
}: Readonly<{ edge: Edge; graph: Graph; anchorId: string }>) {
  const t = useT();
  const otherEnd = edge.to === anchorId ? undefined : edge.to;
  const otherLabel = graph.nodes?.find((n) => n.id === otherEnd)?.label;
  const receipts = edge.receipts ?? [];
  return (
    <div style={{ marginTop: "var(--space-3)" }}>
      <p
        style={{
          margin: 0,
          display: "flex",
          gap: "var(--space-2)",
          alignItems: "center",
        }}
      >
        <Badge>{t(`person.band.${edge.strength_bucket}`)}</Badge>
        <span style={{ fontSize: "0.9rem" }}>
          {otherLabel
            ? t("person.graph.withColleague", { name: otherLabel })
            : t("person.graph.withContact")}
        </span>
      </p>
      <p
        style={{
          margin: "var(--space-1) 0 0",
          fontSize: "0.9rem",
          opacity: 0.8,
        }}
      >
        {t("person.graph.counts", {
          total: String(edge.interactions_90d),
          inbound: String(edge.inbound_90d ?? 0),
          outbound: String(edge.outbound_90d ?? 0),
        })}
      </p>
      {receipts.length > 0 ? (
        <ul
          style={{
            margin: "var(--space-2) 0 0",
            paddingLeft: "var(--space-4)",
            fontSize: "0.9rem",
          }}
        >
          {receipts.map((r) => (
            <li key={r.activity_id}>
              {r.subject ?? t("person.graph.untitledMessage")} ·{" "}
              {new Date(r.occurred_at).toLocaleDateString()}
            </li>
          ))}
        </ul>
      ) : (
        <p
          style={{
            margin: "var(--space-2) 0 0",
            fontSize: "0.9rem",
            opacity: 0.75,
          }}
        >
          {t("person.graph.countsOnly")}
        </p>
      )}
    </div>
  );
}

/**
 * DroppedNote says what the picture is NOT showing. Silent truncation reads as
 * "this is everyone", which is the one thing a graph must never imply falsely.
 */
function DroppedNote({ graph }: Readonly<{ graph: Graph }>) {
  const t = useT();
  const dropped =
    (graph.dropped_count?.direct ?? 0) + (graph.dropped_count?.account ?? 0);
  if (dropped === 0) {
    return null;
  }
  return (
    <p style={{ margin: 0, fontSize: "0.9rem", opacity: 0.75 }}>
      {t("person.graph.dropped", { count: String(dropped) })}
    </p>
  );
}

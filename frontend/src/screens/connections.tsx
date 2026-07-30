// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Avatar,
  Badge,
  Button,
  Modal,
  SectionHeader,
  Skeleton,
} from "../design-system/atoms";
import { useT } from "../i18n";
import { problemMessage } from "./common";
import "./connections.css";
import { EntityRef } from "./entityref";

// The connections card: this account's one-hop neighbourhood — its contacts,
// its open deals and their stakeholders, its parent, children and partner
// companies, and which contact the active signal's warm-intro path routes
// through.
//
// Two renderings of ONE payload, and the list is the primary one. The ego
// diagram is a glance: it is `aria-hidden` and carries no interaction, because
// a hand-rolled SVG is not something a screen reader or a keyboard can be
// given a good experience in. The list underneath holds every node the
// diagram draws, in the same order, each routing through EntityRef on the
// label the payload already carries — so everything the picture shows can be
// reached and opened without it, and without a second request per node.

type Graph = components["schemas"]["OrganizationGraph"];
type GraphNode = components["schemas"]["OrganizationGraphNode"];
type GraphEdge = components["schemas"]["OrganizationGraphEdge"];
type Group = Graph["groups_omitted"][number];

/**
 * RelationKey is the closed set of relations a node can hold to the account,
 * and it is the second half of each `co.connections.rel.*` catalog key.
 *
 * Spelled as a union rather than derived from the edge kind, because two kinds
 * split by direction and one does not: a mismatch between this and the catalog
 * fails to compile instead of rendering a raw key at a reader.
 */
type RelationKey =
  | "employment"
  | "has_deal"
  | "deal_stakeholder"
  | "parent"
  | "child"
  | "partner_of.counterparty"
  | "partner_of.owner"
  | "referred_by.counterparty"
  | "referred_by.owner"
  | "co_sell_with";

/** useOrganizationGraph reads the account's one-hop connections. */
export function useOrganizationGraph(id: string) {
  return useQuery({
    queryKey: ["organization-graph", id],
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations/{id}/graph", {
        params: { path: { id } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
}

// The diagram's geometry, in the SVG's own coordinate space. One place, so the
// collapsed rail view and the expanded modal are the same picture at two
// scales rather than two layouts that drift.
const VIEW = 220;
const CENTRE = VIEW / 2;
const RING = 84;
const ROOT_RADIUS = 13;
const NODE_RADIUS = 8;

// placed is one node with the point the layout put it at.
type Placed = { node: GraphNode; x: number; y: number };

/**
 * layout places the neighbours on a fixed ring around the account.
 *
 * Fixed, not force-directed: the payload's node order is deterministic, so a
 * fixed ring makes the picture deterministic too — the same account draws the
 * same diagram on every read, and a rep learns where to look. A force
 * simulation would move every node whenever one arrived.
 *
 * The ring follows the payload order, which groups a deal next to its own
 * stakeholders, so a stakeholder edge is usually a short hop between
 * neighbours instead of a chord across the middle.
 */
export function layout(nodes: readonly GraphNode[]): Placed[] {
  const root = nodes.find((node) => node.root);
  const ring = nodes.filter((node) => !node.root);
  const placed: Placed[] = [];
  if (root) {
    placed.push({ node: root, x: CENTRE, y: CENTRE });
  }
  ring.forEach((node, index) => {
    // Start at twelve o'clock and go clockwise, so the first node in the
    // payload — the strongest contact — is the one at the top.
    const angle = (index / ring.length) * 2 * Math.PI - Math.PI / 2;
    placed.push({
      node,
      x: CENTRE + RING * Math.cos(angle),
      y: CENTRE + RING * Math.sin(angle),
    });
  });
  return placed;
}

/**
 * EgoDiagram draws the account at the centre with its neighbours on the ring.
 *
 * It is decorative: `aria-hidden`, no focusable element, no click target.
 * Everything in it is in the node list beside it, which is where reaching a
 * record actually happens.
 */
function EgoDiagram({ graph }: Readonly<{ graph: Graph }>) {
  const placed = layout(graph.nodes);
  const at = new Map(placed.map((p) => [p.node.id, p]));
  // Logos that failed to load. A node then keeps its kind colour and reads as
  // an ordinary node, which is the same floor the list's monogram gives it —
  // never a pale empty disc where a company should be.
  const [broken, setBroken] = useState<ReadonlySet<string>>(new Set());
  return (
    <svg
      className="cx-diagram"
      viewBox={`0 0 ${VIEW} ${VIEW}`}
      aria-hidden="true"
      focusable="false"
    >
      {graph.edges.map((edge) => {
        const from = at.get(edge.from);
        const to = at.get(edge.to);
        // The server guarantees both ends are nodes; a payload where they are
        // not is one this build does not understand, and a line to nowhere
        // would draw as a stray stroke off the canvas.
        if (!from || !to) {
          return null;
        }
        return (
          <line
            key={edgeKey(edge)}
            className={`cx-edge cx-edge-${edge.kind}`}
            x1={from.x}
            y1={from.y}
            x2={to.x}
            y2={to.y}
          />
        );
      })}
      {placed.map((p) => {
        const r = p.node.root ? ROOT_RADIUS : NODE_RADIUS;
        const logo = p.node.logo_url && !broken.has(p.node.logo_url);
        return (
          <g key={p.node.id}>
            <circle
              className={nodeClass(p.node, Boolean(logo))}
              cx={p.x}
              cy={p.y}
              r={r}
            />
            {logo && p.node.logo_url && (
              <>
                {/* One clip per node: a clipPath is defined in the diagram's
                    own coordinates, so a shared one would clip every logo to
                    a single node's position. */}
                <clipPath id={`cx-clip-${p.node.id}`}>
                  <circle cx={p.x} cy={p.y} r={r - 1} />
                </clipPath>
                {/* The circle underneath keeps its fill and its ring, so a
                    company whose image never loads is still a drawn node
                    rather than a hole in the diagram. */}
                <image
                  className="cx-node-logo"
                  href={p.node.logo_url}
                  clipPath={`url(#cx-clip-${p.node.id})`}
                  x={p.x - r + 2}
                  y={p.y - r + 2}
                  width={(r - 2) * 2}
                  height={(r - 2) * 2}
                  preserveAspectRatio="xMidYMid meet"
                  onError={() =>
                    setBroken((was) =>
                      p.node.logo_url ? new Set(was).add(p.node.logo_url) : was,
                    )
                  }
                />
              </>
            )}
          </g>
        );
      })}
    </svg>
  );
}

// edgeKey identifies one edge for React. Two records can be joined by more
// than one edge — a company that is both this account's parent and its
// reseller — so the kind is part of the key.
function edgeKey(edge: GraphEdge): string {
  return `${edge.kind}:${edge.from}:${edge.to}`;
}

// nodeClass carries the node's kind, whether it is the centre, and whether it
// is on the intro path into CSS, so the diagram's palette lives in the
// stylesheet rather than in inline attributes.
function nodeClass(node: GraphNode, hasLogo: boolean): string {
  const classes = ["cx-node", `cx-node-${node.kind}`];
  if (node.root) {
    classes.push("cx-node-root");
  }
  if (node.intro_path) {
    classes.push("cx-node-intro");
  }
  if (hasLogo) {
    // A company whose logo IS being drawn needs the same neutral backing the
    // avatar chip gives it elsewhere: a mark drawn on transparency would
    // otherwise read against the node's own dark fill. Keyed off the drawing,
    // not off the field — a node whose image failed keeps its kind colour
    // instead of becoming a pale empty disc.
    classes.push("cx-node-marked");
  }
  return classes.join(" ");
}

/**
 * relationKeys names how one node attaches to the account, read off the EDGES
 * rather than guessed from the node's kind.
 *
 * The kind alone is not the relation, and the difference is the thing a rep
 * acts on: a contact may be an employee, a stakeholder on a deal, or both; a
 * company may be the parent, a subsidiary, or a reseller. The diagram carries
 * that in the line it draws, so the list has to carry it in words — otherwise
 * a reader who cannot see the picture learns WHO is attached and never HOW.
 *
 * Every edge kind describes its `to` end. `employment`, `has_deal` and
 * `deal_stakeholder` say nothing about their `from` end that the reader needs,
 * so a node sitting there gets no label from them — a deal is not "a
 * stakeholder" because a stakeholder edge leaves it.
 *
 * On the remaining kinds both ends mean something and the direction is what
 * says which. The hierarchy edge runs parent → child. A partner edge runs from
 * the organization that RECORDS it to its counterparty, so `referred_by`
 * pointing at this account means that company referred it, while pointing away
 * means this account referred them — calling both "referral" would lose which.
 * `co_sell_with` is the one partner edge whose sides read the same, so it gets
 * one label rather than two that would say the same thing twice.
 */
export function relationKeys(graph: Graph, nodeId: string): RelationKey[] {
  const keys: RelationKey[] = [];
  for (const edge of graph.edges) {
    const key = relationKey(edge, nodeId);
    if (key) {
      keys.push(key);
    }
  }
  // A person holding two seats on two drawn deals is a stakeholder once as far
  // as the reader is concerned; the repeated word adds nothing.
  return [...new Set(keys)];
}

// relationKey is one edge's label for one node, or null when that edge says
// nothing about it — either because the node is not on the edge at all, or
// because the node is at the end the edge does not describe.
function relationKey(edge: GraphEdge, nodeId: string): RelationKey | null {
  const pointsAtNode = edge.to === nodeId;
  if (!pointsAtNode && edge.from !== nodeId) {
    return null;
  }
  switch (edge.kind) {
    case "parent_of":
      return pointsAtNode ? "child" : "parent";
    case "partner_of":
    case "referred_by":
      // `counterparty` is the far end the recording organization points at;
      // `owner` is the organization whose row carries the edge.
      return `${edge.kind}.${pointsAtNode ? "counterparty" : "owner"}`;
    case "co_sell_with":
      return edge.kind;
    default:
      return pointsAtNode ? edge.kind : null;
  }
}

/**
 * NodeList is the card's accessible content: every node the diagram draws, in
 * the same order, each reachable by keyboard through EntityRef's own link and
 * each naming how it attaches to the account.
 *
 * The root is left out — it is the record the reader is already on, and a
 * link back to the current page is a dead end that costs a tab stop.
 */
function NodeList({ graph }: Readonly<{ graph: Graph }>) {
  const t = useT();
  const neighbours = graph.nodes.filter((node) => !node.root);
  if (neighbours.length === 0) {
    return <p className="co-empty">{t("co.connections.empty")}</p>;
  }
  return (
    <ul className="co-list cx-nodes">
      {neighbours.map((node) => (
        <li key={node.id} className="co-row">
          <span className="cx-node-name avatar-row">
            {node.kind === "organization" && (
              <Avatar name={node.label} src={node.logo_url} tinted />
            )}
            {/* The label comes off THIS payload. EntityRef would otherwise
                fetch each record's name — one request per visible node, with
                the raw id showing until it lands — for names the graph read
                already returned. */}
            <EntityRef kind={node.kind} id={node.id} name={node.label} />
            {node.intro_path && (
              <Badge tone="accent">{t("co.connections.introPath")}</Badge>
            )}
          </span>
          <span className="co-row-meta">
            {relationKeys(graph, node.id).map((key) => (
              <span key={key} className="cx-relation">
                {t(`co.connections.rel.${key}`)}
              </span>
            ))}
            {node.detail && <span>{node.detail}</span>}
            {node.strength != null && node.strength_bucket && (
              <Badge tone={strengthTone(node.strength_bucket)}>
                {node.strength}
              </Badge>
            )}
          </span>
        </li>
      ))}
    </ul>
  );
}

// strengthTone maps the server's band onto a badge tone. The band is the
// server's word; nothing here re-derives one from the score.
function strengthTone(
  bucket: NonNullable<GraphNode["strength_bucket"]>,
): "success" | "accent" | undefined {
  if (bucket === "strong") {
    return "success";
  }
  if (bucket === "warm") {
    return "accent";
  }
  return undefined;
}

/**
 * Withheld names the groups the caller's role could not read.
 *
 * Named rather than silently absent: a company whose contacts are hidden and
 * a company with no contacts draw the same empty ring, and only one of them is
 * a fact about the account.
 */
function Withheld({ groups }: Readonly<{ groups: readonly Group[] }>) {
  const t = useT();
  if (groups.length === 0) {
    return null;
  }
  return (
    <p className="co-restricted">
      {t("co.connections.withheld", {
        groups: groups
          .map((group) => t(`co.connections.group.${group}`))
          .join(", "),
      })}
    </p>
  );
}

/**
 * ConnectionsCard is the rail's connection view: the diagram, the node list,
 * what was withheld, what the caps left out, and a way to see it larger.
 *
 * It reads its own endpoint rather than riding the 360, so it owns the states
 * the 360's sections get from their payload: a failed read is unavailable, and
 * only a successful one may say the account has no connections.
 */
export function ConnectionsCard({ orgId }: Readonly<{ orgId: string }>) {
  const t = useT();
  const [expanded, setExpanded] = useState(false);
  const query = useOrganizationGraph(orgId);
  const graph = query.data;
  // A payload without nodes is a graph this build cannot read, not an account
  // with no connections — the same distinction every card on this page keeps.
  const readable = Array.isArray(graph?.nodes) ? graph : undefined;
  const unreadable = !query.isPending && !query.isError && !readable;

  return (
    <section className="card co-card cx-card">
      <SectionHeader title={t("co.connections.title")} />
      {query.isPending && <Skeleton width="100%" height={120} />}
      {(query.isError || unreadable) && (
        <p className="co-restricted">{t("co.section.unavailable")}</p>
      )}
      {readable && (
        <>
          <EgoDiagram graph={readable} />
          <NodeList graph={readable} />
          <Withheld groups={readable.groups_omitted} />
          {readable.dropped_count > 0 && (
            <p className="co-row-meta">
              {t("co.connections.more", { count: readable.dropped_count })}
            </p>
          )}
          <p className="cx-actions">
            <Button small onClick={() => setExpanded(true)}>
              {t("co.connections.expand")}
            </Button>
          </p>
          <Modal
            open={expanded}
            onClose={() => setExpanded(false)}
            labelledBy="cx-modal-title"
            size="wide"
          >
            <h2 id="cx-modal-title" className="t-h2">
              {t("co.connections.title")}
            </h2>
            <div className="cx-expanded">
              <EgoDiagram graph={readable} />
              <NodeList graph={readable} />
            </div>
            <Withheld groups={readable.groups_omitted} />
            {readable.dropped_count > 0 && (
              <p className="co-row-meta">
                {t("co.connections.more", { count: readable.dropped_count })}
              </p>
            )}
            <p className="cx-actions">
              <Button small onClick={() => setExpanded(false)}>
                {t("co.connections.collapse")}
              </Button>
            </p>
          </Modal>
        </>
      )}
    </section>
  );
}

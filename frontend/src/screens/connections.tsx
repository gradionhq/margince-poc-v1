// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
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
// diagram draws, in the same order, each routing through EntityRef — so
// everything the picture shows can be reached and opened without it.

type Graph = components["schemas"]["OrganizationGraph"];
type GraphNode = components["schemas"]["OrganizationGraphNode"];
type GraphEdge = components["schemas"]["OrganizationGraphEdge"];
type Group = Graph["groups_omitted"][number];

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
      {placed.map((p) => (
        <circle
          key={p.node.id}
          className={nodeClass(p.node)}
          cx={p.x}
          cy={p.y}
          r={p.node.root ? ROOT_RADIUS : NODE_RADIUS}
        />
      ))}
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
function nodeClass(node: GraphNode): string {
  const classes = ["cx-node", `cx-node-${node.kind}`];
  if (node.root) {
    classes.push("cx-node-root");
  }
  if (node.intro_path) {
    classes.push("cx-node-intro");
  }
  return classes.join(" ");
}

/**
 * NodeList is the card's accessible content: every node the diagram draws, in
 * the same order, each reachable by keyboard through EntityRef's own link.
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
          <span className="cx-node-name">
            <EntityRef kind={node.kind} id={node.id} />
            {node.intro_path && (
              <Badge tone="accent">{t("co.connections.introPath")}</Badge>
            )}
          </span>
          <span className="co-row-meta">
            <span className="cx-kind">
              {t(`co.connections.kind.${node.kind}`)}
            </span>
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

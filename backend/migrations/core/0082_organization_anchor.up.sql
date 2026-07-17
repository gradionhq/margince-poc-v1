-- 0082: the installation's OWN company — the spec's "anchor organization"
-- (onboarding-and-coldstart §1). The cold-start read-back resolves its target
-- by parsing a domain out of the source URL, which leaves the company the
-- installation belongs to indistinguishable from any customer org. Naming the
-- anchor is what lets "has this installation described itself yet?" be asked
-- as a question rather than inferred from a hostname.
--
-- The mark lives on organization, not as a workspace pointer: anchor-ness is a
-- property of the organization, the table people already owns (a workspace
-- pointer would make identity's table the home of a people concept), and it is
-- same-workspace by construction rather than by composite FK.
--
-- At most ONE live anchor per workspace, enforced by the database rather than
-- by whoever writes next. A freshly bootstrapped installation (ADR-0061) has
-- none: the column is set when a human first saves the company form.

ALTER TABLE organization ADD COLUMN is_anchor boolean NOT NULL DEFAULT false;

CREATE UNIQUE INDEX uq_organization_anchor ON organization (workspace_id)
  WHERE is_anchor AND archived_at IS NULL;

-- 0019: composite same-workspace foreign keys (C4, data-model tenancy
-- integrity). RLS bounds row VISIBILITY; it does not prove a FK target
-- lives in the SAME workspace. Every tenant-local FK is rebuilt as
-- (workspace_id, <col>) REFERENCES <ref>(workspace_id, id) so the database
-- rejects a cross-workspace reference by construction. SET NULL FKs use
-- PG15+ column-list SET NULL so only the FK column is nulled, never the
-- NOT NULL workspace_id. Enforced by TestFK_tenantLocalReferencesAreComposite.

-- 1. Composite unique keys on the referenced side (targets of the new FKs).
ALTER TABLE activity ADD CONSTRAINT uq_activity_ws_id UNIQUE (workspace_id, id);
ALTER TABLE app_user ADD CONSTRAINT uq_app_user_ws_id UNIQUE (workspace_id, id);
ALTER TABLE consent_purpose ADD CONSTRAINT uq_consent_purpose_ws_id UNIQUE (workspace_id, id);
ALTER TABLE deal ADD CONSTRAINT uq_deal_ws_id UNIQUE (workspace_id, id);
ALTER TABLE lead ADD CONSTRAINT uq_lead_ws_id UNIQUE (workspace_id, id);
ALTER TABLE list ADD CONSTRAINT uq_list_ws_id UNIQUE (workspace_id, id);
ALTER TABLE organization ADD CONSTRAINT uq_organization_ws_id UNIQUE (workspace_id, id);
ALTER TABLE passport ADD CONSTRAINT uq_passport_ws_id UNIQUE (workspace_id, id);
ALTER TABLE person ADD CONSTRAINT uq_person_ws_id UNIQUE (workspace_id, id);
ALTER TABLE pipeline ADD CONSTRAINT uq_pipeline_ws_id UNIQUE (workspace_id, id);
ALTER TABLE role ADD CONSTRAINT uq_role_ws_id UNIQUE (workspace_id, id);
ALTER TABLE stage ADD CONSTRAINT uq_stage_ws_id UNIQUE (workspace_id, id);
ALTER TABLE tag ADD CONSTRAINT uq_tag_ws_id UNIQUE (workspace_id, id);
ALTER TABLE team ADD CONSTRAINT uq_team_ws_id UNIQUE (workspace_id, id);
ALTER TABLE stage ADD CONSTRAINT uq_stage_ws_id_pipeline UNIQUE (workspace_id, id, pipeline_id);

-- 2. Rebuild each tenant-local FK as composite.
-- activity
ALTER TABLE activity DROP CONSTRAINT activity_assignee_id_fkey;
ALTER TABLE activity ADD CONSTRAINT activity_assignee_id_fkey FOREIGN KEY (workspace_id, assignee_id) REFERENCES app_user (workspace_id, id) ON DELETE SET NULL (assignee_id);
-- activity_link
ALTER TABLE activity_link DROP CONSTRAINT activity_link_activity_id_fkey;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_activity_id_fkey FOREIGN KEY (workspace_id, activity_id) REFERENCES activity (workspace_id, id) ON DELETE CASCADE;
ALTER TABLE activity_link DROP CONSTRAINT activity_link_deal_id_fkey;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_deal_id_fkey FOREIGN KEY (workspace_id, deal_id) REFERENCES deal (workspace_id, id) ON DELETE CASCADE;
ALTER TABLE activity_link DROP CONSTRAINT activity_link_organization_id_fkey;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_organization_id_fkey FOREIGN KEY (workspace_id, organization_id) REFERENCES organization (workspace_id, id) ON DELETE CASCADE;
ALTER TABLE activity_link DROP CONSTRAINT activity_link_person_id_fkey;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_person_id_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person (workspace_id, id) ON DELETE CASCADE;
-- approval
ALTER TABLE approval DROP CONSTRAINT approval_decided_by_fkey;
ALTER TABLE approval ADD CONSTRAINT approval_decided_by_fkey FOREIGN KEY (workspace_id, decided_by) REFERENCES app_user (workspace_id, id) ON DELETE SET NULL (decided_by);
ALTER TABLE approval DROP CONSTRAINT approval_on_behalf_of_fkey;
ALTER TABLE approval ADD CONSTRAINT approval_on_behalf_of_fkey FOREIGN KEY (workspace_id, on_behalf_of) REFERENCES app_user (workspace_id, id) ON DELETE SET NULL (on_behalf_of);
ALTER TABLE approval DROP CONSTRAINT approval_passport_id_fkey;
ALTER TABLE approval ADD CONSTRAINT approval_passport_id_fkey FOREIGN KEY (workspace_id, passport_id) REFERENCES passport (workspace_id, id) ON DELETE SET NULL (passport_id);
-- audit_log
ALTER TABLE audit_log DROP CONSTRAINT audit_log_on_behalf_of_fkey;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_on_behalf_of_fkey FOREIGN KEY (workspace_id, on_behalf_of) REFERENCES app_user (workspace_id, id) ON DELETE SET NULL (on_behalf_of);
-- consent_event
ALTER TABLE consent_event DROP CONSTRAINT consent_event_person_id_fkey;
ALTER TABLE consent_event ADD CONSTRAINT consent_event_person_id_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person (workspace_id, id) ON DELETE CASCADE;
ALTER TABLE consent_event DROP CONSTRAINT consent_event_purpose_id_fkey;
ALTER TABLE consent_event ADD CONSTRAINT consent_event_purpose_id_fkey FOREIGN KEY (workspace_id, purpose_id) REFERENCES consent_purpose (workspace_id, id) ON DELETE RESTRICT;
-- deal
ALTER TABLE deal DROP CONSTRAINT deal_organization_id_fkey;
ALTER TABLE deal ADD CONSTRAINT deal_organization_id_fkey FOREIGN KEY (workspace_id, organization_id) REFERENCES organization (workspace_id, id) ON DELETE SET NULL (organization_id);
ALTER TABLE deal DROP CONSTRAINT deal_owner_id_fkey;
ALTER TABLE deal ADD CONSTRAINT deal_owner_id_fkey FOREIGN KEY (workspace_id, owner_id) REFERENCES app_user (workspace_id, id) ON DELETE SET NULL (owner_id);
ALTER TABLE deal DROP CONSTRAINT deal_partner_org_id_fkey;
ALTER TABLE deal ADD CONSTRAINT deal_partner_org_id_fkey FOREIGN KEY (workspace_id, partner_org_id) REFERENCES organization (workspace_id, id) ON DELETE SET NULL (partner_org_id);
ALTER TABLE deal DROP CONSTRAINT deal_pipeline_id_fkey;
ALTER TABLE deal ADD CONSTRAINT deal_pipeline_id_fkey FOREIGN KEY (workspace_id, pipeline_id) REFERENCES pipeline (workspace_id, id) ON DELETE RESTRICT;
ALTER TABLE deal DROP CONSTRAINT deal_stage_id_fkey;
ALTER TABLE deal ADD CONSTRAINT deal_stage_id_fkey FOREIGN KEY (workspace_id, stage_id) REFERENCES stage (workspace_id, id) ON DELETE RESTRICT;
ALTER TABLE deal DROP CONSTRAINT deal_stage_in_pipeline;
ALTER TABLE deal ADD CONSTRAINT deal_stage_in_pipeline FOREIGN KEY (workspace_id, stage_id, pipeline_id) REFERENCES stage (workspace_id, id, pipeline_id);
-- deal_stage_history
ALTER TABLE deal_stage_history DROP CONSTRAINT deal_stage_history_deal_id_fkey;
ALTER TABLE deal_stage_history ADD CONSTRAINT deal_stage_history_deal_id_fkey FOREIGN KEY (workspace_id, deal_id) REFERENCES deal (workspace_id, id) ON DELETE CASCADE;
ALTER TABLE deal_stage_history DROP CONSTRAINT deal_stage_history_from_stage_id_fkey;
ALTER TABLE deal_stage_history ADD CONSTRAINT deal_stage_history_from_stage_id_fkey FOREIGN KEY (workspace_id, from_stage_id) REFERENCES stage (workspace_id, id) ON DELETE SET NULL (from_stage_id);
ALTER TABLE deal_stage_history DROP CONSTRAINT deal_stage_history_to_stage_id_fkey;
ALTER TABLE deal_stage_history ADD CONSTRAINT deal_stage_history_to_stage_id_fkey FOREIGN KEY (workspace_id, to_stage_id) REFERENCES stage (workspace_id, id) ON DELETE RESTRICT;
-- lead
ALTER TABLE lead DROP CONSTRAINT lead_owner_id_fkey;
ALTER TABLE lead ADD CONSTRAINT lead_owner_id_fkey FOREIGN KEY (workspace_id, owner_id) REFERENCES app_user (workspace_id, id) ON DELETE SET NULL (owner_id);
ALTER TABLE lead DROP CONSTRAINT lead_promoted_person_id_fkey;
ALTER TABLE lead ADD CONSTRAINT lead_promoted_person_id_fkey FOREIGN KEY (workspace_id, promoted_person_id) REFERENCES person (workspace_id, id) ON DELETE SET NULL (promoted_person_id);
-- list
ALTER TABLE list DROP CONSTRAINT list_owner_id_fkey;
ALTER TABLE list ADD CONSTRAINT list_owner_id_fkey FOREIGN KEY (workspace_id, owner_id) REFERENCES app_user (workspace_id, id) ON DELETE SET NULL (owner_id);
ALTER TABLE list DROP CONSTRAINT list_team_id_fkey;
ALTER TABLE list ADD CONSTRAINT list_team_id_fkey FOREIGN KEY (workspace_id, team_id) REFERENCES team (workspace_id, id) ON DELETE SET NULL (team_id);
-- list_member
ALTER TABLE list_member DROP CONSTRAINT list_member_list_id_fkey;
ALTER TABLE list_member ADD CONSTRAINT list_member_list_id_fkey FOREIGN KEY (workspace_id, list_id) REFERENCES list (workspace_id, id) ON DELETE CASCADE;
-- organization
ALTER TABLE organization DROP CONSTRAINT organization_merged_into_id_fkey;
ALTER TABLE organization ADD CONSTRAINT organization_merged_into_id_fkey FOREIGN KEY (workspace_id, merged_into_id) REFERENCES organization (workspace_id, id) ON DELETE SET NULL (merged_into_id);
ALTER TABLE organization DROP CONSTRAINT organization_owner_id_fkey;
ALTER TABLE organization ADD CONSTRAINT organization_owner_id_fkey FOREIGN KEY (workspace_id, owner_id) REFERENCES app_user (workspace_id, id) ON DELETE SET NULL (owner_id);
ALTER TABLE organization DROP CONSTRAINT organization_parent_org_id_fkey;
ALTER TABLE organization ADD CONSTRAINT organization_parent_org_id_fkey FOREIGN KEY (workspace_id, parent_org_id) REFERENCES organization (workspace_id, id) ON DELETE SET NULL (parent_org_id);
-- organization_domain
ALTER TABLE organization_domain DROP CONSTRAINT organization_domain_organization_id_fkey;
ALTER TABLE organization_domain ADD CONSTRAINT organization_domain_organization_id_fkey FOREIGN KEY (workspace_id, organization_id) REFERENCES organization (workspace_id, id) ON DELETE CASCADE;
-- partner
ALTER TABLE partner DROP CONSTRAINT partner_organization_id_fkey;
ALTER TABLE partner ADD CONSTRAINT partner_organization_id_fkey FOREIGN KEY (workspace_id, organization_id) REFERENCES organization (workspace_id, id) ON DELETE CASCADE;
-- passport
ALTER TABLE passport DROP CONSTRAINT passport_granted_by_fkey;
ALTER TABLE passport ADD CONSTRAINT passport_granted_by_fkey FOREIGN KEY (workspace_id, granted_by) REFERENCES app_user (workspace_id, id) ON DELETE RESTRICT;
ALTER TABLE passport DROP CONSTRAINT passport_on_behalf_of_fkey;
ALTER TABLE passport ADD CONSTRAINT passport_on_behalf_of_fkey FOREIGN KEY (workspace_id, on_behalf_of) REFERENCES app_user (workspace_id, id) ON DELETE CASCADE;
-- person
ALTER TABLE person DROP CONSTRAINT person_converted_from_lead_id_fkey;
ALTER TABLE person ADD CONSTRAINT person_converted_from_lead_id_fkey FOREIGN KEY (workspace_id, converted_from_lead_id) REFERENCES lead (workspace_id, id) ON DELETE SET NULL (converted_from_lead_id);
ALTER TABLE person DROP CONSTRAINT person_merged_into_id_fkey;
ALTER TABLE person ADD CONSTRAINT person_merged_into_id_fkey FOREIGN KEY (workspace_id, merged_into_id) REFERENCES person (workspace_id, id) ON DELETE SET NULL (merged_into_id);
ALTER TABLE person DROP CONSTRAINT person_owner_id_fkey;
ALTER TABLE person ADD CONSTRAINT person_owner_id_fkey FOREIGN KEY (workspace_id, owner_id) REFERENCES app_user (workspace_id, id) ON DELETE SET NULL (owner_id);
-- person_consent
ALTER TABLE person_consent DROP CONSTRAINT person_consent_person_id_fkey;
ALTER TABLE person_consent ADD CONSTRAINT person_consent_person_id_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person (workspace_id, id) ON DELETE CASCADE;
ALTER TABLE person_consent DROP CONSTRAINT person_consent_purpose_id_fkey;
ALTER TABLE person_consent ADD CONSTRAINT person_consent_purpose_id_fkey FOREIGN KEY (workspace_id, purpose_id) REFERENCES consent_purpose (workspace_id, id) ON DELETE RESTRICT;
-- person_email
ALTER TABLE person_email DROP CONSTRAINT person_email_person_id_fkey;
ALTER TABLE person_email ADD CONSTRAINT person_email_person_id_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person (workspace_id, id) ON DELETE CASCADE;
-- person_phone
ALTER TABLE person_phone DROP CONSTRAINT person_phone_person_id_fkey;
ALTER TABLE person_phone ADD CONSTRAINT person_phone_person_id_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person (workspace_id, id) ON DELETE CASCADE;
-- record_grant
ALTER TABLE record_grant DROP CONSTRAINT record_grant_granted_by_fkey;
ALTER TABLE record_grant ADD CONSTRAINT record_grant_granted_by_fkey FOREIGN KEY (workspace_id, granted_by) REFERENCES app_user (workspace_id, id) ON DELETE RESTRICT;
-- relationship
ALTER TABLE relationship DROP CONSTRAINT relationship_counterparty_org_id_fkey;
ALTER TABLE relationship ADD CONSTRAINT relationship_counterparty_org_id_fkey FOREIGN KEY (workspace_id, counterparty_org_id) REFERENCES organization (workspace_id, id) ON DELETE CASCADE;
ALTER TABLE relationship DROP CONSTRAINT relationship_deal_id_fkey;
ALTER TABLE relationship ADD CONSTRAINT relationship_deal_id_fkey FOREIGN KEY (workspace_id, deal_id) REFERENCES deal (workspace_id, id) ON DELETE CASCADE;
ALTER TABLE relationship DROP CONSTRAINT relationship_organization_id_fkey;
ALTER TABLE relationship ADD CONSTRAINT relationship_organization_id_fkey FOREIGN KEY (workspace_id, organization_id) REFERENCES organization (workspace_id, id) ON DELETE CASCADE;
ALTER TABLE relationship DROP CONSTRAINT relationship_person_id_fkey;
ALTER TABLE relationship ADD CONSTRAINT relationship_person_id_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person (workspace_id, id) ON DELETE CASCADE;
-- role_assignment
ALTER TABLE role_assignment DROP CONSTRAINT role_assignment_role_id_fkey;
ALTER TABLE role_assignment ADD CONSTRAINT role_assignment_role_id_fkey FOREIGN KEY (workspace_id, role_id) REFERENCES role (workspace_id, id) ON DELETE CASCADE;
ALTER TABLE role_assignment DROP CONSTRAINT role_assignment_team_id_fkey;
ALTER TABLE role_assignment ADD CONSTRAINT role_assignment_team_id_fkey FOREIGN KEY (workspace_id, team_id) REFERENCES team (workspace_id, id) ON DELETE CASCADE;
ALTER TABLE role_assignment DROP CONSTRAINT role_assignment_user_id_fkey;
ALTER TABLE role_assignment ADD CONSTRAINT role_assignment_user_id_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user (workspace_id, id) ON DELETE CASCADE;
-- session
ALTER TABLE session DROP CONSTRAINT session_user_id_fkey;
ALTER TABLE session ADD CONSTRAINT session_user_id_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user (workspace_id, id) ON DELETE CASCADE;
-- stage
ALTER TABLE stage DROP CONSTRAINT stage_pipeline_id_fkey;
ALTER TABLE stage ADD CONSTRAINT stage_pipeline_id_fkey FOREIGN KEY (workspace_id, pipeline_id) REFERENCES pipeline (workspace_id, id) ON DELETE CASCADE;
-- taggable
ALTER TABLE taggable DROP CONSTRAINT taggable_tag_id_fkey;
ALTER TABLE taggable ADD CONSTRAINT taggable_tag_id_fkey FOREIGN KEY (workspace_id, tag_id) REFERENCES tag (workspace_id, id) ON DELETE CASCADE;
-- team
ALTER TABLE team DROP CONSTRAINT team_parent_team_id_fkey;
ALTER TABLE team ADD CONSTRAINT team_parent_team_id_fkey FOREIGN KEY (workspace_id, parent_team_id) REFERENCES team (workspace_id, id) ON DELETE SET NULL (parent_team_id);
-- team_membership
ALTER TABLE team_membership DROP CONSTRAINT team_membership_team_id_fkey;
ALTER TABLE team_membership ADD CONSTRAINT team_membership_team_id_fkey FOREIGN KEY (workspace_id, team_id) REFERENCES team (workspace_id, id) ON DELETE CASCADE;
ALTER TABLE team_membership DROP CONSTRAINT team_membership_user_id_fkey;
ALTER TABLE team_membership ADD CONSTRAINT team_membership_user_id_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user (workspace_id, id) ON DELETE CASCADE;

-- Reverse 0019: restore single-column FKs and drop the composite unique keys.
-- activity
ALTER TABLE activity DROP CONSTRAINT activity_assignee_id_fkey;
ALTER TABLE activity ADD CONSTRAINT activity_assignee_id_fkey FOREIGN KEY (assignee_id) REFERENCES app_user (id) ON DELETE SET NULL;
-- activity_link
ALTER TABLE activity_link DROP CONSTRAINT activity_link_activity_id_fkey;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_activity_id_fkey FOREIGN KEY (activity_id) REFERENCES activity (id) ON DELETE CASCADE;
ALTER TABLE activity_link DROP CONSTRAINT activity_link_deal_id_fkey;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_deal_id_fkey FOREIGN KEY (deal_id) REFERENCES deal (id) ON DELETE CASCADE;
ALTER TABLE activity_link DROP CONSTRAINT activity_link_organization_id_fkey;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organization (id) ON DELETE CASCADE;
ALTER TABLE activity_link DROP CONSTRAINT activity_link_person_id_fkey;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_person_id_fkey FOREIGN KEY (person_id) REFERENCES person (id) ON DELETE CASCADE;
-- approval
ALTER TABLE approval DROP CONSTRAINT approval_decided_by_fkey;
ALTER TABLE approval ADD CONSTRAINT approval_decided_by_fkey FOREIGN KEY (decided_by) REFERENCES app_user (id) ON DELETE SET NULL;
ALTER TABLE approval DROP CONSTRAINT approval_on_behalf_of_fkey;
ALTER TABLE approval ADD CONSTRAINT approval_on_behalf_of_fkey FOREIGN KEY (on_behalf_of) REFERENCES app_user (id) ON DELETE SET NULL;
ALTER TABLE approval DROP CONSTRAINT approval_passport_id_fkey;
ALTER TABLE approval ADD CONSTRAINT approval_passport_id_fkey FOREIGN KEY (passport_id) REFERENCES passport (id) ON DELETE SET NULL;
-- audit_log
ALTER TABLE audit_log DROP CONSTRAINT audit_log_on_behalf_of_fkey;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_on_behalf_of_fkey FOREIGN KEY (on_behalf_of) REFERENCES app_user (id) ON DELETE SET NULL;
-- consent_event
ALTER TABLE consent_event DROP CONSTRAINT consent_event_person_id_fkey;
ALTER TABLE consent_event ADD CONSTRAINT consent_event_person_id_fkey FOREIGN KEY (person_id) REFERENCES person (id) ON DELETE CASCADE;
ALTER TABLE consent_event DROP CONSTRAINT consent_event_purpose_id_fkey;
ALTER TABLE consent_event ADD CONSTRAINT consent_event_purpose_id_fkey FOREIGN KEY (purpose_id) REFERENCES consent_purpose (id) ON DELETE RESTRICT;
-- deal
ALTER TABLE deal DROP CONSTRAINT deal_organization_id_fkey;
ALTER TABLE deal ADD CONSTRAINT deal_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organization (id) ON DELETE SET NULL;
ALTER TABLE deal DROP CONSTRAINT deal_owner_id_fkey;
ALTER TABLE deal ADD CONSTRAINT deal_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES app_user (id) ON DELETE SET NULL;
ALTER TABLE deal DROP CONSTRAINT deal_partner_org_id_fkey;
ALTER TABLE deal ADD CONSTRAINT deal_partner_org_id_fkey FOREIGN KEY (partner_org_id) REFERENCES organization (id) ON DELETE SET NULL;
ALTER TABLE deal DROP CONSTRAINT deal_pipeline_id_fkey;
ALTER TABLE deal ADD CONSTRAINT deal_pipeline_id_fkey FOREIGN KEY (pipeline_id) REFERENCES pipeline (id) ON DELETE RESTRICT;
ALTER TABLE deal DROP CONSTRAINT deal_stage_id_fkey;
ALTER TABLE deal ADD CONSTRAINT deal_stage_id_fkey FOREIGN KEY (stage_id) REFERENCES stage (id) ON DELETE RESTRICT;
ALTER TABLE deal DROP CONSTRAINT deal_stage_in_pipeline;
ALTER TABLE deal ADD CONSTRAINT deal_stage_in_pipeline FOREIGN KEY (stage_id, pipeline_id) REFERENCES stage (id, pipeline_id);
-- deal_stage_history
ALTER TABLE deal_stage_history DROP CONSTRAINT deal_stage_history_deal_id_fkey;
ALTER TABLE deal_stage_history ADD CONSTRAINT deal_stage_history_deal_id_fkey FOREIGN KEY (deal_id) REFERENCES deal (id) ON DELETE CASCADE;
ALTER TABLE deal_stage_history DROP CONSTRAINT deal_stage_history_from_stage_id_fkey;
ALTER TABLE deal_stage_history ADD CONSTRAINT deal_stage_history_from_stage_id_fkey FOREIGN KEY (from_stage_id) REFERENCES stage (id) ON DELETE SET NULL;
ALTER TABLE deal_stage_history DROP CONSTRAINT deal_stage_history_to_stage_id_fkey;
ALTER TABLE deal_stage_history ADD CONSTRAINT deal_stage_history_to_stage_id_fkey FOREIGN KEY (to_stage_id) REFERENCES stage (id) ON DELETE RESTRICT;
-- lead
ALTER TABLE lead DROP CONSTRAINT lead_owner_id_fkey;
ALTER TABLE lead ADD CONSTRAINT lead_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES app_user (id) ON DELETE SET NULL;
ALTER TABLE lead DROP CONSTRAINT lead_promoted_person_id_fkey;
ALTER TABLE lead ADD CONSTRAINT lead_promoted_person_id_fkey FOREIGN KEY (promoted_person_id) REFERENCES person (id) ON DELETE SET NULL;
-- list
ALTER TABLE list DROP CONSTRAINT list_owner_id_fkey;
ALTER TABLE list ADD CONSTRAINT list_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES app_user (id) ON DELETE SET NULL;
ALTER TABLE list DROP CONSTRAINT list_team_id_fkey;
ALTER TABLE list ADD CONSTRAINT list_team_id_fkey FOREIGN KEY (team_id) REFERENCES team (id) ON DELETE SET NULL;
-- list_member
ALTER TABLE list_member DROP CONSTRAINT list_member_list_id_fkey;
ALTER TABLE list_member ADD CONSTRAINT list_member_list_id_fkey FOREIGN KEY (list_id) REFERENCES list (id) ON DELETE CASCADE;
-- organization
ALTER TABLE organization DROP CONSTRAINT organization_merged_into_id_fkey;
ALTER TABLE organization ADD CONSTRAINT organization_merged_into_id_fkey FOREIGN KEY (merged_into_id) REFERENCES organization (id) ON DELETE SET NULL;
ALTER TABLE organization DROP CONSTRAINT organization_owner_id_fkey;
ALTER TABLE organization ADD CONSTRAINT organization_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES app_user (id) ON DELETE SET NULL;
ALTER TABLE organization DROP CONSTRAINT organization_parent_org_id_fkey;
ALTER TABLE organization ADD CONSTRAINT organization_parent_org_id_fkey FOREIGN KEY (parent_org_id) REFERENCES organization (id) ON DELETE SET NULL;
-- organization_domain
ALTER TABLE organization_domain DROP CONSTRAINT organization_domain_organization_id_fkey;
ALTER TABLE organization_domain ADD CONSTRAINT organization_domain_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organization (id) ON DELETE CASCADE;
-- partner
ALTER TABLE partner DROP CONSTRAINT partner_organization_id_fkey;
ALTER TABLE partner ADD CONSTRAINT partner_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organization (id) ON DELETE CASCADE;
-- passport
ALTER TABLE passport DROP CONSTRAINT passport_granted_by_fkey;
ALTER TABLE passport ADD CONSTRAINT passport_granted_by_fkey FOREIGN KEY (granted_by) REFERENCES app_user (id) ON DELETE RESTRICT;
ALTER TABLE passport DROP CONSTRAINT passport_on_behalf_of_fkey;
ALTER TABLE passport ADD CONSTRAINT passport_on_behalf_of_fkey FOREIGN KEY (on_behalf_of) REFERENCES app_user (id) ON DELETE CASCADE;
-- person
ALTER TABLE person DROP CONSTRAINT person_converted_from_lead_id_fkey;
ALTER TABLE person ADD CONSTRAINT person_converted_from_lead_id_fkey FOREIGN KEY (converted_from_lead_id) REFERENCES lead (id) ON DELETE SET NULL;
ALTER TABLE person DROP CONSTRAINT person_merged_into_id_fkey;
ALTER TABLE person ADD CONSTRAINT person_merged_into_id_fkey FOREIGN KEY (merged_into_id) REFERENCES person (id) ON DELETE SET NULL;
ALTER TABLE person DROP CONSTRAINT person_owner_id_fkey;
ALTER TABLE person ADD CONSTRAINT person_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES app_user (id) ON DELETE SET NULL;
-- person_consent
ALTER TABLE person_consent DROP CONSTRAINT person_consent_person_id_fkey;
ALTER TABLE person_consent ADD CONSTRAINT person_consent_person_id_fkey FOREIGN KEY (person_id) REFERENCES person (id) ON DELETE CASCADE;
ALTER TABLE person_consent DROP CONSTRAINT person_consent_purpose_id_fkey;
ALTER TABLE person_consent ADD CONSTRAINT person_consent_purpose_id_fkey FOREIGN KEY (purpose_id) REFERENCES consent_purpose (id) ON DELETE RESTRICT;
-- person_email
ALTER TABLE person_email DROP CONSTRAINT person_email_person_id_fkey;
ALTER TABLE person_email ADD CONSTRAINT person_email_person_id_fkey FOREIGN KEY (person_id) REFERENCES person (id) ON DELETE CASCADE;
-- person_phone
ALTER TABLE person_phone DROP CONSTRAINT person_phone_person_id_fkey;
ALTER TABLE person_phone ADD CONSTRAINT person_phone_person_id_fkey FOREIGN KEY (person_id) REFERENCES person (id) ON DELETE CASCADE;
-- record_grant
ALTER TABLE record_grant DROP CONSTRAINT record_grant_granted_by_fkey;
ALTER TABLE record_grant ADD CONSTRAINT record_grant_granted_by_fkey FOREIGN KEY (granted_by) REFERENCES app_user (id) ON DELETE RESTRICT;
-- relationship
ALTER TABLE relationship DROP CONSTRAINT relationship_counterparty_org_id_fkey;
ALTER TABLE relationship ADD CONSTRAINT relationship_counterparty_org_id_fkey FOREIGN KEY (counterparty_org_id) REFERENCES organization (id) ON DELETE CASCADE;
ALTER TABLE relationship DROP CONSTRAINT relationship_deal_id_fkey;
ALTER TABLE relationship ADD CONSTRAINT relationship_deal_id_fkey FOREIGN KEY (deal_id) REFERENCES deal (id) ON DELETE CASCADE;
ALTER TABLE relationship DROP CONSTRAINT relationship_organization_id_fkey;
ALTER TABLE relationship ADD CONSTRAINT relationship_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organization (id) ON DELETE CASCADE;
ALTER TABLE relationship DROP CONSTRAINT relationship_person_id_fkey;
ALTER TABLE relationship ADD CONSTRAINT relationship_person_id_fkey FOREIGN KEY (person_id) REFERENCES person (id) ON DELETE CASCADE;
-- role_assignment
ALTER TABLE role_assignment DROP CONSTRAINT role_assignment_role_id_fkey;
ALTER TABLE role_assignment ADD CONSTRAINT role_assignment_role_id_fkey FOREIGN KEY (role_id) REFERENCES role (id) ON DELETE CASCADE;
ALTER TABLE role_assignment DROP CONSTRAINT role_assignment_team_id_fkey;
ALTER TABLE role_assignment ADD CONSTRAINT role_assignment_team_id_fkey FOREIGN KEY (team_id) REFERENCES team (id) ON DELETE CASCADE;
ALTER TABLE role_assignment DROP CONSTRAINT role_assignment_user_id_fkey;
ALTER TABLE role_assignment ADD CONSTRAINT role_assignment_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user (id) ON DELETE CASCADE;
-- session
ALTER TABLE session DROP CONSTRAINT session_user_id_fkey;
ALTER TABLE session ADD CONSTRAINT session_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user (id) ON DELETE CASCADE;
-- stage
ALTER TABLE stage DROP CONSTRAINT stage_pipeline_id_fkey;
ALTER TABLE stage ADD CONSTRAINT stage_pipeline_id_fkey FOREIGN KEY (pipeline_id) REFERENCES pipeline (id) ON DELETE CASCADE;
-- taggable
ALTER TABLE taggable DROP CONSTRAINT taggable_tag_id_fkey;
ALTER TABLE taggable ADD CONSTRAINT taggable_tag_id_fkey FOREIGN KEY (tag_id) REFERENCES tag (id) ON DELETE CASCADE;
-- team
ALTER TABLE team DROP CONSTRAINT team_parent_team_id_fkey;
ALTER TABLE team ADD CONSTRAINT team_parent_team_id_fkey FOREIGN KEY (parent_team_id) REFERENCES team (id) ON DELETE SET NULL;
-- team_membership
ALTER TABLE team_membership DROP CONSTRAINT team_membership_team_id_fkey;
ALTER TABLE team_membership ADD CONSTRAINT team_membership_team_id_fkey FOREIGN KEY (team_id) REFERENCES team (id) ON DELETE CASCADE;
ALTER TABLE team_membership DROP CONSTRAINT team_membership_user_id_fkey;
ALTER TABLE team_membership ADD CONSTRAINT team_membership_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user (id) ON DELETE CASCADE;

ALTER TABLE stage DROP CONSTRAINT uq_stage_ws_id_pipeline;
ALTER TABLE activity DROP CONSTRAINT uq_activity_ws_id;
ALTER TABLE app_user DROP CONSTRAINT uq_app_user_ws_id;
ALTER TABLE consent_purpose DROP CONSTRAINT uq_consent_purpose_ws_id;
ALTER TABLE deal DROP CONSTRAINT uq_deal_ws_id;
ALTER TABLE lead DROP CONSTRAINT uq_lead_ws_id;
ALTER TABLE list DROP CONSTRAINT uq_list_ws_id;
ALTER TABLE organization DROP CONSTRAINT uq_organization_ws_id;
ALTER TABLE passport DROP CONSTRAINT uq_passport_ws_id;
ALTER TABLE person DROP CONSTRAINT uq_person_ws_id;
ALTER TABLE pipeline DROP CONSTRAINT uq_pipeline_ws_id;
ALTER TABLE role DROP CONSTRAINT uq_role_ws_id;
ALTER TABLE stage DROP CONSTRAINT uq_stage_ws_id;
ALTER TABLE tag DROP CONSTRAINT uq_tag_ws_id;
ALTER TABLE team DROP CONSTRAINT uq_team_ws_id;

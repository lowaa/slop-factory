-- +goose Up
-- The reviewer is slop-review now: its usage harness and the steward's check
-- are renamed, and the rows already stored follow so one name means one thing.
-- llm_tool_usage references llm_usage by a key that includes harness, without
-- ON UPDATE CASCADE, so the reference is dropped while both sides move and
-- then restored (which checks it again).
-- +goose StatementBegin
SELECT go_db_with_rls_lifted(
    ARRAY['llm_usage', 'llm_tool_usage', 'llm_usage_marks', 'llm_guidance_usage', 'steward_assessments', 'steward_drafts'],
    $body$
    ALTER TABLE llm_tool_usage DROP CONSTRAINT llm_tool_usage_tenant_id_agent_id_transcript_key_provider__fkey;
    UPDATE llm_usage SET harness = 'slop-review' WHERE harness = 'self-review';
    UPDATE llm_tool_usage SET harness = 'slop-review' WHERE harness = 'self-review';
    ALTER TABLE llm_tool_usage ADD CONSTRAINT llm_tool_usage_tenant_id_agent_id_transcript_key_provider__fkey
        FOREIGN KEY (tenant_id, agent_id, transcript_key, provider, model, harness, billing_mode)
        REFERENCES llm_usage (tenant_id, agent_id, transcript_key, provider, model, harness, billing_mode) ON DELETE CASCADE;
    UPDATE llm_usage_marks SET harness = 'slop-review' WHERE harness = 'self-review';
    UPDATE llm_guidance_usage SET harness = 'slop-review' WHERE harness = 'self-review';
    UPDATE steward_assessments SET evidence = (
        SELECT jsonb_agg(CASE WHEN e->>'check' = 'self_review_complete'
                              THEN jsonb_set(e, '{check}', '"slop_review_complete"') ELSE e END ORDER BY i)
          FROM jsonb_array_elements(evidence) WITH ORDINALITY AS x(e, i))
     WHERE evidence @> '[{"check": "self_review_complete"}]';
    UPDATE steward_drafts SET evidence = (
        SELECT jsonb_agg(CASE WHEN e->>'check' = 'self_review_complete'
                              THEN jsonb_set(e, '{check}', '"slop_review_complete"') ELSE e END ORDER BY i)
          FROM jsonb_array_elements(evidence) WITH ORDINALITY AS x(e, i))
     WHERE evidence @> '[{"check": "self_review_complete"}]';
    $body$);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT go_db_with_rls_lifted(
    ARRAY['llm_usage', 'llm_tool_usage', 'llm_usage_marks', 'llm_guidance_usage', 'steward_assessments', 'steward_drafts'],
    $body$
    ALTER TABLE llm_tool_usage DROP CONSTRAINT llm_tool_usage_tenant_id_agent_id_transcript_key_provider__fkey;
    UPDATE llm_usage SET harness = 'self-review' WHERE harness = 'slop-review';
    UPDATE llm_tool_usage SET harness = 'self-review' WHERE harness = 'slop-review';
    ALTER TABLE llm_tool_usage ADD CONSTRAINT llm_tool_usage_tenant_id_agent_id_transcript_key_provider__fkey
        FOREIGN KEY (tenant_id, agent_id, transcript_key, provider, model, harness, billing_mode)
        REFERENCES llm_usage (tenant_id, agent_id, transcript_key, provider, model, harness, billing_mode) ON DELETE CASCADE;
    UPDATE llm_usage_marks SET harness = 'self-review' WHERE harness = 'slop-review';
    UPDATE llm_guidance_usage SET harness = 'self-review' WHERE harness = 'slop-review';
    UPDATE steward_assessments SET evidence = (
        SELECT jsonb_agg(CASE WHEN e->>'check' = 'slop_review_complete'
                              THEN jsonb_set(e, '{check}', '"self_review_complete"') ELSE e END ORDER BY i)
          FROM jsonb_array_elements(evidence) WITH ORDINALITY AS x(e, i))
     WHERE evidence @> '[{"check": "slop_review_complete"}]';
    UPDATE steward_drafts SET evidence = (
        SELECT jsonb_agg(CASE WHEN e->>'check' = 'slop_review_complete'
                              THEN jsonb_set(e, '{check}', '"self_review_complete"') ELSE e END ORDER BY i)
          FROM jsonb_array_elements(evidence) WITH ORDINALITY AS x(e, i))
     WHERE evidence @> '[{"check": "slop_review_complete"}]';
    $body$);
-- +goose StatementEnd

-- +goose Up
-- M4-17: turn the "a published rule-set version is immutable" convention into a
-- DB-enforced lock. Today that guarantee is grant-level only (rule_set_versions.sql:35-36)
-- -- SELECT-only / UPDATE(enabled)-only for invoice_app -- and grants do not bind the
-- table OWNER. Migrations run as invoice_migrator, which is exactly how
-- 20260715120000_line_rules.sql silently INSERTed two rules into the already-published,
-- already-active v1 (reverted by 20260716185106_rule_set_v2.sql publishing v2 instead).
-- This migration closes that hole with an explicit `sealed` flag plus three owner-proof
-- triggers, directly precedented by audit_log's owner-proof append-only guard
-- (migrations/20260708062657_audit_log.sql).
--
-- Schema change: a version is born unsealed (DEFAULT false) so a publishing migration's
-- INSERT ... rules ... then UPDATE ... SET sealed = true works unchanged (the seal-on-
-- publish convention 20260716185106_rule_set_v2.sql already follows structurally).
ALTER TABLE rule_set_versions ADD COLUMN sealed boolean NOT NULL DEFAULT false;

-- Guard A -- content lock on `rules` (BEFORE INSERT OR UPDATE OR DELETE ON rules FOR EACH
-- ROW). A regular BEFORE trigger fires for the table owner too, and the
-- session_replication_role='replica' skip is SUPERUSER-only (invoice_migrator is
-- NOSUPERUSER) -- the same audit_log guarantee.
--   INSERT -> reject if NEW's parent version is sealed (the incident verb).
--   DELETE (direct child delete only -- the FK-cascade parent-delete path is intercepted
--     upstream by Guard C, see that trigger's comment for the F1 staleness reasoning) ->
--     reject if OLD's parent version is sealed.
--   UPDATE -> enabled-only carve-out: "content changed" = any column other than `enabled`
--     differs, via null-safe IS DISTINCT FROM on every content column. Content unchanged
--     (enabled-only / no-op) is allowed (the M3-06 kill-switch). Content changed is
--     rejected when EITHER OLD's or NEW's parent is sealed -- checking NEW closes the
--     reparent-into-sealed bypass (insert under an unsealed draft, then UPDATE its
--     rule_set_version_id to a sealed version).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rules_content_lock()
    RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF (SELECT sealed FROM rule_set_versions WHERE id = OLD.rule_set_version_id) THEN
            RAISE EXCEPTION 'rules of a sealed rule-set version are immutable: % is not permitted', TG_OP
                USING ERRCODE = 'restrict_violation';
        END IF;
        RETURN OLD;
    END IF;

    IF TG_OP = 'INSERT' THEN
        IF (SELECT sealed FROM rule_set_versions WHERE id = NEW.rule_set_version_id) THEN
            RAISE EXCEPTION 'rules of a sealed rule-set version are immutable: % is not permitted', TG_OP
                USING ERRCODE = 'restrict_violation';
        END IF;
        RETURN NEW;
    END IF;

    -- UPDATE: enabled-only is the sole sanctioned live mutation (the M3-06 kill-switch).
    IF ( OLD.rule_set_version_id IS DISTINCT FROM NEW.rule_set_version_id
      OR OLD.key      IS DISTINCT FROM NEW.key
      OR OLD.type     IS DISTINCT FROM NEW.type
      OR OLD.target   IS DISTINCT FROM NEW.target
      OR OLD.params   IS DISTINCT FROM NEW.params
      OR OLD.severity IS DISTINCT FROM NEW.severity
      OR OLD."when"   IS DISTINCT FROM NEW."when"
      OR OLD.message  IS DISTINCT FROM NEW.message
      OR OLD.scope    IS DISTINCT FROM NEW.scope
      OR OLD.id       IS DISTINCT FROM NEW.id ) THEN
        IF (SELECT sealed FROM rule_set_versions WHERE id = OLD.rule_set_version_id)
           OR (SELECT sealed FROM rule_set_versions WHERE id = NEW.rule_set_version_id) THEN
            RAISE EXCEPTION 'rules of a sealed rule-set version are immutable: content UPDATE is not permitted'
                USING ERRCODE = 'restrict_violation';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER rules_content_lock
    BEFORE INSERT OR UPDATE OR DELETE ON rules
    FOR EACH ROW EXECUTE FUNCTION rules_content_lock();

-- Guard B -- TRUNCATE lock on `rules`. Row triggers never fire on TRUNCATE, so without
-- this the migrator could TRUNCATE rules and wipe ALL rule content -- worse than the
-- line_rules incident. Unconditional is defensible: sealed v1/v2 always exist
-- post-migration, so there is never a legitimate TRUNCATE rules. Directly mirrors
-- audit_log_no_truncate (audit_log.sql:96-98).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rules_no_truncate()
    RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    RAISE EXCEPTION 'rules is protected by the rule-set immutability lock: % is not permitted', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER rules_no_truncate
    BEFORE TRUNCATE ON rules
    FOR EACH STATEMENT EXECUTE FUNCTION rules_no_truncate();

-- Guard C -- seal-guard on `rule_set_versions` (BEFORE UPDATE OR DELETE ON
-- rule_set_versions FOR EACH ROW). One trigger covers both version-row guards, directly
-- precedented by audit_log_no_update_delete BEFORE UPDATE OR DELETE.
--   UPDATE -> reject a sealed true->false transition (the seal is irreversible). Everything
--     else on the version row is allowed: the false->true seal, true->true no-ops, and the
--     legitimate is_active activation flip.
--   DELETE -> reject if OLD.sealed is true, read DIRECTLY off the departing row -- NO
--     subquery. This is the F1 fix: on a DELETE FROM rule_set_versions, the FK
--     ON DELETE CASCADE removes the parent row before the cascaded `rules` DELETEs run, so
--     a subquery inside Guard A's DELETE branch (SELECT sealed FROM rule_set_versions
--     WHERE id = OLD.rule_set_version_id) would find zero rows -> NULL -> never raise,
--     silently letting the cascade destroy a sealed version's rules. Reading OLD.sealed
--     directly here aborts the statement BEFORE the cascade proceeds, so a sealed
--     version's rules can never be reached by it. An unsealed version stays deletable
--     (draft rollback, throwaway fixtures).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rule_set_versions_seal_guard()
    RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.sealed THEN
            RAISE EXCEPTION 'a sealed rule-set version cannot be deleted (version=%)', OLD.version
                USING ERRCODE = 'restrict_violation';
        END IF;
        RETURN OLD;
    END IF;

    -- UPDATE
    IF OLD.sealed AND NOT NEW.sealed THEN
        RAISE EXCEPTION 'a sealed rule-set version cannot be unsealed (version=%)', OLD.version
            USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER rule_set_versions_seal_guard
    BEFORE UPDATE OR DELETE ON rule_set_versions
    FOR EACH ROW EXECUTE FUNCTION rule_set_versions_seal_guard();

-- Retroactive seal (AC #4): runs AFTER Guard C exists -- false->true is allowed by it, so
-- no self-interference -- and touches rule_set_versions only, so Guard A (on rules) never
-- fires. v1's 17 rules and v2's 19 rules become locked purely because their parent is now
-- sealed.
UPDATE rule_set_versions SET sealed = true WHERE version IN (1, 2);

-- +goose Down
-- Clean reverse order: drop the 3 triggers, then the 3 functions, then the column. This is
-- the NEWEST migration, and goose `reset` rolls back newest-oldest, so this Down runs
-- FIRST -- dropping every guard (and the column) before 20260716185106_rule_set_v2.sql's
-- or 20260715120000_line_rules.sql's own Downs run, which is what lets those older Downs'
-- DELETE/INSERT statements against v1/v2 execute with no guard present.
DROP TRIGGER rule_set_versions_seal_guard ON rule_set_versions;
DROP TRIGGER rules_no_truncate ON rules;
DROP TRIGGER rules_content_lock ON rules;

DROP FUNCTION rule_set_versions_seal_guard;
DROP FUNCTION rules_no_truncate;
DROP FUNCTION rules_content_lock;

ALTER TABLE rule_set_versions DROP COLUMN sealed;

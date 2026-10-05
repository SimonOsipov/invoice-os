-- db/seed.e2e-shards.sql -- per-shard tenant pairs for parallel topology E2E.
-- Runs as superuser in a PR environment's reset-and-seed, after seed.dev.sql
-- (db.SeedShards). Each shard tenant copies its source tenant: firm <- 1111, in-house <- 2222.
-- Writes nothing for the four seed.dev.sql tenants. Tenants, memberships, roles and
-- entities re-converge on a re-run; shard tenants are not purged.

-- Session-scoped: vanishes when SeedShards closes its connection.
CREATE TEMP TABLE shard_map (shard_id uuid PRIMARY KEY, source_id uuid NOT NULL, is_firm boolean NOT NULL);
INSERT INTO shard_map (shard_id, source_id, is_firm) VALUES
    ('11111111-1111-1111-1111-00000000e2e1', '11111111-1111-1111-1111-111111111111', true),
    ('11111111-1111-1111-1111-00000000e2e2', '11111111-1111-1111-1111-111111111111', true),
    ('22222222-2222-2222-2222-00000000e2e1', '22222222-2222-2222-2222-222222222222', false),
    ('22222222-2222-2222-2222-00000000e2e2', '22222222-2222-2222-2222-222222222222', false);

INSERT INTO tenants (id, name, kind)
SELECT m.shard_id, t.name, t.kind
FROM shard_map m JOIN tenants t ON t.id = m.source_id
ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, kind = EXCLUDED.kind;

INSERT INTO memberships (tenant_id, user_id, role, display_name, email, status)
SELECT m.shard_id, s.user_id, s.role, s.display_name, s.email, s.status
FROM shard_map m JOIN memberships s ON s.tenant_id = m.source_id
WHERE s.user_id::text LIKE 'c0000000-0000-0000-0000-%' -- seeded personas only; e2e-granted members stay on 1111 / 2222
ON CONFLICT (tenant_id, user_id) DO UPDATE SET
    role         = EXCLUDED.role,
    display_name = EXCLUDED.display_name,
    email        = EXCLUDED.email,
    status       = EXCLUDED.status;

-- New ids; created_at copied so ListRoles' order matches the source.
INSERT INTO workflow_roles (tenant_id, key, title, description, created_at)
SELECT m.shard_id, r.key, r.title, r.description, r.created_at
FROM shard_map m JOIN workflow_roles r ON r.tenant_id = m.source_id AND r.deleted_at IS NULL
ON CONFLICT ON CONSTRAINT workflow_roles_tenant_key_uq DO UPDATE SET
    title       = EXCLUDED.title,
    description = EXCLUDED.description,
    deleted_at  = NULL;

-- Joined by (tenant_id, key): a surviving shard role keeps its old id.
INSERT INTO workflow_role_members (tenant_id, workflow_role_id, user_id, ord)
SELECT m.shard_id, sr.id, sm.user_id, sm.ord
FROM shard_map m
JOIN workflow_roles src ON src.tenant_id = m.source_id AND src.deleted_at IS NULL
JOIN workflow_role_members sm ON sm.tenant_id = src.tenant_id AND sm.workflow_role_id = src.id
JOIN workflow_roles sr ON sr.tenant_id = m.shard_id AND sr.key = src.key AND sr.deleted_at IS NULL
ON CONFLICT ON CONSTRAINT workflow_role_members_tenant_role_user_uq DO NOTHING;

-- One unpublished copy of internal/demopolicy's plan per shard: firmPlan for firm shards,
-- inhousePlan for in-house shards. Steps go in only for a version created here:
-- approval_policy_steps_content_lock rejects an INSERT into a sealed one, and a redeploy
-- finds it sealed.
WITH step_def (is_firm, ref, parent_ref, branch, ord, kind, role_key, cond_op, cond_amount, notify_target, notify_channel) AS (
  VALUES
    (true,  'a',  NULL, NULL,   0, 'approval',    'fin_mgr',    NULL, NULL,            NULL,              NULL),
    (true,  'b',  NULL, NULL,   1, 'condition',   NULL,         '>',  250000000.00,    NULL,              NULL),
    (true,  'b1', 'b',  'then', 0, 'approval',    'fin_dir',    NULL, NULL,            NULL,              NULL),
    (true,  'c',  NULL, NULL,   2, 'condition',   NULL,         '>',  1000000000.00,   NULL,              NULL),
    (true,  'c1', 'c',  'then', 0, 'approval',    'cfo',        NULL, NULL,            NULL,              NULL),
    (true,  'c2', 'c',  'then', 1, 'notify',      NULL,         NULL, NULL,            'Audit Committee', 'Email'),
    (true,  'd',  NULL, NULL,   3, 'approval',    'compliance', NULL, NULL,            NULL,              NULL),
    (false, 'h',  NULL, NULL,   0, 'condition',   NULL,         '>',  100000.00,       NULL,              NULL),
    (false, 'h1', 'h',  'then', 0, 'approval',    'fin_dir',    NULL, NULL,            NULL,              NULL),
    (false, 'h2', 'h',  'else', 0, 'autoapprove', NULL,         NULL, NULL,            NULL,              NULL),
    (false, 'hn', NULL, NULL,   1, 'notify',      NULL,         NULL, NULL,            'Tax Team',        'In-app')
),
new_policy AS (
  INSERT INTO approval_policies (tenant_id, name)
  SELECT m.shard_id, CASE WHEN m.is_firm THEN 'Standard approval policy' ELSE 'Company approval policy' END
  FROM shard_map m
  WHERE NOT EXISTS (SELECT 1 FROM approval_policies p
                     WHERE p.tenant_id = m.shard_id
                       AND p.name = CASE WHEN m.is_firm THEN 'Standard approval policy' ELSE 'Company approval policy' END)
  RETURNING id, tenant_id
),
new_version AS (
  INSERT INTO approval_policy_versions (tenant_id, policy_id, version, sealed, is_active)
  SELECT tenant_id, id, 1, false, false FROM new_policy
  RETURNING id, tenant_id
),
step_row AS (
  SELECT v.id AS version_id, v.tenant_id, d.*, gen_random_uuid() AS step_id
  FROM new_version v
  JOIN shard_map m ON m.shard_id = v.tenant_id
  JOIN step_def d ON d.is_firm = m.is_firm
)
INSERT INTO approval_policy_steps (
    id, tenant_id, version_id, parent_step_id, branch, ord, kind,
    workflow_role_key, cond_op, cond_amount, notify_target, notify_channel
)
SELECT s.step_id, s.tenant_id, s.version_id, p.step_id, s.branch, s.ord, s.kind,
       s.role_key, s.cond_op, s.cond_amount, s.notify_target, s.notify_channel
FROM step_row s
LEFT JOIN step_row p ON p.version_id = s.version_id AND p.ref = s.parent_ref;

-- Firm shard <- 1111's Adeyemi; in-house shard <- 2222's Honeywell Group.
INSERT INTO business_entities (tenant_id, name, tin, sector, status)
SELECT m.shard_id, e.name, e.tin, e.sector, e.status
FROM shard_map m
JOIN business_entities e ON e.tenant_id = m.source_id
 AND e.name = CASE WHEN m.is_firm THEN 'Adeyemi & Sons Trading Ltd' ELSE 'Honeywell Group' END
ON CONFLICT (tenant_id, tin) WHERE tin IS NOT NULL
    DO UPDATE SET name = EXCLUDED.name, sector = EXCLUDED.sector, status = EXCLUDED.status;

-- Firm shards: a copy of 1111's DEMO-2026-1004. Children follow only the ids the
-- invoice insert returns, so a re-run inserts nothing.
WITH new_invoice AS (
  INSERT INTO invoices (
      tenant_id, entity_id, invoice_number, status, issue_date, supplier_tin, supplier_name,
      buyer_tin, buyer_name, currency, subtotal, vat, total, violations, rule_set_version_id,
      rejection_reasons, failure_kind, irn, csid, qr_payload, created_at
  )
  SELECT m.shard_id, e.id, i.invoice_number, i.status, i.issue_date, i.supplier_tin, i.supplier_name,
         i.buyer_tin, i.buyer_name, i.currency, i.subtotal, i.vat, i.total, i.violations, i.rule_set_version_id,
         i.rejection_reasons, i.failure_kind, i.irn, i.csid, i.qr_payload, i.created_at
  FROM shard_map m
  JOIN invoices i ON i.tenant_id = m.source_id AND i.invoice_number = 'DEMO-2026-1004'
  JOIN business_entities e ON e.tenant_id = m.shard_id AND e.tin = i.supplier_tin
  WHERE m.is_firm
  ON CONFLICT (tenant_id, entity_id, invoice_number) DO NOTHING
  RETURNING id, tenant_id, invoice_number
),
new_line_items AS (
  INSERT INTO line_items (tenant_id, invoice_id, line_no, description, quantity, unit_price, line_total, line_tax)
  SELECT n.tenant_id, n.id, li.line_no, li.description, li.quantity, li.unit_price, li.line_total, li.line_tax
  FROM new_invoice n
  JOIN shard_map m ON m.shard_id = n.tenant_id
  JOIN invoices si ON si.tenant_id = m.source_id AND si.invoice_number = n.invoice_number
  JOIN line_items li ON li.invoice_id = si.id
)
INSERT INTO invoice_status_history (tenant_id, invoice_id, from_status, to_status, actor, changed_at)
SELECT n.tenant_id, n.id, h.from_status, h.to_status, h.actor, h.changed_at
FROM new_invoice n
JOIN shard_map m ON m.shard_id = n.tenant_id
JOIN invoices si ON si.tenant_id = m.source_id AND si.invoice_number = n.invoice_number
JOIN invoice_status_history h ON h.invoice_id = si.id;

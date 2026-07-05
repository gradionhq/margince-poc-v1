# 04 — crm.yaml: `login` operation carries the `security` key twice

**Status:** open

## What the spec says

`spec/contract/crm.yaml`, the `POST /auth/login` operation, defines
`security: []` twice in the same mapping: once directly under the
operation head (with the `# pre-auth endpoint` comment, added by the
human-only/pre-auth sweep) and once after the `description` block (the
pre-existing line).

## Why it's a defect

Duplicate mapping keys are invalid YAML (a strict parser rejects the
document; `yaml.v3` and kin-openapi both fail). Any consumer that loads
the contract strictly — including this repo's codegen pipeline — cannot
parse the file as shipped.

## What this repo did

The synced `backend/api/crm.yaml` keeps the first occurrence (the
commented pre-auth override) and drops the duplicate.

## Proposed spec change

Delete the second `security: []` line inside the `login` operation.

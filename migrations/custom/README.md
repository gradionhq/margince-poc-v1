# custom/ — the fork-owned migration namespace (ADR-0017)

Upstream ships this directory empty. A fork's agent-authored migrations
land here as `<YYYYMMDDHHMMSS>_<name>.up.sql` / `.down.sql` pairs, tracked
in `schema_migrations_custom`, applied after every `core/` migration.
Custom columns carry the `x_` prefix so they can never collide with an
upstream column on upgrade.

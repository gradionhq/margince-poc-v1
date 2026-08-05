# Vendored SPDX JSON schemas (SBOM validation)

`make sbom-validate` checks each generated SBOM against its format schema. The
CycloneDX document is validated by the first-party `cyclonedx-cli`, which bundles
its own spec schemas; the two SPDX documents have no maintained validator image,
so they are validated against these pinned upstream schemas with a generic
JSON-schema CLI (see the `SBOM` section of the root `Makefile`).

The schemas are vendored rather than fetched at build time for the same reason
the validator images are digest-pinned: a schema-hosting URL that moves or a
tampered payload must not be able to change what the gate accepts. Bump a schema
by replacing the file from the source below and noting the change in the commit.

| File | SPDX version | Source |
| --- | --- | --- |
| `spdx-2.2.1.schema.json` | 2.2.1 | https://github.com/spdx/spdx-spec/blob/v2.2.1/schemas/spdx-schema.json |
| `spdx-3.0.1.schema.json` | 3.0.1 | https://spdx.org/schema/3.0.1/spdx-json-schema.json |

These files are upstream SPDX artifacts, not shipped product, so `.syft.yaml`
excludes this directory from the scan.

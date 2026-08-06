# Pinned SBOM-validation inputs

`make sbom-validate` checks each generated SBOM against its format. The
validators and the assets in this directory:

| Format | Validator | Asset here |
| --- | --- | --- |
| CycloneDX | first-party `cyclonedx-cli` (bundles its spec schema) | — |
| SPDX 2.2.1 | `pyspdxtools` (spdx/tools-python), structural **and** semantic | `spdx-tools-requirements.txt` |
| SPDX 3.0.1 | generic JSON-schema validation | `spdx-3.0.1.schema.json` |

`pyspdxtools` is the canonical SPDX 2.x validator; it runs in a digest-pinned
Python image with the hash-pinned dependency set in `spdx-tools-requirements.txt`,
so it is as reproducible as the digest-pinned SBOM images (regeneration steps are
in that file's header). It exits non-zero on an invalid document, so the Makefile
recipe gates on its exit status and surfaces its report for context on failure.

## Why SPDX 3.0 is JSON-schema only (spike finding)

SPDX 3.0's semantic layer is SHACL, and the SPDX project points at
[`spdx3-validate`](https://github.com/JPEWdev/spdx3-validate) /
[`pyshacl`](https://github.com/spdx/spdx-3-model/blob/develop/serialization/jsonld/validation.md)
for it. Neither is usable as a gate here today: run against syft 1.50's own
SPDX 3.0 output, `spdx3-validate` reports ~1100 `sh:ClassConstraintComponent`
violations (every `Element` reference fails the class check — the model is loaded
without the OWL inference that makes `software_File` a subclass of `Element`),
takes ~4 minutes, and fetches the model from spdx.org on every run. Gating on it
would fail `make sbom` on a document we do not own (syft's writer) and cannot
patch (contract-first). So SPDX 3.0 gets structural JSON-schema validation — the
layer the SPDX 3 model doc calls the structural check — which passes on syft's
output and is offline and fast. Revisit when syft's SPDX 3.0 writer and the SHACL
tooling both mature.

## The vendored schema

`spdx-3.0.1.schema.json` is vendored rather than fetched at build time for the
same reason the validator images are digest-pinned: a schema-hosting URL that
moves or a tampered payload must not change what the gate accepts. Bump it by
replacing the file from its source and noting the change in the commit.

| File | Source |
| --- | --- |
| `spdx-3.0.1.schema.json` | https://spdx.org/schema/3.0.1/spdx-json-schema.json |

These are validation inputs, not shipped product, but `.syft.yaml` includes this
committed directory so the SBOM covers the complete committed tree.

# The served MCP surface

<!-- Generated together with mcp-info.json; do not edit by hand. -->

Generated from the served MCP surface by `go test ./internal/compose/ -run TestPublishedMCPSurface -update-mcp-info`; do not edit by hand. This is the ALL-SCOPE view: tools/list and resources/list are both filtered per caller, so a passport holding fewer scopes is served less than this. It is the CORE catalog: extension units register onto the same registry and are not composed here. It is captured as an Apps-capable host sees it, so a tool bound to a view carries `_meta.ui.resourceUri`; a client that does not declare the UI extension is served no such member. The `ui://` view descriptors ARE included, and a deployment publishes each only once its boot has fetched and admitted that document, so an api serving neither advertises neither.

`mcp-info.json` beside this page is the same surface byte for byte, as a client
receives it. This page is rendered from that file.

## Totals

| | |
|---|---:|
| Tools | 37 |
| Resources | 7 |
| Tool catalog | 102.5 KB |
| Resource catalog | 2.7 KB |
| Approx. wire tokens | 26943 |
| Largest tool | `run_report` (4.1 KB) |
| Scopes rendered | `read`, `draft`, `write`, `send`, `enrich` |

Those are the WIRE bytes: they carry each tool's output schema and the governance
clause the transport appends. The Surface-B listing a run re-sends every step is
smaller — name, description and input schema only — and is held against its own
budget in `agenttooldescriptions_test.go`.

## Index

### Resources (7)

- [`margince://schema/query`](#query_vocabulary) — Workspace query vocabulary
- [`margince://schema/record-fields`](#record_fields) — Record write vocabulary
- [`ui://margince/account-brief.html`](#account_brief_view) — Morning brief
- [`ui://margince/relationship-map.html`](#relationship_map_view) — Who knows this contact
- [`ui://margince/commitments.html`](#commitments_view) — Open commitments
- [`ui://margince/handoff.html`](#handoff_view) — Delivery handoff
- [`ui://margince/pipeline-review.html`](#pipeline_review_view) — Pipeline review

### Tools (37)

| Tool | What it is for | Read-only | View | Size |
|---|---|:-:|---|---:|
| [`account_coverage`](#account_coverage) | Relationship coverage on a deal | yes |  | 2.6 KB |
| [`advance_deal`](#advance_deal) | Advance a deal to a stage |  |  | 2.9 KB |
| [`advance_project_phase`](#advance_project_phase) | Move a project to a phase |  |  | 2.3 KB |
| [`archive_record`](#archive_record) | Archive a record |  |  | 2.3 KB |
| [`at_risk_relationships`](#at_risk_relationships) | Relationships going cold | yes |  | 2.4 KB |
| [`book_meeting`](#book_meeting) | Book a meeting |  |  | 3.0 KB |
| [`catch_me_up_on`](#catch_me_up_on) | Catch me up on a record | yes |  | 2.6 KB |
| [`check_availability`](#check_availability) | Check calendar availability | yes |  | 2.3 KB |
| [`create_record`](#create_record) | Create a record |  |  | 2.7 KB |
| [`disqualify_lead`](#disqualify_lead) | Disqualify a lead |  |  | 2.0 KB |
| [`draft_email`](#draft_email) | Draft an email reply |  |  | 2.4 KB |
| [`draft_follow_ups_for`](#draft_follow_ups_for) | Draft follow-ups |  |  | 2.7 KB |
| [`enrich`](#enrich) | Enrich an organization from its website |  |  | 2.6 KB |
| [`intro_path_to`](#intro_path_to) | Find a warm introduction path | yes |  | 2.3 KB |
| [`list_pipelines`](#list_pipelines) | List pipelines and their stages | yes |  | 2.3 KB |
| [`list_records`](#list_records) | List records | yes |  | 3.1 KB |
| [`log_activity`](#log_activity) | Log an activity |  |  | 3.1 KB |
| [`merge_records`](#merge_records) | Merge two records |  |  | 2.5 KB |
| [`prep_for_meeting`](#prep_for_meeting) | Prepare for a meeting | yes |  | 2.9 KB |
| [`prepare_handoff`](#prepare_handoff) | Prepare a delivery handoff | yes | [`ui://margince/handoff.html`](#handoff_view) | 3.8 KB |
| [`progress_deal`](#progress_deal) | Progress a deal with a note |  |  | 3.1 KB |
| [`promote_lead`](#promote_lead) | Promote a lead to a person |  |  | 2.6 KB |
| [`qualify_lead`](#qualify_lead) | Qualify a lead |  |  | 2.5 KB |
| [`query_workspace`](#query_workspace) | Query the workspace | yes |  | 3.6 KB |
| [`read_brief`](#read_brief) | Read the morning brief | yes | [`ui://margince/account-brief.html`](#account_brief_view) | 2.8 KB |
| [`read_record`](#read_record) | Read a record | yes |  | 1.9 KB |
| [`relink_activity`](#relink_activity) | Re-associate an activity to a record |  |  | 2.3 KB |
| [`resolve_entities`](#resolve_entities) | Resolve people and companies | yes |  | 3.6 KB |
| [`review_commitments`](#review_commitments) | Review open commitments | yes | [`ui://margince/commitments.html`](#commitments_view) | 2.8 KB |
| [`run_report`](#run_report) | Run a report | yes |  | 4.0 KB |
| [`search_context`](#search_context) | Search for relevant material | yes |  | 3.0 KB |
| [`search_records`](#search_records) | Search records | yes |  | 2.5 KB |
| [`send_email`](#send_email) | Send an email |  |  | 2.6 KB |
| [`send_message`](#send_message) | Reply on a channel conversation |  |  | 2.4 KB |
| [`update_record`](#update_record) | Update a record |  |  | 3.9 KB |
| [`whats_slipping_this_week`](#whats_slipping_this_week) | What's slipping this week | yes | [`ui://margince/pipeline-review.html`](#pipeline_review_view) | 2.3 KB |
| [`who_knows`](#who_knows) | Who knows this contact | yes | [`ui://margince/relationship-map.html`](#relationship_map_view) | 2.2 KB |

## Resources

A resource takes no arguments and changes nothing, so it carries no autonomy
tier — but it is scope-filtered exactly as a tool is, so a passport holding
fewer scopes is served fewer documents.

### query_vocabulary

`margince://schema/query` · application/json

**Workspace query vocabulary**

Everything a query plan may say, for you: the record types you can ask about, the fields you can name on each, the operators each field admits, and the single relationship hop a plan may take. A plan naming anything outside it is refused rather than approximated.

### record_fields

`margince://schema/record-fields` · application/json

**Record write vocabulary**

The fields create_record and update_record accept for each record_type: which are required, what shape each takes, and the values the closed ones admit. The two tools name this document instead of carrying it.

### account_brief_view

`ui://margince/account-brief.html` · text/html;profile=mcp-app

**Morning brief**

The ranked brief queue, with the factor decomposition each item ranked on.

<details><summary>Sandbox policy (<code>_meta.ui</code>)</summary>

```json
{
  "ui": {
    "csp": {
      "baseUriDomains": [],
      "connectDomains": [],
      "frameDomains": [],
      "resourceDomains": []
    },
    "prefersBorder": true
  }
}
```

</details>

### relationship_map_view

`ui://margince/relationship-map.html` · text/html;profile=mcp-app

**Who knows this contact**

The colleagues who know a contact, warmest first, with the interactions behind each warmth band.

<details><summary>Sandbox policy (<code>_meta.ui</code>)</summary>

```json
{
  "ui": {
    "csp": {
      "baseUriDomains": [],
      "connectDomains": [],
      "frameDomains": [],
      "resourceDomains": []
    },
    "prefersBorder": true
  }
}
```

</details>

### commitments_view

`ui://margince/commitments.html` · text/html;profile=mcp-app

**Open commitments**

The promises still outstanding, oldest first, with who owes each one and how far past due it is.

<details><summary>Sandbox policy (<code>_meta.ui</code>)</summary>

```json
{
  "ui": {
    "csp": {
      "baseUriDomains": [],
      "connectDomains": [],
      "frameDomains": [],
      "resourceDomains": []
    },
    "prefersBorder": true
  }
}
```

</details>

### handoff_view

`ui://margince/handoff.html` · text/html;profile=mcp-app

**Delivery handoff**

What the delivery side is being given for one project, with each gap beside the fact it is about.

<details><summary>Sandbox policy (<code>_meta.ui</code>)</summary>

```json
{
  "ui": {
    "csp": {
      "baseUriDomains": [],
      "connectDomains": [],
      "frameDomains": [],
      "resourceDomains": []
    },
    "prefersBorder": true
  }
}
```

</details>

### pipeline_review_view

`ui://margince/pipeline-review.html` · text/html;profile=mcp-app

**Pipeline review**

The deals at risk this week, worst first, with the evidence each risk claim rests on.

<details><summary>Sandbox policy (<code>_meta.ui</code>)</summary>

```json
{
  "ui": {
    "csp": {
      "baseUriDomains": [],
      "connectDomains": [],
      "frameDomains": [],
      "resourceDomains": []
    },
    "prefersBorder": true
  }
}
```

</details>

## Tools

### account_coverage

**Relationship coverage on a deal**

Answer "is this deal covered?": which roles on the account we have a relationship with, and where the deal is exposed to a single contact. It assesses the relationships recorded against one deal's account, not the deal's commercial health — nothing here says whether the deal will close. Use whats_slipping_this_week for deals at risk of stalling, and intro_path_to when the answer is that a gap needs a warm route filling it. Keep the deal_id and the named gaps; they are what a follow-up plan is built from. (Governance: runs immediately; requires passport scope "read".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "deal_id": {
      "description": "The deal to assess",
      "format": "uuid",
      "type": "string"
    }
  },
  "required": [
    "deal_id"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "deal_id": {
          "format": "uuid",
          "type": "string"
        },
        "our_side": {
          "items": {
            "properties": {
              "display_name": {
                "type": "string"
              },
              "interactions_90d": {
                "type": "integer"
              },
              "strength": {
                "type": "integer"
              },
              "strength_bucket": {
                "type": "string"
              },
              "user_id": {
                "format": "uuid",
                "type": "string"
              }
            },
            "required": [
              "display_name",
              "interactions_90d",
              "strength_bucket",
              "user_id"
            ],
            "type": "object"
          },
          "type": "array"
        },
        "risks": {
          "items": {
            "properties": {
              "days_since_touch": {
                "type": "integer"
              },
              "kind": {
                "type": "string"
              },
              "person_ids": {
                "items": {
                  "format": "uuid",
                  "type": "string"
                },
                "type": "array"
              },
              "summary": {
                "type": "string"
              },
              "user_ids": {
                "items": {
                  "format": "uuid",
                  "type": "string"
                },
                "type": "array"
              }
            },
            "required": [
              "kind",
              "summary"
            ],
            "type": "object"
          },
          "type": "array"
        },
        "stakeholders": {
          "items": {
            "properties": {
              "engaged": {
                "type": "boolean"
              },
              "person_id": {
                "format": "uuid",
                "type": "string"
              },
              "role": {
                "type": "string"
              }
            },
            "required": [
              "engaged",
              "person_id",
              "role"
            ],
            "type": "object"
          },
          "type": "array"
        }
      },
      "required": [
        "deal_id",
        "our_side",
        "risks",
        "stakeholders"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### advance_deal

**Advance a deal to a stage**

Move a deal to a different stage of its pipeline. The stage is named by id, not by label, and the id of the stage you are moving TO comes from list_pipelines — call it first, because a deal you have read carries only the stage it is already in. Moving onto a stage that closes the deal as won or lost is a decision a person makes: it is staged for approval and needs a lost_reason when the stage is a losing one. Read the target stage's semantic rather than guessing it from its name. Use progress_deal when the move should also leave a note explaining it, which is almost always what a person means by moving a deal on. Send if_version with the version you read of the deal, and keep the staged approval id when a closing move comes back for approval. (Governance: some calls run immediately and others a person approves first, decided per call from its arguments; requires passport scope "write".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "approval_id": {
      "description": "Set on retry after a human approved a won/lost move",
      "format": "uuid",
      "type": "string"
    },
    "deal_id": {
      "format": "uuid",
      "type": "string"
    },
    "idempotency_key": {
      "description": "Optional. Repeating a call under the same key returns the first result instead of acting twice; different arguments under one key are refused.",
      "maxLength": 255,
      "type": "string"
    },
    "if_version": {
      "type": "integer"
    },
    "lost_reason": {
      "description": "Required when the target stage closes the deal as lost",
      "type": "string"
    },
    "to_stage_id": {
      "description": "The target stage, by id — obtain it from list_pipelines, since a deal you have read carries only the stage it is already IN. That stage's semantic decides what happens next: open executes immediately, won or lost is staged for a human's approval.",
      "format": "uuid",
      "type": "string"
    }
  },
  "required": [
    "deal_id",
    "to_stage_id"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "fields": {
          "type": "object"
        },
        "id": {
          "format": "uuid",
          "type": "string"
        },
        "record_type": {
          "type": "string"
        },
        "trust_tier": {
          "type": "string"
        },
        "version": {
          "type": "integer"
        }
      },
      "required": [
        "fields",
        "id",
        "record_type"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### advance_project_phase

**Move a project to a phase**

Move a project to another phase — initiative, pursuing, delivering, closed. The four names are fixed but the order is not enforced: a project may go back a phase, and a closed one may be reopened. Closing requires a reason, which is recorded on the phase history either way. Use advance_deal for a deal's pipeline stages; a project's phases are a different vocabulary on a different record. Send if_version with the version you read; a person approves the move before it runs. (Governance: a person approves every call before it runs; requires passport scope "write".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "approval_id": {
      "description": "Set on retry after a human approved the staged call",
      "format": "uuid",
      "type": "string"
    },
    "idempotency_key": {
      "description": "Optional. Repeating a call under the same key returns the first result instead of acting twice; different arguments under one key are refused.",
      "maxLength": 255,
      "type": "string"
    },
    "if_version": {
      "description": "The version the caller read; the write is refused as skew if the project moved since",
      "type": "integer"
    },
    "project_id": {
      "format": "uuid",
      "type": "string"
    },
    "reason": {
      "description": "Required when to_phase is closed; recorded on the phase-history row either way",
      "type": "string"
    },
    "to_phase": {
      "enum": [
        "initiative",
        "pursuing",
        "delivering",
        "closed"
      ],
      "type": "string"
    }
  },
  "required": [
    "project_id",
    "to_phase"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "id": {
          "format": "uuid",
          "type": "string"
        }
      },
      "required": [
        "id"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### archive_record

**Archive a record**

Retire a record that should no longer be worked — a duplicate, a dead account, a project that ended. Archiving hides the record from day-to-day work; it does not delete it and does not move anything attached to it, so an archived duplicate still holds the activities and deals that were logged against it. Use merge_records when a duplicate's history should end up on the record that survives, and disqualify_lead when a lead is going nowhere — a lead's own transition records the reason where archiving would not. A person approves this call before it runs; do not report the record as archived until the retry that carries their approval has answered. (Governance: a person approves every call before it runs; requires passport scope "write".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "approval_id": {
      "description": "Set on retry after a human approved the staged call",
      "format": "uuid",
      "type": "string"
    },
    "id": {
      "format": "uuid",
      "type": "string"
    },
    "idempotency_key": {
      "description": "Optional. Repeating a call under the same key returns the first result instead of acting twice; different arguments under one key are refused.",
      "maxLength": 255,
      "type": "string"
    },
    "record_type": {
      "enum": [
        "person",
        "organization",
        "deal",
        "project",
        "relationship"
      ],
      "type": "string"
    }
  },
  "required": [
    "record_type",
    "id"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "archived": {
          "type": "boolean"
        },
        "id": {
          "format": "uuid",
          "type": "string"
        },
        "record_type": {
          "type": "string"
        }
      },
      "required": [
        "archived",
        "id",
        "record_type"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### at_risk_relationships

**Relationships going cold**

Answer "where are our relationships thin?": across the caller's OPEN deals, the ones resting on a single contact, missing an engaged champion, or carried almost entirely by one person on our side. It sweeps open deals — a deal already won or lost is not at risk and is left out — and it takes no arguments, because the caller's own visibility already decides which deals these are. It is about the shape of the relationships around a deal, not about the deal's own momentum. Use whats_slipping_this_week when the question is about deals losing momentum, and account_coverage when the question is about one deal rather than the whole book. Each finding names its deal_id and the people it is about; those are what intro_path_to and who_knows take next. (Governance: runs immediately; requires passport scope "read".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {},
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "deals": {
          "items": {
            "properties": {
              "deal_id": {
                "format": "uuid",
                "type": "string"
              },
              "name": {
                "type": "string"
              },
              "risks": {
                "items": {
                  "properties": {
                    "days_since_touch": {
                      "type": "integer"
                    },
                    "kind": {
                      "type": "string"
                    },
                    "person_ids": {
                      "items": {
                        "format": "uuid",
                        "type": "string"
                      },
                      "type": "array"
                    },
                    "summary": {
                      "type": "string"
                    },
                    "user_ids": {
                      "items": {
                        "format": "uuid",
                        "type": "string"
                      },
                      "type": "array"
                    }
                  },
                  "required": [
                    "kind",
                    "summary"
                  ],
                  "type": "object"
                },
                "type": "array"
              }
            },
            "required": [
              "deal_id",
              "name",
              "risks"
            ],
            "type": "object"
          },
          "type": "array"
        },
        "deals_scanned": {
          "type": "integer"
        },
        "truncated": {
          "type": "boolean"
        }
      },
      "required": [
        "deals",
        "deals_scanned",
        "truncated"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### book_meeting

**Book a meeting**

Hold a slot in the host's calendar and record the meeting against the people, accounts or deals it is about. It requires at least one link saying what the meeting is about and is refused without one. The slot is taken and the meeting becomes a real commitment, so a person approves it before it is booked. It takes no attendee list: who is invited, and whether an invitation is delivered at all, is the deployment's calendar connection rather than this call. Check the slot is free first — this tool does not. Use check_availability to find the time, and log_activity to record a meeting that already happened. Keep the staged approval id and re-send the identical start, end and links: the approval is bound to the meeting as it was described. (Governance: a person approves every call before it runs; requires passport scope "send".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "approval_id": {
      "description": "Set on retry after a human approved the staged call",
      "format": "uuid",
      "type": "string"
    },
    "end": {
      "description": "RFC 3339 with a zone offset — 2026-07-31T16:35:00+07:00 or 2026-07-31T09:35:00Z. A bare local time without an offset is refused.",
      "format": "date-time",
      "type": "string"
    },
    "host_user_id": {
      "format": "uuid",
      "type": "string"
    },
    "idempotency_key": {
      "description": "Optional. Repeating a call under the same key returns the first result instead of acting twice; different arguments under one key are refused.",
      "maxLength": 255,
      "type": "string"
    },
    "links": {
      "description": "Who and what the meeting is about; at least one. The booking is refused without it.",
      "items": {
        "additionalProperties": false,
        "properties": {
          "entity_id": {
            "format": "uuid",
            "type": "string"
          },
          "entity_type": {
            "enum": [
              "person",
              "organization",
              "deal",
              "lead"
            ],
            "type": "string"
          }
        },
        "required": [
          "entity_type",
          "entity_id"
        ],
        "type": "object"
      },
      "maxItems": 25,
      "minItems": 1,
      "type": "array"
    },
    "start": {
      "description": "RFC 3339 with a zone offset — 2026-07-31T16:35:00+07:00 or 2026-07-31T09:35:00Z. A bare local time without an offset is refused.",
      "format": "date-time",
      "type": "string"
    },
    "subject": {
      "type": "string"
    }
  },
  "required": [
    "start",
    "end",
    "links"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "id": {
          "format": "uuid",
          "type": "string"
        }
      },
      "required": [
        "id"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### catch_me_up_on

**Catch me up on a record**

Answer "what has been going on with this?" for one person, account, deal, lead, project or captured meeting: the recent activity and the related records, assembled into one picture with the evidence each part rests on. It is built around ONE record you name, and everything it reports carries a source; what cannot be evidenced is absent rather than inferred. Use prep_for_meeting when the goal is a meeting about to happen, read_record when you only need the record's own stored fields, and search_records when you do not yet know which record you mean. Each item carries the record_type and record_id it came from — those are what a follow-up call acts on. (Governance: runs immediately; requires passport scope "read".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "max_items": {
      "maximum": 20,
      "minimum": 1,
      "type": "integer"
    },
    "record_id": {
      "format": "uuid",
      "type": "string"
    },
    "record_type": {
      "enum": [
        "person",
        "organization",
        "deal",
        "lead",
        "project",
        "activity"
      ],
      "type": "string"
    }
  },
  "required": [
    "record_type",
    "record_id"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "anchor": {
          "properties": {
            "record_id": {
              "format": "uuid",
              "type": "string"
            },
            "record_type": {
              "type": "string"
            }
          },
          "required": [
            "record_id",
            "record_type"
          ],
          "type": "object"
        },
        "sections": {
          "items": {
            "properties": {
              "items": {
                "items": {
                  "properties": {
                    "evidence": {
                      "items": {
                        "properties": {
                          "snippet": {
                            "type": "string"
                          },
                          "source": {
                            "type": "string"
                          }
                        },
                        "required": [
                          "snippet",
                          "source"
                        ],
                        "type": "object"
                      },
                      "type": "array"
                    },
                    "record_id": {
                      "format": "uuid",
                      "type": "string"
                    },
                    "record_type": {
                      "type": "string"
                    },
                    "summary": {
                      "type": "string"
                    }
                  },
                  "required": [
                    "evidence",
                    "record_id",
                    "record_type",
                    "summary"
                  ],
                  "type": "object"
                },
                "type": "array"
              },
              "name": {
                "type": "string"
              }
            },
            "required": [
              "items",
              "name"
            ],
            "type": "object"
          },
          "type": "array"
        }
      },
      "required": [
        "anchor",
        "sections"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### check_availability

**Check calendar availability**

Find when a host is free, so a time can be proposed to someone. It reads free/busy over the window you ask for and books nothing. It answers for one host — the acting user unless another is named — not for the invitees. Use book_meeting once a time is chosen, and prep_for_meeting when a meeting already exists and the goal is walking in ready. Keep the exact start and end of the slot you intend to take; book_meeting takes those, and a slot re-derived later may no longer be free. (Governance: runs immediately; requires passport scope "read".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "duration_minutes": {
      "maximum": 480,
      "minimum": 15,
      "type": "integer"
    },
    "from": {
      "description": "RFC 3339 with a zone offset — 2026-07-31T16:35:00+07:00 or 2026-07-31T09:35:00Z. A bare local time without an offset is refused.",
      "format": "date-time",
      "type": "string"
    },
    "host_user_id": {
      "description": "Defaults to the acting principal's user",
      "format": "uuid",
      "type": "string"
    },
    "to": {
      "description": "RFC 3339 with a zone offset — 2026-07-31T16:35:00+07:00 or 2026-07-31T09:35:00Z. A bare local time without an offset is refused.",
      "format": "date-time",
      "type": "string"
    }
  },
  "required": [
    "from",
    "to"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "slots": {
          "items": {
            "properties": {
              "end": {
                "type": "string"
              },
              "start": {
                "type": "string"
              }
            },
            "required": [
              "end",
              "start"
            ],
            "type": "object"
          },
          "type": "array"
        },
        "truncated": {
          "type": "boolean"
        }
      },
      "required": [
        "slots",
        "truncated"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### create_record

**Create a record**

Create a person, organization, deal, lead, project, activity or relationship that does not exist yet. Creating a deal requires a pipeline_id and a stage_id, and list_pipelines is what yields them for a deal that does not exist yet. Only the fields the chosen record_type actually stores are accepted, and a field belonging to a neighbouring type is refused rather than dropped. Search first when the record might already exist — a second copy of a person or account is a problem that then needs merge_records to undo. The new record's id comes back in the result; keep it for anything that links to it. (Governance: runs immediately; requires passport scope "write".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "fields": {
      "description": "The crm.yaml body for the record_type. The fields each record_type takes, which of them are REQUIRED, and their shapes are published at margince://schema/record-fields — that document, not this description, is what says what a write may name. An extra key must be cf_\u003cslug\u003e for a custom field; any other key is refused BY NAME and never dropped in silence, so a wrong guess is answered with the vocabulary rather than lost.",
      "type": "object"
    },
    "idempotency_key": {
      "description": "Optional. Repeating a call under the same key returns the first result instead of acting twice; different arguments under one key are refused.",
      "maxLength": 255,
      "type": "string"
    },
    "record_type": {
      "enum": [
        "person",
        "organization",
        "deal",
        "lead",
        "activity",
        "project",
        "relationship"
      ],
      "type": "string"
    }
  },
  "required": [
    "record_type",
    "fields"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "fields": {
          "type": "object"
        },
        "id": {
          "format": "uuid",
          "type": "string"
        },
        "record_type": {
          "type": "string"
        },
        "trust_tier": {
          "type": "string"
        },
        "version": {
          "type": "integer"
        }
      },
      "required": [
        "fields",
        "id",
        "record_type"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### disqualify_lead

**Disqualify a lead**

Close out a lead that is not going anywhere, so it stops appearing as live work. It is the lead's own terminal state and keeps the record and its history; it is not a deletion and not an archive. Use promote_lead when engagement says the opposite, and qualify_lead when the lead is only missing information. A person approves this call before it runs; do not report the lead as disqualified until the retry carrying their approval has answered. (Governance: a person approves every call before it runs; requires passport scope "write".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "approval_id": {
      "description": "Set on retry after a human approved the staged call",
      "format": "uuid",
      "type": "string"
    },
    "idempotency_key": {
      "description": "Optional. Repeating a call under the same key returns the first result instead of acting twice; different arguments under one key are refused.",
      "maxLength": 255,
      "type": "string"
    },
    "lead_id": {
      "description": "The lead to disqualify",
      "format": "uuid",
      "type": "string"
    }
  },
  "required": [
    "lead_id"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "id": {
          "format": "uuid",
          "type": "string"
        }
      },
      "required": [
        "id"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### draft_email

**Draft an email reply**

Compose a reply to a conversation already recorded here, anchored on the thread it answers. It writes the message and stops. Nothing is sent, nobody is notified, and the draft is returned for a person — or a later send — to use. It needs the activity_id of the thread being replied to; it cannot compose from a goal alone. Read what comes back before sending it: where no drafting model is configured this falls back to a short deterministic note built from the thread's subject. Use draft_follow_ups_for to draft across a whole set of slipping deals at once, and send_email only once a drafted message exists to send. Keep the drafted subject and body and the activity_id; send_email takes all three, and re-writing the text between the draft and the send means a person approves one message and a different one goes out. (Governance: runs immediately; requires passport scope "draft".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "activity_id": {
      "description": "The thread being replied to",
      "format": "uuid",
      "type": "string"
    },
    "idempotency_key": {
      "description": "Optional. Repeating a call under the same key returns the first result instead of acting twice; different arguments under one key are refused.",
      "maxLength": 255,
      "type": "string"
    },
    "intent": {
      "description": "What the reply should accomplish",
      "type": "string"
    }
  },
  "required": [
    "activity_id"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "body": {
          "type": "string"
        },
        "in_reply_to_activity_id": {
          "format": "uuid",
          "type": "string"
        },
        "subject": {
          "type": "string"
        }
      },
      "required": [
        "body",
        "in_reply_to_activity_id",
        "subject"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### draft_follow_ups_for

**Draft follow-ups**

Draft a follow-up for each deal in a segment at once — today only the slipping deals — and leave each draft on its own deal's timeline. It writes drafts and sends none of them, and it drafts only for deals whose risk is evidenced, so it covers the same set whats_slipping_this_week reports. One call writes to many records, up to a server-side ceiling of 25. Use draft_email for one specific conversation; this tool answers "chase everything that is slipping", not "reply to this". Each draft comes back with its deal_id and draft_activity_id — those are how a person finds the drafts to review. (Governance: runs immediately; requires passport scope "draft".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "idempotency_key": {
      "description": "Optional. Repeating a call under the same key returns the first result instead of acting twice; different arguments under one key are refused.",
      "maxLength": 255,
      "type": "string"
    },
    "limit": {
      "description": "How many of the top-ranked deals to draft for; omit it for 25, the server-side ceiling on records one call may write",
      "maximum": 25,
      "minimum": 1,
      "type": "integer"
    },
    "segment": {
      "description": "The deal set to draft follow-ups for; drafts land on each deal's timeline and are NEVER sent",
      "enum": [
        "slipping"
      ],
      "type": "string"
    }
  },
  "required": [
    "segment"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "drafts": {
          "items": {
            "properties": {
              "deal_id": {
                "format": "uuid",
                "type": "string"
              },
              "draft_activity_id": {
                "format": "uuid",
                "type": "string"
              },
              "evidence": {
                "items": {
                  "properties": {
                    "snippet": {
                      "type": "string"
                    },
                    "source": {
                      "type": "string"
                    }
                  },
                  "required": [
                    "snippet",
                    "source"
                  ],
                  "type": "object"
                },
                "type": "array"
              },
              "summary": {
                "type": "string"
              }
            },
            "required": [
              "deal_id",
              "draft_activity_id",
              "evidence",
              "summary"
            ],
            "type": "object"
          },
          "type": "array"
        },
        "segment": {
          "type": "string"
        }
      },
      "required": [
        "drafts",
        "segment"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### enrich

**Enrich an organization from its website**

Learn about an organization by reading its public website, and propose what was found for a person to accept onto the record. It reaches OUTSIDE the workspace, so a person approves the call before it runs, and what it returns is a proposal — nothing lands on the record until someone accepts it. Reading one page answers immediately; reading a whole site is queued and answers with a read id rather than the content. What it finds is captured text from a third party, not a fact this workspace has verified. Use qualify_lead when the missing values are already derivable from the record itself, which costs no external read and needs no approval. Keep the organization_id you enriched, and the read id when a whole-site read was queued — the result is collected against it later. (Governance: a person approves every call before it runs; requires passport scope "enrich".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "approval_id": {
      "description": "Set on retry after a human approved the staged call",
      "format": "uuid",
      "type": "string"
    },
    "depth": {
      "default": "page",
      "description": "page reads one page and returns a staged proposal; site queues a multi-page crawl and returns its read id",
      "enum": [
        "page",
        "site"
      ],
      "type": "string"
    },
    "idempotency_key": {
      "description": "Optional. Repeating a call under the same key returns the first result instead of acting twice; different arguments under one key are refused.",
      "maxLength": 255,
      "type": "string"
    },
    "organization_id": {
      "description": "The organization to enrich",
      "format": "uuid",
      "type": "string"
    },
    "url": {
      "description": "Absolute http(s) URL to read instead of the organization's own domain",
      "format": "uri",
      "type": "string"
    }
  },
  "required": [
    "organization_id"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### intro_path_to

**Find a warm introduction path**

Find a warm route into a company: who we already know there, and which colleague could make the introduction. It walks the relationships this workspace has recorded. An account nobody here has ever spoken to has no warm path, and saying so is the correct answer rather than a failure. Use who_knows when you already have the specific person and want the colleagues who know THEM, and search_records when you are still looking for the account itself. The path names the colleague and the contact by id; both are needed to ask anyone for the introduction. (Governance: runs immediately; requires passport scope "read".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "organization_id": {
      "description": "The account to find a warm route into",
      "format": "uuid",
      "type": "string"
    }
  },
  "required": [
    "organization_id"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "candidates_truncated": {
          "type": "boolean"
        },
        "organization_id": {
          "format": "uuid",
          "type": "string"
        },
        "routes": {
          "items": {
            "properties": {
              "display_name": {
                "type": "string"
              },
              "interactions_90d": {
                "type": "integer"
              },
              "person_id": {
                "format": "uuid",
                "type": "string"
              },
              "person_name": {
                "type": "string"
              },
              "strength": {
                "type": "integer"
              },
              "strength_bucket": {
                "type": "string"
              },
              "user_id": {
                "format": "uuid",
                "type": "string"
              }
            },
            "required": [
              "display_name",
              "interactions_90d",
              "person_id",
              "person_name",
              "strength_bucket",
              "user_id"
            ],
            "type": "object"
          },
          "type": "array"
        }
      },
      "required": [
        "candidates_truncated",
        "organization_id",
        "routes"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### list_pipelines

**List pipelines and their stages**

List every pipeline this workspace has with its live stages — the configuration the deal-shaped writes are named against. It is where the id of a stage a deal could move TO comes from, so a deal cannot be created, or moved anywhere new, without calling this first — a deal you have already read carries only the stage it is in. Each stage carries a semantic — open, won or lost — and that, not its name, is what decides whether moving onto it needs a person's approval; a stage called "Closed" may be either. Keep the pipeline_id and the stage_id of the stage you mean: create_record for a deal requires both, and advance_deal and progress_deal take that stage_id as their to_stage_id. (Governance: runs immediately; requires passport scope "read".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {},
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "pipelines": {
          "items": {
            "properties": {
              "id": {
                "format": "uuid",
                "type": "string"
              },
              "is_default": {
                "type": "boolean"
              },
              "name": {
                "type": "string"
              },
              "position": {
                "type": "integer"
              },
              "stages": {
                "items": {
                  "properties": {
                    "id": {
                      "format": "uuid",
                      "type": "string"
                    },
                    "name": {
                      "type": "string"
                    },
                    "position": {
                      "type": "integer"
                    },
                    "semantic": {
                      "type": "string"
                    },
                    "win_probability": {
                      "type": "integer"
                    }
                  },
                  "required": [
                    "id",
                    "name",
                    "position",
                    "semantic",
                    "win_probability"
                  ],
                  "type": "object"
                },
                "type": "array"
              }
            },
            "required": [
              "id",
              "is_default",
              "name",
              "position",
              "stages"
            ],
            "type": "object"
          },
          "type": "array"
        }
      },
      "required": [
        "pipelines"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### list_records

**List records**

Enumerate the people, organizations, deals, leads or projects that meet exact conditions — every deal in one pipeline, the leads one person owns, the projects still being delivered. It narrows only by the filters this workspace publishes for that record_type, which the schema lists per type, and it answers ONE page: the set continues past it. Use search_records when the question is what a record is called rather than which records meet a condition, and run_report when the answer is a count or a total rather than the records themselves. Keep next_cursor and pass it back to read the next page — a second call without it re-reads the first one. (Governance: runs immediately; requires passport scope "read".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "cursor": {
      "description": "Keyset cursor from a previous page's next_cursor",
      "type": "string"
    },
    "filters": {
      "additionalProperties": {
        "type": "string"
      },
      "description": "Narrow the list. Every operand is a string, booleans included (\"true\"). Each record_type takes only its own filters: person — owner_id organization — lifecycle (unknown|target|prospect|opportunity|customer|former_customer|disqualified), owner_id, relationship_type (customer|partner|supplier|investor|portfolio_company|competitor|other) deal — organization_id, owner_id, partner_org_id, partner_sourced (boolean), pipeline_id, project_id, stage_id, stalled (boolean), status (open|won|lost) lead — owner_id, status (new|working|promoted|disqualified) project — key, organization_id, owner_id, phase (initiative|pursuing|delivering|closed) A pipeline_id or stage_id comes from list_pipelines; nothing else on this surface yields one.",
      "type": "object"
    },
    "limit": {
      "maximum": 50,
      "minimum": 1,
      "type": "integer"
    },
    "record_type": {
      "enum": [
        "person",
        "organization",
        "deal",
        "lead",
        "project"
      ],
      "type": "string"
    }
  },
  "required": [
    "record_type"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "next_cursor": {
          "type": "string"
        },
        "records": {
          "items": {
            "properties": {
              "fields": {
                "type": "object"
              },
              "id": {
                "format": "uuid",
                "type": "string"
              },
              "record_type": {
                "type": "string"
              },
              "trust_tier": {
                "type": "string"
              },
              "version": {
                "type": "integer"
              }
            },
            "required": [
              "fields",
              "id",
              "record_type"
            ],
            "type": "object"
          },
          "type": "array"
        }
      },
      "required": [
        "records"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### log_activity

**Log an activity**

Record something that happened — a call, a meeting, a note, a message — on the timeline of the people, accounts and deals it was about. It writes history, and changes nothing else: logging a call does not move a deal, update a field or notify anyone. An activity logged without links is stored attached to nothing and appears on no timeline. Use progress_deal when the same event also moves a deal forward, so the move and the note are one act rather than two. The activity id comes back in the result; keep it — draft_email, send_email and send_message all identify a conversation by it. (Governance: runs immediately; requires passport scope "write".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "body": {
      "type": "string"
    },
    "direction": {
      "enum": [
        "inbound",
        "outbound"
      ],
      "type": "string"
    },
    "due_at": {
      "description": "RFC 3339 with a zone offset — 2026-07-31T16:35:00+07:00 or 2026-07-31T09:35:00Z. A bare local time without an offset is refused.",
      "format": "date-time",
      "type": "string"
    },
    "idempotency_key": {
      "description": "Optional. Repeating a call under the same key returns the first result instead of acting twice; different arguments under one key are refused.",
      "maxLength": 255,
      "type": "string"
    },
    "kind": {
      "enum": [
        "email",
        "call",
        "meeting",
        "note",
        "task",
        "whatsapp",
        "telegram"
      ],
      "type": "string"
    },
    "links": {
      "description": "What the activity is about. Omit it and the activity is stored unattached to any record — it will not appear on a person's, company's or deal's timeline.",
      "items": {
        "additionalProperties": false,
        "properties": {
          "entity_id": {
            "format": "uuid",
            "type": "string"
          },
          "entity_type": {
            "enum": [
              "person",
              "organization",
              "deal",
              "lead"
            ],
            "type": "string"
          }
        },
        "required": [
          "entity_type",
          "entity_id"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "occurred_at": {
      "description": "RFC 3339 with a zone offset — 2026-07-31T16:35:00+07:00 or 2026-07-31T09:35:00Z. A bare local time without an offset is refused.",
      "format": "date-time",
      "type": "string"
    },
    "source_id": {
      "type": "string"
    },
    "source_system": {
      "type": "string"
    },
    "subject": {
      "type": "string"
    }
  },
  "required": [
    "kind"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "fields": {
          "type": "object"
        },
        "id": {
          "format": "uuid",
          "type": "string"
        },
        "record_type": {
          "type": "string"
        },
        "trust_tier": {
          "type": "string"
        },
        "version": {
          "type": "integer"
        }
      },
      "required": [
        "fields",
        "id",
        "record_type"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### merge_records

**Merge two records**

Collapse two records for the same real person or company into one, moving the source's activities, deals and links onto the record that survives. People merge with people and organizations with organizations; the source is archived and redirected to the target, and the direction is not reversible by calling this again the other way round. Use archive_record when the extra record has nothing worth keeping, rather than merging to make it disappear. target_id is the record that survives and source_id the one merged away — read both records before choosing, because a person approves the call as you described it. (Governance: a person approves every call before it runs; requires passport scope "write".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "approval_id": {
      "description": "Set on retry after a human approved the staged call",
      "format": "uuid",
      "type": "string"
    },
    "idempotency_key": {
      "description": "Optional. Repeating a call under the same key returns the first result instead of acting twice; different arguments under one key are refused.",
      "maxLength": 255,
      "type": "string"
    },
    "record_type": {
      "enum": [
        "person",
        "organization"
      ],
      "type": "string"
    },
    "source_id": {
      "description": "The record merged away (archived, redirected to the survivor)",
      "format": "uuid",
      "type": "string"
    },
    "target_id": {
      "description": "The surviving record everything relinks to",
      "format": "uuid",
      "type": "string"
    }
  },
  "required": [
    "record_type",
    "source_id",
    "target_id"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "merged": {
          "type": "boolean"
        },
        "record_type": {
          "type": "string"
        },
        "survivor_id": {
          "format": "uuid",
          "type": "string"
        }
      },
      "required": [
        "merged",
        "record_type",
        "survivor_id"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### prep_for_meeting

**Prepare for a meeting**

Get ready for a specific meeting: the same assembled picture as a catch-up, plus the open items pulled out as the things to raise. It is built around ONE record you name, and everything it reports carries a source; what cannot be evidenced is absent rather than inferred. Given a meeting it works out which record that meeting is about and names the others alongside. Use catch_me_up_on when there is no meeting and the question is simply what has been happening, and check_availability when the goal is finding a time rather than preparing for one. The focus list names the open items by record_id; those are what to act on after the meeting. prepared_for names the record the prep was built around. (Governance: runs immediately; requires passport scope "read".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "max_items": {
      "maximum": 20,
      "minimum": 1,
      "type": "integer"
    },
    "record_id": {
      "format": "uuid",
      "type": "string"
    },
    "record_type": {
      "enum": [
        "person",
        "organization",
        "deal",
        "lead",
        "project",
        "activity"
      ],
      "type": "string"
    }
  },
  "required": [
    "record_type",
    "record_id"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "briefing": {
          "properties": {
            "anchor": {
              "properties": {
                "record_id": {
                  "format": "uuid",
                  "type": "string"
                },
                "record_type": {
                  "type": "string"
                }
              },
              "required": [
                "record_id",
                "record_type"
              ],
              "type": "object"
            },
            "sections": {
              "items": {
                "properties": {
                  "items": {
                    "items": {
                      "properties": {
                        "evidence": {
                          "items": {
                            "properties": {
                              "snippet": {
                                "type": "string"
                              },
                              "source": {
                                "type": "string"
                              }
                            },
                            "required": [
                              "snippet",
                              "source"
                            ],
                            "type": "object"
                          },
                          "type": "array"
                        },
                        "record_id": {
                          "format": "uuid",
                          "type": "string"
                        },
                        "record_type": {
                          "type": "string"
                        },
                        "summary": {
                          "type": "string"
                        }
                      },
                      "required": [
                        "evidence",
                        "record_id",
                        "record_type",
                        "summary"
                      ],
                      "type": "object"
                    },
                    "type": "array"
                  },
                  "name": {
                    "type": "string"
                  }
                },
                "required": [
                  "items",
                  "name"
                ],
                "type": "object"
              },
              "type": "array"
            }
          },
          "required": [
            "anchor",
            "sections"
          ],
          "type": "object"
        },
        "meeting_focus": {
          "items": {
            "properties": {
              "record_id": {
                "format": "uuid",
                "type": "string"
              },
              "summary": {
                "type": "string"
              }
            },
            "required": [
              "record_id",
              "summary"
            ],
            "type": "object"
          },
          "type": "array"
        }
      },
      "required": [
        "briefing",
        "meeting_focus"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### prepare_handoff

**Prepare a delivery handoff**

Assemble what the delivery side of one project needs from the sales side: who owns it, who to call at the client, what was sold, by when, and what is already promised — with a named gap for each of those the records do not answer. It reports what the records say and reads nothing outside them; each gap names the field it was read off. It is scoped to the records the caller may see, so a gap means the field is empty as far as THEY can see, and a bounded list withholds the gaps that claim something is absent rather than guessing them. It changes nothing — preparing a handover is not performing one. Use catch_me_up_on when the question is what has been happening on the account rather than what a handover is missing, and read_record for the project's own stored fields alone. The project_id, and each gap's source field — the gaps are what a follow-up fills in. (Governance: runs immediately; requires passport scope "read".)

Renders its result in [`ui://margince/handoff.html`](#handoff_view), visible to `model`, `app`.

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "project_id": {
      "description": "The project being handed to delivery",
      "format": "uuid",
      "type": "string"
    }
  },
  "required": [
    "project_id"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "as_of": {
          "type": "string"
        },
        "deals": {
          "items": {
            "properties": {
              "amount_minor": {
                "type": "integer"
              },
              "currency": {
                "type": "string"
              },
              "deal_id": {
                "format": "uuid",
                "type": "string"
              },
              "name": {
                "type": "string"
              },
              "status": {
                "type": "string"
              }
            },
            "required": [
              "deal_id",
              "name",
              "status"
            ],
            "type": "object"
          },
          "type": "array"
        },
        "description": {
          "type": "string"
        },
        "gaps": {
          "items": {
            "properties": {
              "code": {
                "type": "string"
              },
              "message": {
                "type": "string"
              },
              "source": {
                "type": "string"
              }
            },
            "required": [
              "code",
              "message",
              "source"
            ],
            "type": "object"
          },
          "type": "array"
        },
        "key": {
          "type": "string"
        },
        "name": {
          "type": "string"
        },
        "open_commitments": {
          "items": {
            "properties": {
              "about": {
                "items": {
                  "properties": {
                    "entity_id": {
                      "format": "uuid",
                      "type": "string"
                    },
                    "entity_type": {
                      "type": "string"
                    },
                    "name": {
                      "type": "string"
                    }
                  },
                  "required": [
                    "entity_id",
                    "entity_type"
                  ],
                  "type": "object"
                },
                "type": "array"
              },
              "assignee_id": {
                "format": "uuid",
                "type": "string"
              },
              "assignee_name": {
                "type": "string"
              },
              "days_overdue": {
                "type": "integer"
              },
              "due_at": {
                "type": "string"
              },
              "state": {
                "type": "string"
              },
              "subject": {
                "type": "string"
              },
              "task_id": {
                "format": "uuid",
                "type": "string"
              }
            },
            "required": [
              "about",
              "state",
              "subject",
              "task_id"
            ],
            "type": "object"
          },
          "type": "array"
        },
        "organization_id": {
          "format": "uuid",
          "type": "string"
        },
        "owner_id": {
          "format": "uuid",
          "type": "string"
        },
        "owner_name": {
          "type": "string"
        },
        "phase": {
          "type": "string"
        },
        "project_id": {
          "format": "uuid",
          "type": "string"
        },
        "stakeholders": {
          "items": {
            "properties": {
              "name": {
                "type": "string"
              },
              "person_id": {
                "format": "uuid",
                "type": "string"
              },
              "role": {
                "type": "string"
              }
            },
            "required": [
              "person_id"
            ],
            "type": "object"
          },
          "type": "array"
        },
        "started_at": {
          "type": "string"
        },
        "target_end_date": {
          "type": "string"
        }
      },
      "required": [
        "as_of",
        "deals",
        "gaps",
        "name",
        "open_commitments",
        "organization_id",
        "phase",
        "project_id",
        "stakeholders"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### progress_deal

**Progress a deal with a note**

Move a deal to a new stage and leave a note on its timeline saying why, in one call. The move commits first and the note follows it, so a note that fails to write does not put the deal back — the answer says so, and the note is then log_activity's to retry. The note itself is optional. Same rules as the bare move otherwise: call list_pipelines for the id of the stage you are moving to, and closing a deal as won or lost is staged for a person to approve. Use advance_deal when there is genuinely nothing to say about the move, and log_activity when something happened but the deal did not move. Send if_version with the version you read of the deal; keep the staged approval id if a closing move is sent for approval. (Governance: some calls run immediately and others a person approves first, decided per call from its arguments; requires passport scope "write".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "approval_id": {
      "description": "Set on retry after a human approved a won/lost move",
      "format": "uuid",
      "type": "string"
    },
    "deal_id": {
      "format": "uuid",
      "type": "string"
    },
    "idempotency_key": {
      "description": "Optional. Repeating a call under the same key returns the first result instead of acting twice; different arguments under one key are refused.",
      "maxLength": 255,
      "type": "string"
    },
    "if_version": {
      "type": "integer"
    },
    "lost_reason": {
      "description": "Required when the target stage closes the deal as lost",
      "type": "string"
    },
    "note": {
      "description": "Logged as a note on the deal's timeline after the move",
      "type": "string"
    },
    "to_stage_id": {
      "description": "The target stage, by id — obtain it from list_pipelines, since a deal you have read carries only the stage it is already IN. That stage's semantic decides what happens next: open executes immediately, won or lost is staged for a human's approval.",
      "format": "uuid",
      "type": "string"
    }
  },
  "required": [
    "deal_id",
    "to_stage_id"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "deal": {
          "properties": {
            "fields": {
              "type": "object"
            },
            "id": {
              "format": "uuid",
              "type": "string"
            },
            "record_type": {
              "type": "string"
            },
            "trust_tier": {
              "type": "string"
            },
            "version": {
              "type": "integer"
            }
          },
          "required": [
            "fields",
            "id",
            "record_type"
          ],
          "type": "object"
        },
        "note_activity_id": {
          "format": "uuid",
          "type": "string"
        }
      },
      "required": [
        "deal"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### promote_lead

**Promote a lead to a person**

Turn a lead who has genuinely engaged into a person record, carrying their history across. It requires a trigger naming the engagement that justifies it — a reply, a booked or held meeting, or a human's decision. Cold outreach that nobody answered is not a promotion, and there is no trigger for it. Use qualify_lead when the lead is merely incomplete rather than ready, and disqualify_lead when the engagement says the opposite. A person approves this call before it runs; the promoted person's id comes back only from the retry that carries their approval. (Governance: a person approves every call before it runs; requires passport scope "write".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "approval_id": {
      "description": "Set on retry after a human approved the staged call",
      "format": "uuid",
      "type": "string"
    },
    "evidence_note": {
      "type": "string"
    },
    "idempotency_key": {
      "description": "Optional. Repeating a call under the same key returns the first result instead of acting twice; different arguments under one key are refused.",
      "maxLength": 255,
      "type": "string"
    },
    "lead_id": {
      "format": "uuid",
      "type": "string"
    },
    "trigger": {
      "description": "The genuine engagement justifying promotion; cold outreach with no reply never promotes",
      "enum": [
        "inbound_reply",
        "meeting_booked",
        "meeting_held",
        "human_qualify"
      ],
      "type": "string"
    }
  },
  "required": [
    "lead_id",
    "trigger"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "merged": {
          "type": "boolean"
        },
        "person": {
          "properties": {
            "fields": {
              "type": "object"
            },
            "id": {
              "format": "uuid",
              "type": "string"
            },
            "record_type": {
              "type": "string"
            },
            "trust_tier": {
              "type": "string"
            },
            "version": {
              "type": "integer"
            }
          },
          "required": [
            "fields",
            "id",
            "record_type"
          ],
          "type": "object"
        }
      },
      "required": [
        "merged",
        "person"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### qualify_lead

**Qualify a lead**

Fill in what a lead's own data already implies — today the company name, from the domain of its email address — and report which qualification fields are still empty. It fills only a field that is currently EMPTY and derivable from the lead itself. It never overwrites a value, never invents one, and reaches nothing outside the record, so a lead with nothing to derive from comes back unchanged with its gaps named. Use enrich to learn about a company from its website, and promote_lead once a real engagement means the lead should become a person. The gaps in the result are what a human still has to supply; they are the honest answer to "is this lead ready", not a failure of the call. (Governance: runs immediately; requires passport scope "write".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "idempotency_key": {
      "description": "Optional. Repeating a call under the same key returns the first result instead of acting twice; different arguments under one key are refused.",
      "maxLength": 255,
      "type": "string"
    },
    "record_id": {
      "description": "The lead to qualify",
      "format": "uuid",
      "type": "string"
    }
  },
  "required": [
    "record_id"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "filled": {
          "additionalProperties": {
            "properties": {
              "evidence": {
                "items": {
                  "properties": {
                    "snippet": {
                      "type": "string"
                    },
                    "source": {
                      "type": "string"
                    }
                  },
                  "required": [
                    "snippet",
                    "source"
                  ],
                  "type": "object"
                },
                "type": "array"
              },
              "value": {
                "type": "string"
              }
            },
            "required": [
              "evidence",
              "value"
            ],
            "type": "object"
          },
          "type": "object"
        },
        "gaps": {
          "items": {
            "type": "string"
          },
          "type": "array"
        },
        "record_id": {
          "format": "uuid",
          "type": "string"
        }
      },
      "required": [
        "filled",
        "gaps",
        "record_id"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### query_workspace

**Query the workspace**

Answer a question that has STRUCTURE — a record type, conditions on its fields, a hop to a related record, or a likeness to describe — by sending a plan and reading back the records that satisfy it, together with what kind of answer it is. Every name in a plan comes from the published vocabulary, and a name outside it is refused by name rather than guessed at. The margince://schema/query resource — not this description — is what says which record types, fields, operators and relationships this workspace can actually be asked about. A plan takes at most one similarity clause and at most one relationship hop. It cannot group, count or total, and there is no cursor: an answer that hit its limit says so instead of offering a next page. Use search_records when you only have a name or a phrase and no conditions to apply, and run_report when the answer wanted is a count, a total or a breakdown rather than the records themselves. Read `coverage` before you use the rows: `complete_exact` means every record matching the plan is here, `ranked_semantic` means these ranked highest and others may match, and `partial_degraded` means something in the plan could not be answered as asked — `notes` says which. Keep each row's record_type and id for any follow-up call, and its `evidence` for the related record that admitted it. (Governance: runs immediately; requires passport scope "read".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "plan": {
      "description": "A query plan, in the grammar published at margince://schema/query. That document, not this description, holds the record types, fields, operators and relationships this workspace admits: a name outside it is refused by name, never guessed at.",
      "type": "object"
    }
  },
  "required": [
    "plan"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "coverage": {
          "type": "string"
        },
        "executed_plan": {
          "type": "string"
        },
        "limit": {
          "type": "integer"
        },
        "notes": {
          "items": {
            "properties": {
              "code": {
                "type": "string"
              },
              "detail": {
                "type": "string"
              },
              "path": {
                "type": "string"
              }
            },
            "required": [
              "code",
              "detail"
            ],
            "type": "object"
          },
          "type": "array"
        },
        "rows": {
          "items": {
            "properties": {
              "evidence": {
                "items": {
                  "properties": {
                    "id": {
                      "format": "uuid",
                      "type": "string"
                    },
                    "record_type": {
                      "type": "string"
                    },
                    "relation": {
                      "type": "string"
                    },
                    "title": {
                      "type": "string"
                    },
                    "trust_tier": {
                      "type": "string"
                    }
                  },
                  "required": [
                    "id",
                    "record_type",
                    "relation",
                    "title"
                  ],
                  "type": "object"
                },
                "type": "array"
              },
              "record": {
                "properties": {
                  "fields": {
                    "type": "object"
                  },
                  "id": {
                    "format": "uuid",
                    "type": "string"
                  },
                  "record_type": {
                    "type": "string"
                  },
                  "trust_tier": {
                    "type": "string"
                  },
                  "version": {
                    "type": "integer"
                  }
                },
                "required": [
                  "fields",
                  "id",
                  "record_type"
                ],
                "type": "object"
              },
              "score": {
                "type": "number"
              }
            },
            "required": [
              "evidence",
              "record"
            ],
            "type": "object"
          },
          "type": "array"
        }
      },
      "required": [
        "coverage",
        "executed_plan",
        "limit",
        "notes",
        "rows"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### read_brief

**Read the morning brief**

Read the ranked queue the person you act for sees when they open their morning brief — the deals the workspace decided are worth their attention today, in order, with the rows behind each ranking. It re-reads the last assembled run rather than building a new one, so its as_of says how current it is, and it is that person's own queue: it cannot be asked for anyone else's. Acting on, dismissing or snoozing an item is theirs alone. Use whats_slipping_this_week when the question is which deals are losing momentum regardless of what today's brief chose, and read_record for what one of these deals currently says. Each item names a deal_id and its evidence_ids; read those to cite what the ranking rested on rather than restating the item's own summary. (Governance: runs immediately; requires passport scope "read".)

Renders its result in [`ui://margince/account-brief.html`](#account_brief_view), visible to `model`, `app`.

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {},
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "as_of": {
          "type": "string"
        },
        "brief_id": {
          "format": "uuid",
          "type": "string"
        },
        "candidate_count": {
          "type": "integer"
        },
        "generated_at": {
          "type": "string"
        },
        "items": {
          "items": {
            "properties": {
              "composite": {
                "type": "number"
              },
              "deal_id": {
                "format": "uuid",
                "type": "string"
              },
              "evidence_ids": {
                "items": {
                  "format": "uuid",
                  "type": "string"
                },
                "type": "array"
              },
              "factors": {
                "properties": {
                  "momentum": {
                    "type": "number"
                  },
                  "revenue": {
                    "type": "number"
                  },
                  "timing": {
                    "type": "number"
                  },
                  "warmth": {
                    "type": "number"
                  },
                  "winnability": {
                    "type": "number"
                  }
                },
                "required": [
                  "momentum",
                  "revenue",
                  "timing",
                  "warmth",
                  "winnability"
                ],
                "type": "object"
              },
              "item_id": {
                "format": "uuid",
                "type": "string"
              },
              "rank": {
                "type": "integer"
              },
              "snoozed_until": {
                "type": "string"
              },
              "state": {
                "type": "string"
              },
              "state_at": {
                "type": "string"
              }
            },
            "required": [
              "composite",
              "deal_id",
              "evidence_ids",
              "factors",
              "item_id",
              "rank",
              "state"
            ],
            "type": "object"
          },
          "type": "array"
        }
      },
      "required": [
        "as_of",
        "brief_id",
        "candidate_count",
        "generated_at",
        "items"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### read_record

**Read a record**

Read one record's own stored fields — the values a person would see on its detail page — when you already know which record you mean. It returns that record and nothing around it: no timeline, no related people, no deals on the account. Use catch_me_up_on when the goal is what has been happening on the record rather than what it currently says. Keep the version from the result and pass it back as if_version on a later update, so a write is refused rather than silently overwriting a change made in between. (Governance: runs immediately; requires passport scope "read".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "id": {
      "format": "uuid",
      "type": "string"
    },
    "record_type": {
      "enum": [
        "person",
        "organization",
        "deal",
        "lead",
        "activity",
        "project"
      ],
      "type": "string"
    }
  },
  "required": [
    "record_type",
    "id"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "fields": {
          "type": "object"
        },
        "id": {
          "format": "uuid",
          "type": "string"
        },
        "record_type": {
          "type": "string"
        },
        "trust_tier": {
          "type": "string"
        },
        "version": {
          "type": "integer"
        }
      },
      "required": [
        "fields",
        "id",
        "record_type"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### relink_activity

**Re-associate an activity to a record**

Fix what an already-recorded activity is about, when a captured mail or meeting landed on the wrong record or on none. It changes only the association. The activity's own content — subject, body, when it happened — is untouched, and by default the new link is ADDED alongside any existing one rather than replacing it. Use log_activity when the event is not recorded at all yet; this tool moves an existing one. Set replace_existing_of_type when you mean to move the activity rather than associate it with one more record. (Governance: runs immediately; requires passport scope "write".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "activity_id": {
      "description": "The captured activity to re-associate",
      "format": "uuid",
      "type": "string"
    },
    "entity_id": {
      "description": "The record to link it to",
      "format": "uuid",
      "type": "string"
    },
    "entity_type": {
      "enum": [
        "person",
        "organization",
        "deal",
        "lead",
        "project"
      ],
      "type": "string"
    },
    "idempotency_key": {
      "description": "Optional. Repeating a call under the same key returns the first result instead of acting twice; different arguments under one key are refused.",
      "maxLength": 255,
      "type": "string"
    },
    "replace_existing_of_type": {
      "default": false,
      "description": "Replace the existing link of the same entity_type (move) rather than adding one (associate)",
      "type": "boolean"
    }
  },
  "required": [
    "activity_id",
    "entity_type",
    "entity_id"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "id": {
          "format": "uuid",
          "type": "string"
        }
      },
      "required": [
        "id"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### resolve_entities

**Resolve people and companies**

Find out whether the people and companies named in something you are holding already exist here, matched on addresses, phone numbers and company domains rather than on text. It reads only. Nothing is created, changed or merged, and it answers person and organization, never leads. A near match comes back `ambiguous` however close it is. Use search_records to find a record you know exists, and merge_records once a person has decided that two records are one. Call this BEFORE creating a person or company from anything you did not type. Act on `matched`; on `ambiguous` ask which is meant; on `unresolved` say what you are about to create, because a miss is not proof that nothing exists. (Governance: runs immediately; requires passport scope "read".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "candidates": {
      "items": {
        "additionalProperties": false,
        "properties": {
          "domains": {
            "description": "Company domains claimed by the payload. Read for an organization only.",
            "items": {
              "type": "string"
            },
            "maxItems": 10,
            "type": "array"
          },
          "emails": {
            "description": "Every address on the payload, not just the primary one. For an organization each address also contributes its domain, unless it is a consumer mail domain.",
            "items": {
              "type": "string"
            },
            "maxItems": 10,
            "type": "array"
          },
          "kind": {
            "description": "Which record type this payload is asking about. Leads are not resolved.",
            "enum": [
              "person",
              "organization"
            ],
            "type": "string"
          },
          "legal_name": {
            "description": "The registered company name, when it differs from the trading name. Read for an organization only.",
            "type": "string"
          },
          "name": {
            "description": "Full name for a person, trading name for a company.",
            "type": "string"
          },
          "phones": {
            "description": "Phone numbers in E.164 form; one that does not normalize is not a key and is ignored.",
            "items": {
              "type": "string"
            },
            "maxItems": 10,
            "type": "array"
          },
          "ref": {
            "description": "Your own label for this candidate, echoed back on its answer so a batch can be lined up. Any string; it is never stored.",
            "type": "string"
          }
        },
        "required": [
          "kind"
        ],
        "type": "object"
      },
      "maxItems": 20,
      "minItems": 1,
      "type": "array"
    }
  },
  "required": [
    "candidates"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "candidates": {
          "items": {
            "properties": {
              "decision": {
                "type": "string"
              },
              "matches": {
                "items": {
                  "properties": {
                    "confidence": {
                      "type": "number"
                    },
                    "matched_on": {
                      "type": "string"
                    },
                    "record": {
                      "properties": {
                        "fields": {
                          "type": "object"
                        },
                        "id": {
                          "format": "uuid",
                          "type": "string"
                        },
                        "record_type": {
                          "type": "string"
                        },
                        "trust_tier": {
                          "type": "string"
                        },
                        "version": {
                          "type": "integer"
                        }
                      },
                      "required": [
                        "fields",
                        "id",
                        "record_type"
                      ],
                      "type": "object"
                    }
                  },
                  "required": [
                    "confidence",
                    "matched_on",
                    "record"
                  ],
                  "type": "object"
                },
                "type": "array"
              },
              "ref": {
                "type": "string"
              }
            },
            "required": [
              "decision",
              "matches"
            ],
            "type": "object"
          },
          "type": "array"
        }
      },
      "required": [
        "candidates"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### review_commitments

**Review open commitments**

Answer "what have we promised and not delivered?": the open tasks across the workspace, earliest due date first and undated ones last, each with the person who owes it, when it came due and the record it was made about. A promise appears here only because someone recorded it as a task — what was agreed in a meeting and never written down is absent — so this is what the workspace has on record, not everything owed. It is scoped to the records the caller may see. Use whats_slipping_this_week when the question is which DEALS are at risk rather than which promises are outstanding, and catch_me_up_on for everything that has happened on one record. Each item carries task_id and, where there is one, assignee_id. Every state is judged against as_of, so carry that too if you report the answer later. (Governance: runs immediately; requires passport scope "read".)

Renders its result in [`ui://margince/commitments.html`](#commitments_view), visible to `model`, `app`.

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "assignee_id": {
      "description": "Narrow to one owner's promises; omit for everyone's",
      "format": "uuid",
      "type": "string"
    },
    "limit": {
      "description": "Cap the set; omit for 50, the server-side ceiling",
      "maximum": 50,
      "minimum": 1,
      "type": "integer"
    }
  },
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "as_of": {
          "type": "string"
        },
        "commitments": {
          "items": {
            "properties": {
              "about": {
                "items": {
                  "properties": {
                    "entity_id": {
                      "format": "uuid",
                      "type": "string"
                    },
                    "entity_type": {
                      "type": "string"
                    },
                    "name": {
                      "type": "string"
                    }
                  },
                  "required": [
                    "entity_id",
                    "entity_type"
                  ],
                  "type": "object"
                },
                "type": "array"
              },
              "assignee_id": {
                "format": "uuid",
                "type": "string"
              },
              "assignee_name": {
                "type": "string"
              },
              "days_overdue": {
                "type": "integer"
              },
              "due_at": {
                "type": "string"
              },
              "state": {
                "type": "string"
              },
              "subject": {
                "type": "string"
              },
              "task_id": {
                "format": "uuid",
                "type": "string"
              }
            },
            "required": [
              "about",
              "state",
              "subject",
              "task_id"
            ],
            "type": "object"
          },
          "type": "array"
        }
      },
      "required": [
        "as_of",
        "commitments"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### run_report

**Run a report**

Answer a question about totals, counts or breakdowns — pipeline by stage, deals won by owner, activity volume over time — by running one of this workspace's prebuilt reports. Only the named reports exist, and each accepts only its own filter, grouping and measure names; anything outside its lists is refused rather than approximated. It aggregates, so it answers how many and how much, never which record. Use search_records or whats_slipping_this_week when the answer wanted is the records themselves rather than a number over them. Call a report with no plan arguments first to see what it answers by default, then narrow using the names its own catalog entry lists. (Governance: runs immediately; requires passport scope "read".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "aggregates": {
      "description": "Omit for the report's own default aggregates.",
      "items": {
        "additionalProperties": false,
        "properties": {
          "as": {
            "description": "Output column name for this aggregate",
            "type": "string"
          },
          "field": {
            "description": "A measure name from this report's list. Omit only with fn=count.",
            "type": "string"
          },
          "fn": {
            "enum": [
              "count",
              "sum",
              "avg",
              "min",
              "max"
            ],
            "type": "string"
          }
        },
        "required": [
          "fn"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "filters": {
      "description": "Equality predicates keyed by this report's filter names — {\"owner_id\":\"\u003cuuid\u003e\"}. A key outside the report's list is refused.",
      "type": "object"
    },
    "group_by": {
      "description": "Dimension names from this report's list. Omit for the report's own default grouping.",
      "items": {
        "type": "string"
      },
      "type": "array"
    },
    "report": {
      "description": "The prebuilt report to run. Send `report` alone to get its defaults; the three plan arguments accept ONLY the names listed for that report. activities-by-kind — group_by: direction, kind; filters: direction, kind; aggregate fields: (none); default: count as activities grouped by kind. deals-by-stage — group_by: pipeline_id, stage_id, status; filters: owner_id, pipeline_id, status; aggregate fields: amount_minor; default: count as deals, sum(amount_minor) as amount_minor_sum grouped by stage_id. forecast — group_by: currency, forecast_category, owner_id, pipeline_id, stage_id, win_probability; filters: currency, forecast_category, owner_id, pipeline_id, stage_id; aggregate fields: amount_minor, weighted_amount_minor; default: count as deals, sum(amount_minor) as unweighted_minor, sum(weighted_amount_minor) as weighted_minor grouped by forecast_category. open-deals-per-company — group_by: organization_id, owner_id; filters: owner_id, pipeline_id; aggregate fields: amount_minor; default: count as open_deals grouped by organization_id. A `pipeline_id` or `stage_id` used here comes from list_pipelines — no other tool on this surface yields one.",
      "enum": [
        "activities-by-kind",
        "deals-by-stage",
        "forecast",
        "open-deals-per-company"
      ],
      "type": "string"
    }
  },
  "required": [
    "report"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "columns": {
          "items": {
            "type": "string"
          },
          "type": "array"
        },
        "derivation_url": {
          "type": "string"
        },
        "generated_at": {
          "type": "string"
        },
        "plan": {
          "type": "object"
        },
        "report": {
          "type": "string"
        },
        "rows": {
          "items": {
            "type": "object"
          },
          "type": "array"
        },
        "total_rows": {
          "type": "integer"
        }
      },
      "required": [
        "columns",
        "plan",
        "report",
        "rows"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### search_context

**Search for relevant material**

Find the records most relevant to a description, ranked by meaning as well as by wording, each with the excerpt that ranked it. Ranked, never exhaustive: records that also match may be absent, and no count of them exists. You can narrow it to particular record types, but not by field, date or owner, and it does not group or total. Use query_workspace when the question has conditions, a date bound or a related record to reach through, and search_records when you have the exact name or phrase. Read `coverage`: `partial_degraded` means `notes` matters, and `semantic_ranking_degraded_to_lexical` there means the ranking fell back to word overlap. Keep each hit's record_type and id. (Governance: runs immediately; requires passport scope "read".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "limit": {
      "maximum": 25,
      "minimum": 1,
      "type": "integer"
    },
    "query": {
      "description": "What to look for, in your own words. The wording is matched by meaning as well as by the words themselves, so a phrase that appears nowhere on a record can still rank it.",
      "maxLength": 1000,
      "type": "string"
    },
    "record_types": {
      "description": "Restrict the sweep to these types; omit to sweep all of them.",
      "items": {
        "enum": [
          "person",
          "organization",
          "deal",
          "lead",
          "project"
        ],
        "type": "string"
      },
      "type": "array"
    }
  },
  "required": [
    "query"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "coverage": {
          "type": "string"
        },
        "hits": {
          "items": {
            "properties": {
              "excerpts": {
                "items": {
                  "properties": {
                    "snippet": {
                      "type": "string"
                    },
                    "source": {
                      "type": "string"
                    }
                  },
                  "required": [
                    "snippet",
                    "source"
                  ],
                  "type": "object"
                },
                "type": "array"
              },
              "record": {
                "properties": {
                  "fields": {
                    "type": "object"
                  },
                  "id": {
                    "format": "uuid",
                    "type": "string"
                  },
                  "record_type": {
                    "type": "string"
                  },
                  "trust_tier": {
                    "type": "string"
                  },
                  "version": {
                    "type": "integer"
                  }
                },
                "required": [
                  "fields",
                  "id",
                  "record_type"
                ],
                "type": "object"
              },
              "score": {
                "type": "number"
              }
            },
            "required": [
              "excerpts",
              "record",
              "score"
            ],
            "type": "object"
          },
          "type": "array"
        },
        "notes": {
          "items": {
            "properties": {
              "code": {
                "type": "string"
              },
              "detail": {
                "type": "string"
              },
              "path": {
                "type": "string"
              }
            },
            "required": [
              "code",
              "detail"
            ],
            "type": "object"
          },
          "type": "array"
        }
      },
      "required": [
        "coverage",
        "hits",
        "notes"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### search_records

**Search records**

Find people, organizations, deals, leads and projects when you know roughly what they are called but not which record they are. It matches text stored ON the record. It does not read a timeline: message bodies, call notes and meeting content are not searched, so a query describing what someone said or did will not find them. Use list_records when the question is which records meet a condition rather than what one is called, read_record when you already hold the record's id, and run_report when the question is a count, a total or a breakdown rather than a set of records. Keep each result's record_type and id together: every other tool identifies a record by both, and an id alone does not say which type it belongs to. (Governance: runs immediately; requires passport scope "read".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "cursor": {
      "description": "Keyset cursor (single record_type only)",
      "type": "string"
    },
    "limit": {
      "maximum": 50,
      "minimum": 1,
      "type": "integer"
    },
    "q": {
      "description": "What to match against the text stored on the record. It does not reach a timeline: message bodies, call notes and meeting content are not searched.",
      "type": "string"
    },
    "record_type": {
      "description": "Restrict to one type; omit to sweep all five",
      "enum": [
        "person",
        "organization",
        "deal",
        "lead",
        "project"
      ],
      "type": "string"
    }
  },
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "next_cursor": {
          "type": "string"
        },
        "records": {
          "items": {
            "properties": {
              "fields": {
                "type": "object"
              },
              "id": {
                "format": "uuid",
                "type": "string"
              },
              "record_type": {
                "type": "string"
              },
              "trust_tier": {
                "type": "string"
              },
              "version": {
                "type": "integer"
              }
            },
            "required": [
              "fields",
              "id",
              "record_type"
            ],
            "type": "object"
          },
          "type": "array"
        }
      },
      "required": [
        "records"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### send_email

**Send an email**

Put a mail on the wire to a real recipient, from this workspace, and record it on the thread it belongs to. It sends EXACTLY the subject and body it is given and composes nothing, so it is not the tool to reach for when the message does not exist yet. Every recipient must have granted the consent purpose the call names, and a person approves the send before it leaves — a message leaving the workspace cannot be recalled. Use draft_email first to produce the message and let it be read, and send_message when the conversation is on a chat channel rather than mail. Send the same activity_id, subject and body the draft produced, and keep the staged approval id: the approval is bound to that exact message, so changed text needs a new approval. (Governance: a person approves every call before it runs; requires passport scope "send".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "activity_id": {
      "format": "uuid",
      "type": "string"
    },
    "approval_id": {
      "description": "Set on retry after a human approved the staged call",
      "format": "uuid",
      "type": "string"
    },
    "body": {
      "type": "string"
    },
    "cc": {
      "items": {
        "format": "email",
        "type": "string"
      },
      "type": "array"
    },
    "consent_purpose": {
      "description": "Purpose key the recipients must have granted",
      "type": "string"
    },
    "idempotency_key": {
      "description": "Optional. Repeating a call under the same key returns the first result instead of acting twice; different arguments under one key are refused.",
      "maxLength": 255,
      "type": "string"
    },
    "subject": {
      "type": "string"
    },
    "to": {
      "items": {
        "format": "email",
        "type": "string"
      },
      "minItems": 1,
      "type": "array"
    }
  },
  "required": [
    "activity_id",
    "to",
    "subject",
    "body",
    "consent_purpose"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "activity_id": {
          "format": "uuid",
          "type": "string"
        },
        "status": {
          "type": "string"
        }
      },
      "required": [
        "activity_id",
        "status"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### send_message

**Reply on a channel conversation**

Reply on a captured chat conversation — the channels this workspace has connected — on the thread it was captured from. It replies to an existing conversation named by activity_id; it cannot start one, and it cannot choose a channel. The recipient must have granted the consent purpose the call names, and a person approves it before it leaves. Use send_email when the thread is a mail thread, and log_activity when the point is to record that something was said rather than to say it. Keep the activity_id of the conversation and the staged approval id; the approval binds the exact text, so changed text needs a new approval. (Governance: a person approves every call before it runs; requires passport scope "send".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "activity_id": {
      "description": "The captured conversation being replied to",
      "format": "uuid",
      "type": "string"
    },
    "approval_id": {
      "description": "Set on retry after a human approved the staged call",
      "format": "uuid",
      "type": "string"
    },
    "body": {
      "minLength": 1,
      "type": "string"
    },
    "consent_purpose": {
      "description": "Purpose key the recipient must have granted",
      "type": "string"
    },
    "idempotency_key": {
      "description": "Optional. Repeating a call under the same key returns the first result instead of acting twice; different arguments under one key are refused.",
      "maxLength": 255,
      "type": "string"
    }
  },
  "required": [
    "activity_id",
    "body",
    "consent_purpose"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "activity_id": {
          "format": "uuid",
          "type": "string"
        },
        "status": {
          "type": "string"
        }
      },
      "required": [
        "activity_id",
        "status"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### update_record

**Update a record**

Change stored field values on a record that already exists — a corrected title, an amount, an expected close date. Only the fields you send change, and only the fields the record type stores: a person's email addresses, for one, are not among them. A field whose current value a HUMAN last set is not overwritten — that part of the call is staged for a person to decide and named in the result, so treat a staged answer as the write not having happened yet. It names the record by id: when a name matches two records, which of them was meant is a question for a person and not a choice to make quietly. There is no sharing verb here, but owner_id is NOT a neutral field — who owns a record is what decides who can see it, so reassigning it moves the record onto someone else's book and can take it off the current owner's. Use advance_deal or progress_deal to move a deal between stages, and relink_activity to change what an activity is about; neither is a field edit. Send if_version with the version you read, and keep the staged approval id from the result if you intend to retry the same change once a human has released it. (Governance: runs immediately; requires passport scope "write".)

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "approval_id": {
      "description": "Set on retry after a human approved overwriting their edit; send it with exactly the staged replay arguments",
      "format": "uuid",
      "type": "string"
    },
    "fields": {
      "description": "Only sent fields change. Fields a human last edited are not applied: they are staged for approval and named in the result's staged_approval. The crm.yaml body for the record_type. The fields each record_type takes, which of them are REQUIRED, and their shapes are published at margince://schema/record-fields — that document, not this description, is what says what a write may name. An extra key must be cf_\u003cslug\u003e for a custom field; any other key is refused BY NAME and never dropped in silence, so a wrong guess is answered with the vocabulary rather than lost.",
      "type": "object"
    },
    "id": {
      "format": "uuid",
      "type": "string"
    },
    "idempotency_key": {
      "description": "Optional. Repeating a call under the same key returns the first result instead of acting twice; different arguments under one key are refused.",
      "maxLength": 255,
      "type": "string"
    },
    "if_version": {
      "description": "Optimistic-concurrency guard: the last-seen record version",
      "type": "integer"
    },
    "record_type": {
      "enum": [
        "person",
        "organization",
        "deal",
        "lead",
        "activity",
        "project",
        "relationship"
      ],
      "type": "string"
    }
  },
  "required": [
    "record_type",
    "id",
    "fields"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "fields": {
          "type": "object"
        },
        "id": {
          "format": "uuid",
          "type": "string"
        },
        "record_type": {
          "type": "string"
        },
        "staged_approval": {
          "properties": {
            "approval_id": {
              "format": "uuid",
              "type": "string"
            },
            "fields": {
              "items": {
                "type": "string"
              },
              "type": "array"
            },
            "message": {
              "type": "string"
            },
            "replay": {
              "type": "object"
            }
          },
          "required": [
            "approval_id",
            "fields",
            "message",
            "replay"
          ],
          "type": "object"
        },
        "trust_tier": {
          "type": "string"
        },
        "version": {
          "type": "integer"
        }
      },
      "required": [
        "fields",
        "id",
        "record_type"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### whats_slipping_this_week

**What's slipping this week**

Answer "what is slipping?": the deals going quiet or running past their expected close date, ranked worst first, each with the evidence that says so. It reports only deals whose risk can be evidenced from their own fields — a deal nobody can point at a reason for is absent rather than guessed — and it is scoped to the deals the caller may see. Use run_report for the pipeline as a whole (totals, counts, breakdowns), and at_risk_relationships when the question is who a deal rests on rather than whether it is moving. Keep each deal_id if you intend to act; draft_follow_ups_for works over this same ranked set without you re-deriving it. (Governance: runs immediately; requires passport scope "read".)

Renders its result in [`ui://margince/pipeline-review.html`](#pipeline_review_view), visible to `model`, `app`.

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "limit": {
      "description": "Cap the ranked set; omit for the full evidenced set",
      "maximum": 50,
      "minimum": 1,
      "type": "integer"
    }
  },
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "deals": {
          "items": {
            "properties": {
              "amount_minor": {
                "type": "integer"
              },
              "currency": {
                "type": "string"
              },
              "deal_id": {
                "format": "uuid",
                "type": "string"
              },
              "evidence": {
                "items": {
                  "properties": {
                    "snippet": {
                      "type": "string"
                    },
                    "source": {
                      "type": "string"
                    }
                  },
                  "required": [
                    "snippet",
                    "source"
                  ],
                  "type": "object"
                },
                "type": "array"
              },
              "name": {
                "type": "string"
              },
              "rank": {
                "type": "integer"
              }
            },
            "required": [
              "deal_id",
              "evidence",
              "name",
              "rank"
            ],
            "type": "object"
          },
          "type": "array"
        }
      },
      "required": [
        "deals"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>

### who_knows

**Who knows this contact**

Answer "who here knows this person?": the colleagues with a relationship to one contact, warmest first, with the interaction counts that ground the warmth. It reports relationships this workspace can evidence from its own recorded interactions, so a genuine relationship nobody has logged does not appear. Never spoken is reported as no relationship rather than a score of zero. Use intro_path_to when you want a route into a COMPANY rather than the people who know one contact. Each colleague comes back with a user_id; the strength bucket, not the raw score, is what a person should be asked about. (Governance: runs immediately; requires passport scope "read".)

Renders its result in [`ui://margince/relationship-map.html`](#relationship_map_view), visible to `model`, `app`.

<details><summary>Input schema</summary>

```json
{
  "additionalProperties": false,
  "properties": {
    "person_id": {
      "description": "The contact to ask about",
      "format": "uuid",
      "type": "string"
    }
  },
  "required": [
    "person_id"
  ],
  "type": "object"
}
```

</details>

<details><summary>Output schema</summary>

```json
{
  "properties": {
    "data": {
      "properties": {
        "colleagues": {
          "items": {
            "properties": {
              "display_name": {
                "type": "string"
              },
              "interactions_90d": {
                "type": "integer"
              },
              "strength": {
                "type": "integer"
              },
              "strength_bucket": {
                "type": "string"
              },
              "user_id": {
                "format": "uuid",
                "type": "string"
              }
            },
            "required": [
              "display_name",
              "interactions_90d",
              "strength_bucket",
              "user_id"
            ],
            "type": "object"
          },
          "type": "array"
        },
        "person_id": {
          "format": "uuid",
          "type": "string"
        }
      },
      "required": [
        "colleagues",
        "person_id"
      ],
      "type": "object"
    },
    "evidence": {
      "items": {
        "properties": {
          "captured_by": {
            "type": "string"
          },
          "record_id": {
            "format": "uuid",
            "type": "string"
          },
          "record_type": {
            "type": "string"
          },
          "source": {
            "type": "string"
          }
        },
        "required": [
          "record_id",
          "record_type"
        ],
        "type": "object"
      },
      "type": "array"
    },
    "freshness": {
      "properties": {
        "authoritative": {
          "type": "boolean"
        },
        "last_synced_at": {
          "type": "string"
        }
      },
      "required": [
        "authoritative"
      ],
      "type": "object"
    },
    "schema_version": {
      "type": "string"
    },
    "trace_id": {
      "type": "string"
    },
    "trust": {
      "type": "string"
    },
    "warnings": {
      "items": {
        "properties": {
          "code": {
            "type": "string"
          },
          "message": {
            "type": "string"
          }
        },
        "required": [
          "code",
          "message"
        ],
        "type": "object"
      },
      "type": "array"
    }
  },
  "required": [
    "data",
    "evidence",
    "freshness",
    "schema_version",
    "trace_id",
    "trust",
    "warnings"
  ],
  "type": "object"
}
```

</details>


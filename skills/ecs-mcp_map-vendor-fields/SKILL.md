---
name: map-vendor-fields
description: "Map vendor fields (non-ECS fields such as CrowdStrike FDR, Okta, Palo Alto, custom JSON keys) to their closest Elastic Common Schema (ECS) fields using the ecs-mcp server. Use when the user provides a list of vendor/custom field names (or a file, sample event, or schema) and asks for an ECS mapping, alignment review, or recommendations for `external: ecs` reuse."
---

# Map vendor fields to ECS

Map arbitrary vendor or custom field names to their best-fit Elastic Common Schema (ECS) counterparts using the `ecs-mcp` MCP server. The output is a structured mapping table that distinguishes exact ECS matches, strong semantic matches, weak matches, and fields with no ECS equivalent.

## Prerequisites

The `ecs-mcp` server must be connected. Confirm by calling `ecs_get_sql_tables` once at the start — if the tool is unavailable, stop and tell the user to start the server (`./ecs-mcp -dir <path-to-ecs-checkout>` in this repo) and configure it in their MCP client before retrying.

## Inputs

Accept any of the following from the user:

- An inline list of field names (dotted paths, camelCase, snake_case, or mixed).
- A path to a file containing field names (one per line, JSON, YAML, or a sample event).
- A vendor product name plus a pointer to where its schema lives (e.g. a docs URL or local file).

If only a product name is given with no field list, ask the user for a concrete field list or schema file before proceeding — do not fabricate vendor field names from memory.

## Selecting an ECS version

The server hosts multiple ECS releases. `ecs_match_fields`, `ecs_search_fields`, and every `ecs_execute_sql_query` against `fields`/`fieldsets` require a `version` argument (e.g. `"9.3.0"`).

Before step 1 of the workflow, settle on exactly one version:

1. If the user specifies an ECS version, use it.
2. If the vendor input comes from an Elastic integration package that pins an ECS version (e.g. a manifest referencing `git@v9.3.0`), use that version.
3. Otherwise, list the available versions and ask the user which to target:
   ```sql
   SELECT DISTINCT version FROM fields ORDER BY version;
   ```

Reuse the same version for every tool call in a single mapping. Do not mix results from different ECS releases in one output table.

## Workflow

1. **Normalize the input.** Extract a flat list of field names. For sample events, use the JSON/YAML key paths (e.g. `process.parent.command_line`). Preserve the original casing — the server handles camelCase splitting.

2. **Exact match pass.** Call `ecs_match_fields` with the chosen `version` and the full list (batches of ≤500). Record which names are already valid ECS fields in that version along with their ECS `type` and `description`. These are the "exact" matches — recommend `external: ecs` for these in the output.

3. **Semantic search pass.** For every field that did *not* exact-match, call `ecs_search_fields` with the same `version` and the raw field name as the query. The server tokenizes dotted paths and camelCase automatically, so `crowdstrike.fdr.ProcessTTYAttached` finds `process.tty`-family fields without manual preprocessing. Pull the top candidates (default limit is fine; raise to ~50 only if nothing plausible appears).

   - If the first search returns nothing useful, try again with a keyword reformulation derived from the field's semantics (e.g. `srcIp` → `source ip address`, `usrName` → `user name`). Do not invent keywords that aren't implied by the field name or its known vendor meaning.

4. **Score each candidate.** For each non-exact field, pick the single best ECS target (or none) and classify:
   - **strong** — semantics and data type clearly align (e.g. vendor `SrcIpAddr` → ECS `source.ip`).
   - **weak** — plausible but ambiguous; requires human confirmation (e.g. vendor `user` → ECS `user.name` vs `user.id`).
   - **none** — no ECS field fits; the vendor field should remain vendor-namespaced.

5. **Inspect ECS details when needed.** Use `ecs_execute_sql_query` against the schema from step 1 for deeper questions: allowed values, fieldset membership, level (`core` vs `extended`), or cross-referencing multiple fields. Always filter by the chosen `version` — otherwise rows from every loaded ECS release will be merged. Prefer targeted `SELECT` queries over full-table scans. Example:
   ```sql
   SELECT name, type, level, description
   FROM fields
   WHERE version = '9.3.0'
     AND name IN ('source.ip', 'source.address', 'client.ip');
   ```

6. **Produce the mapping table.** Output a single Markdown table with these columns, in this order:

   | Vendor field | ECS field | Match | ECS type | Rationale |
   |---|---|---|---|---|

   - `Match` is one of `exact`, `strong`, `weak`, `none`.
   - `ECS field` is blank when `Match` is `none`.
   - `Rationale` is one short sentence — cite the specific semantic link (e.g. "same concept, both IPv4 source addresses") or, for `weak`, the ambiguity to resolve.

   State the ECS version used immediately above or below the table so readers can reproduce the mapping.

7. **Summarize next steps.** After the table, add a short section:
   - Count of each match class.
   - For `exact` matches: recommend switching those package/integration fields to `external: ecs`.
   - For `weak` matches: list the specific questions a human needs to answer.
   - For `none`: suggest a vendor-namespaced field path (e.g. `crowdstrike.fdr.<field>`) if the user is authoring an Elastic integration.

## Rules

- Never guess an ECS field that wasn't returned by `ecs_match_fields` or `ecs_search_fields`. If the tools don't surface a candidate, the answer is `none`.
- Always pass `version` to `ecs_match_fields` and `ecs_search_fields`, and always filter by `version` in `ecs_execute_sql_query`. Do not omit or guess — ask the user if unclear.
- Do not mix results from multiple ECS versions in a single mapping table. Pick one version, state it in the output, and stick with it.
- Don't silently drop fields. Every input field appears in the output table exactly once.
- Don't invent ECS field names from training data — ECS evolves, and the server reflects the live schema versions loaded into the database. Trust the tool output over memory.
- Keep the rationale column tight (≤ ~15 words per row). Save deeper discussion for the summary section.

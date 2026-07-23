---
name: upgrade-ecs-version
description: "Assist with upgrading the ECS (Elastic Common Schema) version used by an Elastic integration package. Use when the user wants to move a package from one ECS release to another and needs to know what will break: field removals, type changes, allowed-value changes, `is_array` changes, fieldset reshuffles, and new fields worth adopting. Drives the `ecs-mcp` server to compare the two versions against the specific set of ECS fields the package consumes."
---

# Upgrade ECS version for an integration package

Compare the ECS schema at the package's current pinned version against a target version and produce a structured list of changes that affect the package, using the `ecs-mcp` MCP server as the source of truth. The output focuses on the fields the package actually consumes (its `external: ecs` entries) and flags breaking changes, behavioral changes, and new fields that may be worth adopting.

## Prerequisites

The `ecs-mcp` server must be connected. Confirm by calling `ecs_get_sql_tables` once at the start — if the tool is unavailable, stop and tell the user to start the server (`./ecs-mcp -dir <path-to-ecs-checkout>` from this repo) and configure it in their MCP client before retrying.

## Inputs

Accept any of the following:

- A path to the integration package directory (e.g. `packages/cisco_asa/`).
- A package name plus a path to the `elastic/integrations` checkout root.
- An explicit source ECS version plus an explicit list of fields to evaluate (skip package discovery).

If the user gives only a package name with no path, ask for the repo location before proceeding.

## Determining versions

### Source version (what the package currently pins)

1. Read `<package>/_dev/build/build.yml` and parse `dependencies.ecs.reference`. It has the form `git@vX.Y.Z` — strip the `git@v` prefix to get the semver (e.g. `git@v8.17.0` → `8.17.0`).
2. If `_dev/build/build.yml` is missing or has no `dependencies.ecs.reference`, ask the user which ECS version the package was authored against. Do not assume.
3. Confirm the version is loaded in the database:
   ```sql
   SELECT DISTINCT version FROM fields ORDER BY version;
   ```
   If the pinned version is absent, tell the user and ask which loaded version is closest — do not silently substitute.

### Target version

1. If the user specifies a target version, use it.
2. Otherwise, show the list of loaded versions (same query as above) and ask which to target. Do not default to "latest" silently — users sometimes upgrade to a specific release, not the tip.

Restate both versions back to the user before proceeding (e.g. "Comparing package `cisco_asa` from ECS `8.17.0` → `9.3.0`"). Both versions must exist in the loaded database; if either is missing, stop.

## Determining the field set

The package's ECS surface is the union of `external: ecs` entries in every `data_stream/<stream>/fields/*.yml` file (typically `ecs.yml`, but any file in that directory can contain them). Collect them:

1. Glob `<package>/data_stream/*/fields/*.yml`.
2. Parse each file. Each entry is a YAML list item; collect `name` where `external: ecs`. A nested entry with `fields:` may itself be a group — walk children and emit their full dotted paths (`parent.child`).
3. De-duplicate into a single flat list of dotted field names. This is the **package field set**.

If the package has no `external: ecs` entries, the user should confirm whether a schema comparison is even needed — the skill's value comes from diffing fields the package actually uses. If they still want a full-schema diff, warn that the output will be large and proceed only after confirmation.

## Workflow

1. **Confirm field presence in both versions.** For each version, call `ecs_match_fields` with the package field set. Record:
   - Fields present in source but **missing in target** → *removed* (BREAKING if the package uses them).
   - Fields present in target but **missing in source** → shouldn't normally happen from the package field set, but flag as a sanity check (the package may already reference something that didn't exist at its pinned version).
   - Fields present in both → candidates for deeper comparison in the next step.

2. **Pull full definitions for both versions in a single SQL call each.** Use `ecs_execute_sql_query` to fetch the authoritative rows for the package field set. Example for one version (repeat for the other):
   ```sql
   SELECT name, type, level, is_array, short, description
   FROM fields
   WHERE version = '8.17.0'
     AND name IN ('source.ip', 'user.name', ...);
   ```
   Do **not** omit `version` — without it rows from every loaded release merge silently.

3. **Pull allowed values for both versions.** Join `field_allowed_values`:
   ```sql
   SELECT f.name AS field, fav.name AS value, fav.description
   FROM fields f
   JOIN field_allowed_values fav ON fav.field_id = f.id
   WHERE f.version = '8.17.0'
     AND f.name IN ('event.category', 'event.type', 'event.kind', ...);
   ```
   Run once per version. Many fields have no allowed-value list; that's fine.

4. **Compute the diff.** Pair rows by `name` and compare. Classify each change:

   | Class | Meaning |
   |---|---|
   | **breaking** | Field removed; `type` changed; `is_array` changed; an allowed value the package may emit was removed or renamed. |
   | **behavioral** | `level` changed (`core` ↔ `extended`); field reclassified between fieldsets (may affect docs/tooling but not ingest). |
   | **informational** | `short`/`description` updated; new allowed values added to an enum; field still present, unchanged otherwise. |
   | **opportunity** | Field is new in the target version and semantically related to something the package already emits (surface this only when obvious from the field name — do not invent suggestions). |

   When scoring allowed-value changes, treat additions as *informational* (the package can ignore them) and removals/renames as *breaking* (the package may currently emit them).

5. **Surface unrelated but notable changes.** Also run these two checks and fold results into the report when they affect the package's data streams:
   - **Event category/type pairs.** If the package's `event.category` / `event.type` values appear in the source version's `expected_event_types` but not the target's, flag as breaking. Query:
     ```sql
     SELECT category, type FROM expected_event_types
     WHERE version = '<version>' ORDER BY category, type;
     ```
   - **Fieldset membership.** Only surface when a field moved between fieldsets in a way that changes what `external: ecs` imports. Use the `field_fieldsets` join; skip otherwise.

6. **Produce the report.** Output three Markdown sections in this order. Omit a section if empty rather than printing "none." When listing the number of ECS fields, make sure it matches the count from "Determine the field set"

   ### Breaking changes
   | Field | Change | Source | Target | Action |
   |---|---|---|---|---|
   - `Change` is one of `removed`, `type changed`, `is_array changed`, `allowed value removed`.
   - `Action` is a short concrete next step (e.g. "replace with `destination.address`", "re-index as `keyword`", "drop emission of `authentication_success`").

   ### Behavioral & informational changes
   | Field | Change | Note |
   |---|---|---|
   - Keep this short — one row per field that moved levels, got new allowed values, or had a material description change. Skip pure copy-edits.

   ### New fields worth adopting
   | Target field | Type | Why it may apply |
   |---|---|---|
   - Only list fields that are new in the target version AND are plausibly relevant to the package's existing surface. If nothing qualifies, omit the section entirely — do not pad.

   State the source and target versions once above the first table.

7. **Summarize next steps.** Close with a short checklist:
   - Update `_dev/build/build.yml` → `dependencies.ecs.reference: "git@v<target>"`.
   - For each breaking change: the specific edit required (field rename in `data_stream/*/fields/*.yml`, pipeline change, docs update).
   - Tests/fixtures that may need to be regenerated (`elastic-package test pipeline -g`, if applicable).
   - Whether a changelog entry is warranted (yes for any breaking change; usually yes for a version bump itself).

## Rules

- Every call to `ecs_match_fields` and every query against `fields` / `fieldsets` / `field_allowed_values` / `expected_event_types` must pass or filter by the correct `version`. Never merge rows across versions.
- Never invent ECS fields or allowed values. If a field or value isn't returned by the server for a given version, treat it as absent in that version — even if memory suggests otherwise.
- Do not guess the source version. If `_dev/build/build.yml` is missing or malformed, ask.
- Do not recommend "upgrade to the latest" if the user named a specific target — honor their target exactly.
- Keep the report focused on the package field set. A full-schema diff is out of scope unless the user explicitly asks for one (and then warn about size first).
- A field being present in both versions with identical `type`, `level`, `is_array`, and allowed values is not worth a row. Silence is signal.
- For the "New fields worth adopting" section, err on the side of fewer suggestions. One strong suggestion beats ten weak ones, and hallucinated relevance erodes trust.
- When the user is ready to apply changes, make the edits against the package files directly — do not produce patches the user has to transcribe.

# Always report the source of model and effort

## Overview

Exec's selection line and decision JSON always carry `effective` when a CLI is chosen. It says who fixed each field (`router`, `native`, `verified_child_default`, `child_default_unknown`). Today `cmd/agrouter/fallback.go:100` attaches `effective` only when a model or effort is left to the child. A caller therefore cannot tell a classifier choice from a native argument such as `-- --model x --effort y`. The change is additive. The `effective` object keeps its shape, and no field is renamed or removed.

Decisions taken (documented in the README):

- Exec: `effective` is always present on a line that precedes a run. A block found after routing still logs the selection without `effective`, `fallback` or `readiness`, because that line claims nothing about a run (unchanged).
- Decision mode: a non-null field is `router`, because decision mode rejects `--`. A null field (a CLI known but routing failed) is `child_default_unknown` with the new reason code `default_not_probed`, because decision mode never probes. Exec uses the same code if a chosen CLI has no preflight result. When `cli` is null, nothing is chosen, so `effective` is absent.
- Model without effort levels (`d.NoEffort`): `effective.effort` stays out. `null_reasons.effort` (`no_applicable_effort`) already explains it, and no new source code is needed.

```
             field set?        native token?       probe default?
model/effort --yes--> native? --yes--> native
                         \--no---> router
             --no---> exec probe reported it? --yes--> verified_child_default
                         \--no---> child_default_unknown
                                   reason: default_not_reported | default_for_other_model
                                           | default_not_probed (decision mode, no probe)
```

Note: every chosen CLI gets one source per field. Only a model without effort levels leaves `effort` out.

## Context

- Files involved: `cmd/agrouter/fallback.go` (execSelection, effective, reason codes), `cmd/agrouter/app.go` (selectionJSON comment, printDecision, the cannot-decide writeDecision path), `cmd/agrouter/fallback_test.go`, `cmd/agrouter/app_test.go`, `cmd/agrouter/readiness_test.go`, `cmd/agrouter/openai_test.go`, `README.md` (decision examples at lines 159, 165 and 246, selection line paragraph about line 180, "Exec fallback" tables about lines 297-310), `CLAUDE.md` and `AGENTS.md` (stdout rule, kept identical), `docs/backlog/selection-line-lacks-field-source.md`, `docs/backlog/README.md`
- Related patterns: the existing `effective()` helper and source constants; `defaultReasonTexts` for warning words; synthetic env helpers in `fallback_test.go` (`newSyntheticEnv`, `defaultsChecker`, `selectionLog`, `chosenBy`, `verified`, `unknownDefault`)
- Dependencies: none
- `confidence_test.go` tests `selection()` directly. `selection()` does not change, so its JSONEq goldens stay valid.

## Development Approach

- **Testing approach**: Regular (code first, then tests)
- Complete each task fully before moving to the next
- No catalog names in production code; tests use the synthetic fixtures already in `fallback_test.go`
- **CRITICAL: every task MUST include new/updated tests**
- **CRITICAL: all tests must pass before starting next task**

## Implementation Steps

### Task 1: Always attach effective in exec

**Files:**
- Modify: `cmd/agrouter/fallback.go`
- Modify: `cmd/agrouter/fallback_test.go`
- Modify: `cmd/agrouter/app_test.go`
- Modify: `cmd/agrouter/readiness_test.go`
- Modify: `cmd/agrouter/openai_test.go`

- [x] add `defaultNotProbed = "default_not_probed"` beside the other unknown-default codes, with warning text "no probe ran" in `defaultReasonTexts`
- [x] restructure `execSelection` so it computes `effective` also when `d.checked` is nil: no defaults, with reason `defaultNotProbed`; it skips only the quota and unknown checks that need `pre`, and guards `readinessSummary(pre.ev, unknown)` so it runs only when `pre` is not nil (no `readiness` when `d.checked` is nil)
- [x] remove the `s.Model == nil || (s.Effort == nil && !d.NoEffort)` condition, so `s.Effective` is always set; keep effort out when `d.NoEffort`; keep the after-routing block path returning before `effective` is set
- [x] update the godoc of `execSelection`, `effectiveJSON` and the source constants ("what the child runs with and who fixed each value")
- [x] update tests in `fallback_test.go` that assert `effective` is absent: "success" (both `router`), "a model without efforts" (model `native`, no effort key), and "native values with source native on success" near line 325 (both `native`)
- [x] keep "an effort left to a passed-through model" near line 273 unchanged: it already checks `effective` with model `native` and effort `verified_child_default`
- [x] update the full selection-line comparisons that now gain `effective`: `app_test.go` (lines about 437, 452, 492, 504, 833, 1301, 1317, 1333, 1509, 1517), `readiness_test.go` (lines about 208, 246, 277) and `openai_test.go` (line about 205); add the expected `effective` object to each, so `make test` passes before task 2
- [x] add a unit test for `execSelection` with `routed.checked == nil`: router values keep `router`, null values report `child_default_unknown` / `default_not_probed`, no `readiness` key, and no panic
- [x] keep the after-routing block test asserting no `effective`, `fallback` or `readiness`
- [x] run `make test` - must pass before task 2

### Task 2: Attach effective in decision JSON

**Files:**
- Modify: `cmd/agrouter/app.go`
- Modify: `cmd/agrouter/fallback.go`
- Modify: `cmd/agrouter/app_test.go` (or `fallback_test.go`, next to the selection-line tests)
- Modify: `cmd/agrouter/openai_test.go`

- [x] add `decisionEffective(s selectionJSON, noEffort bool) *effectiveJSON`: nil when `s.CLI` is nil; else `effective(s.Model, "", nil, defaultNotProbed)`, and the same for effort unless `noEffort`
- [x] set `Effective` in `printDecision` (concise and verbose) and in the cannot-decide `writeDecision` path in `app.go`: `router.cannotDecide` returns an empty decision when several CLIs remain, so `cli` is null and `effective` stays absent; keep the existing error response and exit code 2 unchanged
- [x] move the `Effective` field out of the "exec only" group in `selectionJSON` and update its comment
- [x] update the exact decision comparisons that now gain `effective`: `app_test.go:519` ("prompt only") and `openai_test.go:167`; add the expected `effective` object (both fields `router`)
- [x] update the concise-output field count in `app_test.go:789` (`assert.Len(t, d, 5)`) to include `effective`
- [x] add decision-mode tests with synthetic fixtures: routed model and effort are both `router`; a CLI known but routing failed gives `child_default_unknown` / `default_not_probed` for the null fields; a model without efforts has no effort key; cannot decide with more than one CLI keeps exit code 2 and the stderr error line, and its JSON has no `effective`; `--verbose` output carries the same `effective`
- [x] run `make test` - must pass before task 3

### Task 3: Verify acceptance criteria

- [x] check that `cmd/agrouter/guard_test.go` passes (no catalog names added)
- [x] run `make test`
- [x] run `make lint`, also with `GOOS=linux`, `GOOS=darwin` and `--build-tags=eval`
- [x] verify coverage of `cmd/agrouter` stays 80%+ (`go test -cover ./cmd/agrouter`)

### Task 4: Update documentation and close the backlog item

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`
- Modify: `AGENTS.md`
- Modify: `docs/backlog/README.md`
- Delete: `docs/backlog/selection-line-lacks-field-source.md`

- [ ] README decision examples at lines 159, 165 and 246: add `effective` to each so it matches the new output (both fields `router` at 159 and 165; at 246 `--cli=alpha` with null model and effort, so both `child_default_unknown` with reason `default_not_probed`)
- [ ] README line about 180: `effective` is now in both modes, not exec only
- [ ] README "Exec fallback" field table: `effective` is always present when a CLI is chosen (exec and decision mode), absent when `cli` is null and on the after-routing block line; `effort` is absent for a model without effort levels
- [ ] README source table: add `default_not_probed` (decision mode, or no probe result) to `child_default_unknown`; add a sentence that decision mode reports only `router` or `child_default_unknown`; update the example selection line if needed
- [ ] CLAUDE.md and AGENTS.md stdout rule (keep both files identical): name `effective` beside `reason` and `null_reasons` as always present for a chosen CLI, and state the exception that the selection line before an after-routing block (`agrouter: run skipped: ...`) has no `effective`, `fallback` or `readiness`, as the README says
- [ ] delete `docs/backlog/selection-line-lacks-field-source.md` and remove its entry and section from `docs/backlog/README.md`; leave a "no open items" line when the list is empty
- [ ] run `make test` and `make lint` once more

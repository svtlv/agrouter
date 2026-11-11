# Pass Native Arguments Through Unchanged

## Overview

Ralphex picks a model and effort per phase (`task_model`, `review_model`, `plan_model`) and appends
them as native flags after agrouter's `--`. Today `pkg/args/native.go` strips those flags and replaces
them with agrouter's routed choice, so `review_model = opus:medium` still runs Opus at xhigh.

After this change, everything after agrouter's first `--` reaches the chosen CLI exactly as given.
Agrouter emits its routed model/effort first, so a native selection later in argv overrides it under
the CLI's own rules. Agrouter no longer has `--model`/`-m`/`--effort` options in either mode; routing
always chooses both.

Source: backlog item `docs/backlog/ralphex-model-effort-overrides-discarded.md` (deleted in the last task).

## Context (from discovery)

- `pkg/args/native.go`: `nativeOptions` and `flagValue` strip reserved spellings; `CheckSeparator` stays.
- `pkg/args/build.go`: argv order today is native options, then generated selections, then the child's `--` tail.
- `pkg/args/redact.go`: redacts two raw ranges (`rawAt`, `tailAt`); becomes one.
- `pkg/args/request.go`: `Request.Model`/`Request.Effort` fields (removed).
- `cmd/agrouter/flags.go`: `Model`/`Effort` go-flags options and help text.
- `pkg/router/eligibility.go`, `router.go`, `stage.go`, `question.go`: `--model`/`--effort` filters,
  `ModelPassthrough`/`EffortPassthrough`, caller values in `cannotDecide`, passthrough question text.
- `pkg/router/testdata/request_{model,effort}_passthrough.json`: goldens for removed paths.
- `pkg/catalog/catalog.go`: `LookupModel`, used only by `filterModel`.
- `cmd/agrouter/app.go`: `dropUnpassableEffort`, `routed.effortDropped` and the `effort_not_passable`
  null reason (only a caller `--effort` reaches them).
- `pkg/router/pool.go`: chunked stage questions take `el.effort`.
- `cmd/agrouter/fallback.go`: `sourceCaller`, `effective(..., fixed, ...)`, fallback warning.
- `cmd/agrouter/reason.go`: `delegated` null reason (passthrough-only once constraints go).
- `cmd/agrouter/debug.go`: passthrough debug lines.
- `pkg/config/defaults/config`: argv-order comment; codex `model_args`.
- Docs: `README.md` (Usage table, Staged routing table, Failure and null reasons, Exec fallback,
  Invocation descriptors, Migrating, ralphex), `AGENTS.md` and `CLAUDE.md` (identical copies).

Duplicate-option behavior, checked parse-only with `--help` and an empty `CODEX_HOME` (no inference):

| CLI | Repeated model flag | Repeated effort |
| --- | --- | --- |
| Claude 2.1.322 | accepted (commander; later value wins) | accepted |
| Codex 0.167.1 | exit 2: `'--model <MODEL>' cannot be used multiple times` (also `--model` + `-m`) | `-c` repeats accepted |

Because of the Codex row, codex's embedded `model_args` moves from `--model` to `-c model="..."`.
That is a config-only change: a native `-m X` then overrides the config layer instead of duplicating a flag.

## Development Approach

- **testing approach**: Regular (code first, then tests in the same task)
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
  - tests are not optional - they are a required part of the checklist
  - write unit tests for new functions/methods
  - write unit tests for modified functions/methods
  - add new test cases for new code paths
  - update existing test cases if behavior changes
  - tests cover both success and error scenarios
- **CRITICAL: all tests must pass before starting next task** - no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- run tests after each change (`make test`, `make lint`)
- backward compatibility: intentionally broken for `--model`/`-m`/`--effort` (rejected with a migration
  hint); everything else stays compatible

## Testing Strategy

- **unit tests**: required for every task, table-driven with testify.
- **end-to-end**: `cmd/agrouter` tests with the test binary as fake CLI (`GO_WANT_HELPER_PROCESS=1`),
  recording exact argv and stdin; classifier is an `httptest` server or the moq mock. No live inference.
- Synthetic catalogs and descriptors only; do not pin the shipped codex `model_args` in tests
  except through the existing "embedded config loads and validates" smoke test.
- Router goldens: delete the two passthrough goldens; `go test ./pkg/router -update` must not change
  the remaining ones. Classifier wire goldens are untouched.
- No UI e2e suite in this project.

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope
- keep plan in sync with actual work done

## Solution Overview

```text
agrouter exec claude --doc=AGENTS.md -- --verbose --model opus --effort medium
        |                  |              |
        |                  |              +-- native argv: opaque, never read
        |                  +-- routing only (never in child argv)
        v
 routing (cli -> model -> effort), no caller constraints
        |
        v
claude -p --model <routed> --effort <routed>  --verbose --model opus --effort medium  [-- prompt]
       |  exec_args + generated defaults   |  native argv, unchanged          |  prompt_args
```

**Note:** agrouter still routes and emits its own model/effort, but only as defaults placed before
the caller's native arguments. The child CLI sees both and applies its own rule (Claude: the later
value wins), so the native `opus`/`medium` take effect. Agrouter never looks inside the native part.

Key decisions (from planning):

| Decision | Choice |
| --- | --- |
| Native argv | passed unchanged: no stripping, validation, alias mapping or inspection |
| Argv order | `command + exec_args + model_args + effort_args + native argv + prompt_args` |
| `--model`/`-m`/`--effort` | removed in **both** modes; rejected (exit 2) with a migration hint |
| Routing | always classifies model and effort; nothing in native argv constrains it |
| Selection line | keeps the routed `cli`/`model`/`effort`; adds a fixed note when native args are present |
| Codex model override | codex `model_args` becomes `["-c", "model=\"{model}\""]` so native `-m` overrides without a duplicate-flag error |

## Technical Details

### Argv assembly (`pkg/args/build.go`)

```text
before: cmd exec_args [native opts] model_args effort_args [native "--" tail] prompt_args
after:  cmd exec_args model_args effort_args [entire native argv, as given] prompt_args
```

**Note:** the native sequence is no longer split around the child's own `--`; it moves as one block
after the generated defaults.

- `Result` keeps `promptAt`, one `rawAt`/`rawLen` range; `tailAt`/`tailLen` go away.
- `Redacted()` prints the single native block as `<N raw argument(s)>`.
- `CheckSeparator` stays as an assembly guard (native `--` plus `prompt_args` with `--` and a
  command-line prompt is exit 2). It never inspects option values.

### Option removal (`cmd/agrouter/flags.go`)

Keep `Model`/`Effort` in `flagSet` as `hidden:"true"` options so go-flags still recognizes every
spelling (`--model X`, `--model=X`, `-m X`, `-mX`, `--effort X`). Their funcs only record that they were
seen; `flagCounts.check` then returns a usage error (exit 2):

```text
agrouter: --model/-m is not an agrouter option: routing chooses the model; with exec, pass the
          child's own model flag after "--" (agrouter exec CLI ... -- --model X)
```

The message is fixed and names `--model`/`-m` together, since the shared go-flags callback only gets
the value. `--effort` has its own fixed message. A hidden option keeps the go-flags error
from turning into a generic "unknown flag". Only tokens before agrouter's `--` are parsed, so native
`--model` is never seen here.

### Routing (`pkg/router`)

`Eligible` keeps only the `--cli` filter. `ModelPassthrough`, `EffortPassthrough`, `filterModel`,
`filterEffort`, the `passed` effort in `optionEffort`, caller model/effort in `cannotDecide`, the
passthrough branch of `modelLabel`, and passthrough-only question text (`descModelPassed`, the
`effortDefault` branch of `noEffortLabel`) are removed when unused. `catalog.LookupModel` is removed
if nothing else uses it.

### Reporting (`cmd/agrouter/fallback.go`, `reason.go`)

- `effective()` loses its `fixed` parameter and `sourceCaller`; sources are `router`,
  `verified_child_default` and `child_default_unknown`.
- `delegated` null reason: once passthrough is gone, a successful route always sets the model and
  either sets the effort or reports `no_applicable_effort`. Confirm it is unreachable, then remove it
  and its README row.
- Exec selection line: when `len(req.Raw) > 0`, add a fixed field next to the routed values:

```json
{"cli":"claude","model":"opus","effort":"xhigh",
 "native_override":{"code":"not_inspected","message":"Native arguments after \"--\" are passed unchanged and may override this model and effort"}}
```

- The fallback warning appends `; native arguments after "--" may override them` when native args are
  present. Readiness probes and their defaults still describe agrouter's configured context.
- No native token value is ever logged; only this fixed text.

### Codex descriptor (`pkg/config/defaults/config`)

```ini
model_args  = ["-c", "model=\"{model}\""]
effort_args = ["-c", "model_reasoning_effort=\"{effort}\""]
```

**Note:** both routed defaults now sit in Codex's `-c` config layer. A native `-m X` sets the CLI
flag, which Codex applies over config, and a later native `-c model_reasoning_effort=...` replaces
the earlier `-c`. Task 6 confirms both without inference.

### Ralphex shapes (Claude)

| Ralphex setting | Native tokens | Child result |
| --- | --- | --- |
| `opus:medium` | `--model opus --effort medium` | both override routed values |
| `opus:` / `opus` | `--model opus` | model overridden; routed effort kept |
| `:medium` | `--effort medium` | routed model kept; effort overridden |
| `:` | none | routed model and effort |

## What Goes Where

- **Implementation Steps**: code, tests, embedded config and docs in this repo.
- **Post-Completion**: user-owned ralphex/agrouter configs, reinstalling the binary, the live ralphex check.

## Implementation Steps

### Task 1: Pass native argv unchanged after generated defaults

**Files:**
- Modify: `pkg/args/build.go`
- Modify: `pkg/args/native.go`
- Modify: `pkg/args/redact.go`
- Modify: `pkg/args/request.go`
- Modify: `pkg/args/build_test.go`
- Modify: `pkg/args/native_test.go`
- Modify: `cmd/agrouter/app_test.go` (native-stripping expectations, e.g. near line 1374)
- Modify: `cmd/agrouter/debug_test.go` (redacted argv order, e.g. near line 67)

- [ ] delete `nativeOptions` and `flagValue` from `pkg/args/native.go`; keep `CheckSeparator` and reword its comment as an assembly guard
- [ ] rewrite `Build` in `pkg/args/build.go`: command, exec_args, model_args, effort_args, then all of `req.Raw`, then prompt_args; drop `tailAt`/`tailLen`
- [ ] update `Redacted` in `pkg/args/redact.go` for the single raw range; update the `Raw` field comment in `request.go`
- [ ] update `build_test.go`: generated defaults precede the whole native sequence, including a child `--` and its tokens
- [ ] add passthrough cases: empty tokens, repeated/attached/equals forms (`--model=x`, `-mx`, `-m=x`), a flag with a missing value, later separators, codex-style `-c key="v"`, aliases, `-m` as a separate value; each must come out byte-identical and in order
- [ ] keep `native_test.go` for `CheckSeparator` (prompt after `--` with native `--` rejected; stdin prompt allowed)
- [ ] update redaction tests (one `<N raw argument(s)>` block, prompt still `<prompt>`)
- [ ] update `cmd/agrouter` tests that expect native model/effort to be stripped or the old argv order (`app_test.go`, `debug_test.go`); they now expect the native tokens verbatim after the generated defaults
- [ ] run `make test` - must pass before task 2

### Task 2: Remove model/effort constraints from routing

**Files:**
- Modify: `pkg/router/eligibility.go`
- Modify: `pkg/router/router.go`
- Modify: `pkg/router/stage.go`
- Modify: `pkg/router/question.go`
- Modify: `pkg/router/pool.go`
- Delete: `pkg/router/testdata/request_model_passthrough.json`
- Delete: `pkg/router/testdata/request_effort_passthrough.json`
- Modify: `pkg/router/router_test.go` (and any eligibility/question tests)
- Modify: `pkg/catalog/catalog.go`, `pkg/catalog/catalog_test.go` (if `LookupModel` becomes unused)
- Modify: `cmd/agrouter/debug.go`, `cmd/agrouter/debug_test.go`

- [ ] reduce `Eligible` to the `--cli` filter; remove `filterModel`, `filterEffort`, `ModelPassthrough`, `EffortPassthrough` and the `effort` field
- [ ] drop the effort argument from `stageQuestion` and update both callers, including the chunked path in `pool.go` (`el.effort`)
- [ ] remove the caller model/effort from `cannotDecide` and its doc comment; simplify `optionEffort` and `modelLabel`
- [ ] remove passthrough-only question text in `question.go` (check `descModelPassed`, `noEffortLabel` and `effortDefault` users first)
- [ ] remove `catalog.LookupModel` if it has no remaining users
- [ ] remove the passthrough lines from `cmd/agrouter/debug.go`; reword the `d.Undecided` line ("running %s with the caller's fixed values") to say it keeps choices from completed stages and leaves the rest to the child's defaults; update its test
- [ ] keep `request_stage_effort.json` byte-identical: the test reached the effort stage through `Request.Model`, so change the harness to record every request and assert on the effort-stage one (synthetic catalog with one CLI and one model if needed)
- [ ] delete the two passthrough goldens and their test cases; run `go test ./pkg/router -update` and confirm no other golden changes
- [ ] update router tests: cannot-decide with one CLI left uses only the classifier's earlier choices; staged routing always asks model and effort when more than one candidate is left
- [ ] run `make test` - must pass before task 3

### Task 3: Reject `--model`/`-m`/`--effort` in both modes

**Files:**
- Modify: `cmd/agrouter/flags.go`
- Modify: `pkg/args/request.go`
- Modify: `cmd/agrouter/app.go`
- Modify: `cmd/agrouter/reason.go`
- Modify: `cmd/agrouter/flags_test.go`, `cmd/agrouter/command_test.go`, `cmd/agrouter/app_test.go`
- Modify: `cmd/agrouter/fallback_test.go`, `cmd/agrouter/reason_test.go`, `cmd/agrouter/readiness_test.go`, `cmd/agrouter/debug_test.go`
- Modify: `cmd/agrouter/app_windows_test.go` (build-tagged; check it compiles with `GOOS=windows go vet ./cmd/agrouter`)

- [ ] mark `Model`/`Effort` in `flagSet` `hidden:"true"`; their funcs set a seen flag in `flagCounts`; `check` returns a fixed migration-hint usage error (exit 2) in both modes, one message naming `--model`/`-m` and one for `--effort` (the shared callback cannot tell `-m` from `--model`)
- [ ] remove `Request.Model` and `Request.Effort`; fix every compile site
- [ ] remove `dropUnpassableEffort`, `routed.effortDropped`, its `selectionOutput` branch and `nullEffortDropped` (`effort_not_passable`), with their tests
- [ ] migrate every `cmd/agrouter` test fixture that passes `--model`/`-m`/`--effort` before `--` (fallback, reason, readiness, debug, command, app and `app_windows_test.go`): drop the option or move the case to the native block, so each test still covers its own concern (e.g. the Windows start failure keeps exit 127)
- [ ] update `helpText`: drop the "replaces native --model/-m and --effort" sentence; say native options pass unchanged after the routed defaults
- [ ] test rejection for `--model X`, `--model=X`, `-m X`, `-mX`, `--effort X` in decision and exec mode (exit 2, hint text, no classifier request, no child start)
- [ ] test that `--model`/`--effort` after agrouter's `--` are not rejected and reach the fake child verbatim
- [ ] test that `--help` does not list the hidden options
- [ ] run `make test` - must pass before task 4

### Task 4: Report routed defaults and the native override note

**Files:**
- Modify: `cmd/agrouter/fallback.go`
- Modify: `cmd/agrouter/reason.go`
- Modify: `cmd/agrouter/app.go` (selection JSON)
- Modify: `cmd/agrouter/fallback_test.go`, `cmd/agrouter/reason_test.go`, `cmd/agrouter/app_test.go`

- [ ] drop `sourceCaller` and the `fixed` parameter of `effective`
- [ ] confirm the `delegated` null reason is unreachable after Task 2; remove it, or record here why it stays
- [ ] add `native_override` (`code: not_inspected`, fixed message) to the exec selection line when `len(req.Raw) > 0`; never in decision mode
- [ ] append the fixed native note to the fallback warning when native args are present
- [ ] test the selection line with and without native args, on success and on routing failure (fallback)
- [ ] test that no native token text appears on stderr, in the console log or in debug output
- [ ] run `make test` - must pass before task 5

### Task 5: End-to-end native passthrough and ralphex shapes

**Files:**
- Modify: `cmd/agrouter/app_test.go` (or a new `cmd/agrouter/native_test.go`)

- [ ] with a synthetic catalog and a fake classifier, run exec against the helper-process fake CLI and assert exact argv: generated defaults, then the native block unchanged, then prompt_args
- [ ] cover the ralphex Claude shapes from Technical Details (`--model opus --effort medium`, model only, effort only, none) for different task/review settings
- [ ] cover a stdin-only call (ralphex) with native args: stdin replayed byte-identical, no prompt_args emitted
- [ ] cover classifier failure: native args still in argv, fallback warning carries the native note
- [ ] cover invalid native args (unknown flag, missing value): agrouter passes them on, and the fake child's exit code is agrouter's exit code
- [ ] assert that every classifier stage is still asked when native model/effort flags are present (no stage skipped)
- [ ] run `make test` - must pass before task 6

### Task 6: Codex descriptor uses the config layer for the model

**Files:**
- Modify: `pkg/config/defaults/config`
- Modify: `pkg/config/config_test.go` (only if the smoke test needs it)

- [ ] change codex `model_args` to `["-c", "model=\"{model}\""]`, with a comment on why (Codex rejects a repeated `--model`)
- [ ] rewrite the argv-order comment above `[cli.<name>]` to the new order, without the "replaces native" sentence
- [ ] confirm, parse-only with an empty `CODEX_HOME` and no credentials, that `codex exec -c model="a" -m b --help` and a repeated `-c model_reasoning_effort` both parse (exit 0)
- [ ] try to confirm that the `-m` flag beats `-c model` without inference, e.g. from the model shown in `codex exec`'s startup header with no credentials. If that is not possible, mark ⚠️ and move it to Post-Completion
- [ ] run `make test` (embedded config smoke test) - must pass before task 7

### Task 7: Verify acceptance criteria

- [ ] every backlog acceptance check is covered: argv order, token preservation, aliases kept, no routing constraint, classifier failure, stdin replay, invalid args reaching the child, child exit code authoritative, removed options rejected, reporting distinguishes routed defaults
- [ ] `grep` for leftover `nativeOptions`, `ModelPassthrough`, `sourceCaller` and "reserved" wording in code and docs
- [ ] `make test` and `make lint`; also lint `GOOS=linux`, `GOOS=darwin` and `--build-tags=eval` (golangci-lint v2.14.0)
- [ ] coverage stays at 80%+ per package

### Task 8: [Final] Update documentation

- [ ] `README.md` Usage: remove the `-m`/`--model`/`--effort` row; update the `-- native args` row (passed unchanged after routed defaults)
- [ ] `README.md` Staged routing: remove the `--model`/`--effort` rows from the skip table; drop "implied by `--model`" in Failure and null reasons and Exec fallback; remove the `caller` source, `delegated` and `effort_not_passable` rows
- [ ] `README.md` Invocation descriptors: new argv formula; replace the reserved-spellings text and examples with passthrough examples (Claude later-wins, Codex `-m` over `-c model`); keep the duplicate-separator paragraph
- [ ] `README.md` Migrating: add rows for removed `--model`/`-m`/`--effort` and for native selections that now pass through
- [ ] `README.md` ralphex: use the backlog's example config (`task_model = opus:`, `review_model = opus:medium`), remove the "leave models empty" advice and any temporary pinned flags, and explain the ralphex fallback rule (`review_model` empty falls back to `task_model`, `:` emits nothing)
- [ ] `AGENTS.md` and `CLAUDE.md` (keep identical): rewrite the Invocation rule, the staged-routing "fixed by `--model`/`--effort`" clause, the "no per-CLI argument mappings" rule and the reserved-spelling sentence in the exit-code rule; update the `pkg/args` layout line
- [ ] delete `docs/backlog/ralphex-model-effort-overrides-discarded.md`
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Manual verification:**
- reinstall (`go install ./cmd/agrouter`), then run one real ralphex review phase with `review_model = opus:medium`. The child should run Opus at medium; check the selection line shows the routed values plus `native_override`.
- if Task 6 could not confirm Codex flag-over-config precedence without inference, check it once on a cheap Codex run.

**External system updates:**
- user-owned ralphex config: remove the temporary `--model opus --effort medium` pinned before agrouter's `--`, and set per-phase `task_model`/`review_model`/`plan_model`
- user agrouter configs (global and local `.agrouter/config`) that set codex `model_args = ["--model", ...]` explicitly should switch to the `-c model=` form; comment-only global configs refresh automatically
- any script that passes `--model`/`-m`/`--effort` before agrouter's `--` now gets exit 2 and must move them after `--`

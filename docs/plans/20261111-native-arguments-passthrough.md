# Native Model and Effort Overrides, With or Without a Provider

## Overview

Ralphex picks a model and effort per phase (`task_model`, `review_model`, `plan_model`) and appends
them as native flags after agrouter's `--`. Today `pkg/args/native.go` strips those flags and replaces
them with agrouter's routed choice, so `review_model = opus:medium` still runs Opus at xhigh.

After this change:

- everything after agrouter's first `--` reaches the chosen CLI exactly as given (never stripped or rewritten);
- agrouter recognizes a native model/effort by each CLI's **own** `model_args`/`effort_args` templates
  from config, and resolves it against the catalog (`[model.*]` names and aliases, `[effort.*]`);
- a natively set field is not routed (no classifier call for that stage) and not generated again, so
  the child gets one model flag and one effort flag; an unset field is routed and generated, so
  model-only and effort-only overrides both work;
- exec no longer needs a provider: `agrouter exec "fix bug" -- ...` lets the native args imply the CLI,
  or the router choose it. `exec <cli>` or `exec --cli <cli>` still pins it;
- `--model`/`-m`/`--effort` are no longer agrouter options in either mode. Decision mode keeps
  rejecting `--`, so it pins only through `--cli`.

Source: backlog item `docs/backlog/ralphex-model-effort-overrides-discarded.md` (deleted in the last task).
By user decision this plan departs from the backlog's "never inspect native args": native args are read
for model/effort only, through config templates, and never modified. Anything wrong after `--` is the
caller's problem; the child validates it.

## Context (from discovery)

- `pkg/args/native.go`: `nativeOptions`/`flagValue` strip reserved spellings (replaced); `CheckSeparator` stays.
- `pkg/args/build.go`: argv today is native options, then generated selections, then the child's `--` tail.
  `Choice` with an empty `Model`/`Effort` already emits nothing for that field.
- `pkg/args/redact.go`: two raw ranges (`rawAt`, `tailAt`); becomes one.
- `pkg/args/request.go`: `Request.Model`/`Request.Effort` (replaced by per-CLI native selections).
- `cmd/agrouter/flags.go`: `execOperand` requires the CLI right after `exec`; `--cli` in exec must equal
  it; `Model`/`Effort` options; help text.
- `cmd/agrouter/app.go` `route`: exec resolves one `execCLI`, runs `CheckSeparator` and one preflight
  (`startPreflight`/`applyPreflight` in `cmd/agrouter/readiness.go`) beside capture, then `router.Eligible`.
- `cmd/agrouter/fallback.go` `execSelection`: delegated-model check uses the single preflight report.
- `pkg/router/eligibility.go`: `filterCLI`, `filterModel`, `filterEffort`, `ModelPassthrough`,
  `EffortPassthrough` (reused, made per CLI); `router.go` `cannotDecide` already exits with
  `ErrCannotDecide` when more than one CLI is left.
- `pkg/catalog/catalog.go`: `LookupModel` (name or alias; aliases are unique across all models).
- `cmd/agrouter/app.go`: `dropUnpassableEffort`, `routed.effortDropped` (unreachable after this change).
- `cmd/agrouter/reason.go`, `debug.go`: `caller` source, null reasons, "caller's fixed values" wording.
- `pkg/config/defaults/config`: argv-order comment; codex `model_args`.
- Docs: `README.md`, `AGENTS.md` and `CLAUDE.md` (identical copies).
- Ralphex (`~/Projects/ralphex`): Claude executor appends `--model X` / `--effort Y`
  (`pkg/executor/executor.go:366`); Codex executor appends `-c model="X"` and unquoted
  `-c model_reasoning_effort=Y` (`pkg/executor/codex.go:196`).

Duplicate-option behavior, checked parse-only (`--help`, empty `CODEX_HOME`, no inference):

| CLI | Repeated model flag | Repeated effort |
| --- | --- | --- |
| Claude 2.1.322 | accepted (later value wins) | accepted |
| Codex 0.167.1 | exit 2: `'--model <MODEL>' cannot be used multiple times` (also `--model` + `-m`) | `-c` repeats accepted |

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
- backward compatibility: `exec <cli> ...` keeps working unchanged; `--model`/`-m`/`--effort` before
  `--` are intentionally rejected with a migration hint

## Testing Strategy

- **unit tests**: required for every task, table-driven with testify.
- **end-to-end**: `cmd/agrouter` tests with the test binary as fake CLI (`GO_WANT_HELPER_PROCESS=1`)
  recording exact argv and stdin; the classifier is an `httptest` server or the moq mock, so tests can
  count classifier requests per stage. Readiness uses the existing fake-peer fixtures. No live inference.
- Synthetic catalogs with two CLIs: a Claude-like one (`--model {model}`, `--effort {effort}`) and a
  codex-like one (`-c model="{model}"`, `-c model_reasoning_effort="{effort}"`). Do not pin the shipped
  codex `model_args` beyond the embedded-config smoke test.
- Router goldens: existing ones stay byte-identical (`go test ./pkg/router -update` produces no diff);
  classifier wire goldens are untouched.
- No UI e2e suite in this project.

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope
- keep plan in sync with actual work done

## Solution Overview

```text
agrouter exec [claude | --cli claude] "fix bug" -- --verbose --model opus
               \_ optional provider _/            \__ native argv (unchanged) __/
                         |                                     |
                         |             each candidate CLI's templates scan it:
                         |             claude ["--model","{model}"] -> "opus"
                         |             [model.*] alias "opus" -> claude-opus-5-5 (claude)
                         v                                     v
              candidates: pinned CLI, else CLIs the native selection implies, else all
                         v
              routing skips stages already set natively (here: cli, model)
                         v
claude -p --effort <routed>  --verbose --model opus  [-- prompt]
```

**Note:** without a provider, `--model opus` matches only Claude's template and resolves to a Claude
model, so the CLI and model stages are skipped and only the effort stage asks the classifier. With no
recognizable native selection, the router picks the CLI, model and effort, and the native args go to
whichever CLI wins.

Key decisions:

| Decision | Choice |
| --- | --- |
| Provider in exec | operand after `exec` or `--cli`; both must agree (else exit 2); neither: decided |
| Operand vs prompt | the token right after `exec` is the CLI only when it names a configured CLI; otherwise it is the positional prompt |
| Native argv | passed unchanged to the chosen CLI; read-only scan for model/effort |
| Detection | each candidate CLI's own `model_args`/`effort_args` templates; no hardcoded flags |
| Value resolution | that CLI's catalog `[model.*]` name/alias and `[effort.*]`; unknown values pass through |
| Argv | `command + exec_args + generated (unset fields only) + native argv + prompt_args` |
| Classifier fails, more than one CLI left | exit 2, nothing started (as decision mode today) |
| Readiness without a provider | probe every candidate CLI; route over what can run now |
| `--model`/`-m`/`--effort` | rejected in both modes (exit 2, migration hint) |
| Decision mode | keeps rejecting `--`; pins only with `--cli` |
| Codex model template | `["-c", "model=\"{model}\""]`: ralphex's spelling, and avoids Codex's duplicate-`--model` error |

## Technical Details

### Exec command line (`cmd/agrouter/flags.go`)

| Command | Provider |
| --- | --- |
| `agrouter exec claude "fix" -- …` | claude (operand) |
| `agrouter exec --cli claude "fix" -- …` | claude (`--cli`) |
| `agrouter exec claude --cli codex …` | exit 2: conflict |
| `agrouter exec "fix bug" -- …` | decided (native or router) |
| `agrouter exec --doc=AGENTS.md -- …` (prompt on stdin) | decided |
| `agrouter exec -p claude` | decided; prompt "claude" |

`parseArgs` keeps the token right after `exec` as a candidate operand when it does not start with `-`,
and lets go-flags collect positionals. After config load, the candidate is the CLI if it names a
configured `[cli.*]` (a disabled one stays exit 2, as today); otherwise it is the positional prompt.
"More than one positional argument" is checked after that resolution. `--cli` in exec now pins the
provider; `AGROUTER_CLI` stays ignored.

`Model`/`Effort` stay in `flagSet` as `hidden:"true"` options so go-flags recognizes every spelling
(`--model X`, `--model=X`, `-m X`, `-mX`, `--effort X`); `flagCounts.check` returns a fixed usage error
(exit 2) in both modes:

```text
agrouter: --model/-m is not an agrouter option: routing chooses the model; with exec, pass the
          CLI's own model flag after "--" (agrouter exec [CLI] ... -- --model X)
```

`--effort` gets its own fixed message. The shared callback cannot tell `-m` from `--model`, so the
message names both.

### Detection (`pkg/args/native.go`)

`NativeSelection(cli config.CLI, raw []string) Selection` (`Selection{Model, Effort string}`) scans
`raw` up to the child's own `--` and matches that CLI's templates. The last match wins.

| Form | Template | Native tokens matched |
| --- | --- | --- |
| exact sequence | `["--model", "{model}"]` | `--model opus` |
| attached long flag (two-token template, first token `--…`) | `["--model", "{model}"]` | `--model=opus` |
| literal prefix/suffix around the placeholder | `["-c", "model_reasoning_effort=\"{effort}\""]` | `-c model_reasoning_effort="high"` |
| quotes around the placeholder optional | same | `-c model_reasoning_effort=high` (ralphex) |

**Note:** every spelling comes from the CLI's own template; the only generic rules are the `=` form of a
long flag and optional matching quotes. Other spellings (for example Codex's `-m` alias) are not
recognized: they pass through and the CLI resolves the duplicate itself.

Empty values and a template missing its value token (`--model` last) are no selection; the tokens
still reach the child unchanged.

### Candidates and routing (`pkg/router/eligibility.go`)

`Request.Model`/`Request.Effort` become `Request.Native map[string]args.Selection` (CLI name to its
detected selection), filled in `cmd/agrouter` for every enabled CLI, or only the pinned one.
`Eligible` applies, in order:

1. `--cli`/operand pin, as today.
2. Native CLI filter: if any CLI detected a selection, keep only those CLIs; else keep all.
3. Per kept CLI, the existing model and effort filters with that CLI's own values (catalog model,
   passthrough outside the catalog, effort in or outside the catalog). A catalog model belongs to one
   CLI (aliases are unique), so it pins that CLI.
4. A catalog model for CLI A wins over an effort-only detection for CLI B: B is dropped. Its tokens
   still reach A unchanged; that is the caller's problem.

Routing cases (one CLI left, pinned or implied):

| Native model | Native effort | Classifier asked | Generated args |
| --- | --- | --- | --- |
| catalog model | catalog effort | nothing | none |
| catalog model | none | effort stage among that model's efforts (skipped if one) | effort |
| none | catalog effort | model stage among models with that effort (skipped if one) | model |
| none | none | model and effort stages | model and effort |
| outside catalog | any | nothing | none; an unset effort is left to the child (`delegated`) |
| none | outside catalog | model stage among all models (effort passthrough) | model |
| catalog model | effort that model lacks | nothing (passthrough) | none; the child validates |

Without a provider and several CLIs left (no selection, or the same template matching several CLIs),
the cli stage runs first, then the rows above apply to the chosen CLI. If the classifier fails with more
than one CLI left, exec exits 2 with the existing cannot-decide line and starts nothing. With one CLI
left, the existing fallback (child defaults plus native values) applies.

`cmd/agrouter` builds `Choice` with empty `Model`/`Effort` for natively set fields of the chosen CLI,
so `Build` emits nothing for them; the selection line reports the resolved native value.

### Readiness without a provider (`cmd/agrouter/readiness.go`, `app.go`)

```text
eligible CLIs ──┬── probe claude ──┐
                └── probe codex ───┴── apply each report to its CLI's options ── route over what is left
```

**Note:** each candidate CLI is probed in parallel, beside prompt capture, exactly as the single exec
CLI is today. A blocked CLI just drops out of routing.

- `startPreflight` runs per candidate CLI under the shared deadline; `applyPreflight` excludes each
  CLI's blocked options from the eligibility.
- Only when every candidate is blocked does exec stop: it prints each CLI's block line in config order
  (quota lines keep their own `error_prefix`/`error_message`) and uses the first CLI's exit code.
- `execSelection`'s delegated-model check uses the chosen CLI's report.
- `CheckSeparator` runs for the pinned CLI before routing, and for a decided CLI after routing, before
  the child starts (exit 2).

### Argv assembly (`pkg/args/build.go`)

```text
before: cmd exec_args [native opts] model_args effort_args [native "--" tail] prompt_args
after:  cmd exec_args [model_args] [effort_args] [entire native argv, as given] prompt_args
```

**Note:** generated args appear only for fields the native args did not set, and the native sequence
moves as one block, including the child's own `--`.

`Result` keeps `promptAt` and one `rawAt`/`rawLen`; `Redacted()` prints one `<N raw argument(s)>`.

### Reporting

- The selection line's `cli`/`model`/`effort` are what the child runs with: native when set, routed otherwise.
- The `caller` effective source becomes `native`. A CLI pinned by `exec <cli>`/`--cli` is unchanged in
  reporting; a CLI implied by a native model is reported like one implied by `--model` today.
- `dropUnpassableEffort`, `routed.effortDropped` and `effort_not_passable` are removed: a native effort
  is only detected through `effort_args`.
- The debug line "running %s with the caller's fixed values" becomes "… with the native values and the
  child's defaults".
- Native token values are never logged beyond the resolved model/effort in the selection line.

### Codex descriptor (`pkg/config/defaults/config`)

```ini
model_args  = ["-c", "model=\"{model}\""]
effort_args = ["-c", "model_reasoning_effort=\"{effort}\""]
```

**Note:** this is ralphex's own Codex spelling, so ralphex's `-c model="X"` is detected and the routed
model is not emitted. It also keeps Claude's `--model X` from matching Codex, so a provider-less
`--model opus` implies Claude only. An unrecognized native `-m X` still runs: the flag applies over the
`-c` config layer instead of tripping Codex's duplicate-`--model` error.

### Ralphex shapes

| Ralphex setting | Claude native tokens | Codex native tokens | Classifier asked | Generated |
| --- | --- | --- | --- | --- |
| `opus:medium` | `--model opus --effort medium` | `-c model="m" -c model_reasoning_effort=medium` | nothing | none |
| `opus:` / `opus` | `--model opus` | `-c model="m"` | effort stage | effort |
| `:medium` | `--effort medium` | `-c model_reasoning_effort=medium` | model stage | model |
| `:` | none | none | model and effort | both |

Ralphex configs keep `exec claude …` / `exec codex …`, so these rows run with a pinned provider.

Exact native argv ralphex sends (checked in `~/Projects/ralphex` at v1.7.0-windows.3-9), used as e2e fixtures:

```text
claude: claude_args after "--" ... [--model M] [--effort E] --print          (prompt on stdin)
codex:  [-c features.multi_agent=true -c agents.<name>.description="..."] [-c project_doc_fallback_filenames=["CLAUDE.md"]]
        [--dangerously-bypass-approvals-and-sandbox] --sandbox S [-c model="M"] [-c model_reasoning_effort=E]
        -c stream_idle_timeout_ms=N [-c project_doc="P"]                     (prompt on stdin; wrapper drops "exec")
```

**Note:** ralphex never sends a `--` of its own, so the whole sequence is scanned. `--print` duplicates
Claude's `-p` from `exec_args`, which Claude accepts today. Other `-c` keys must not match the model or
effort templates (`model_x`-style lookalikes are covered in Task 1).

Compatibility notes:

- Ralphex's Codex defaults (`codex_model`, `codex_reasoning_effort`) are non-empty, so a phase with an
  empty spec still sends `-c model=` and `-c model_reasoning_effort=`. Today agrouter's generated
  `--model` beats that native `-c model=`. After this change the native values win and routing is
  skipped. To keep routing in Codex mode, set both to empty (the README's existing advice, now required).
- Ralphex strips `--model`/`--effort` from `claude_args` only when a phase sets them. A workaround that
  pins `--model`/`--effort` before agrouter's `--` therefore reaches agrouter in phases without a spec, and
  is exit 2 after this change. Remove it in the same step as upgrading.
- Full model IDs (`claude-sonnet-4-5-20250929`) and efforts missing from the catalog (for example
  `max`) take the passthrough rows: they run as given, without the classifier for that field.

## What Goes Where

- **Implementation Steps**: code, tests, embedded config and docs in this repo.
- **Post-Completion**: user-owned ralphex/agrouter configs, reinstalling the binary, live checks.

## Implementation Steps

### Task 1: Detect native model/effort from a CLI's templates

**Files:**
- Modify: `pkg/args/native.go`
- Modify: `pkg/args/native_test.go`

- [x] add `Selection` and `NativeSelection(cli, raw)` per the detection table; keep `CheckSeparator` (⚠️ `nativeOptions`/`flagValue` stay until Task 2 rewrites `Build`, their only caller, and are deleted there)
- [x] match exact template sequences, the `--flag=value` form of a two-token long-flag template, and literal prefix/suffix tokens with optional matching quotes around the placeholder
- [x] stop at the child's own `--`; last match wins; empty values and a missing trailing value are no selection
- [x] tests with synthetic Claude-like and codex-like templates: quoted and unquoted, `=` form, repeats (last wins), tokens after the child's `--` ignored
- [x] negative tests: alias spellings (`-m x`), lookalike prefixes (`--modelx`, `-c model_x=1`), empty and missing values, CLI without `effort_args`
- [x] run `make test` - must pass before task 2

### Task 2: Build argv with native args unchanged after generated args

**Files:**
- Modify: `pkg/args/build.go`
- Modify: `pkg/args/redact.go`
- Modify: `pkg/args/build_test.go`
- Modify: `cmd/agrouter/app_test.go` (native-stripping expectations, e.g. near line 1374)
- Modify: `cmd/agrouter/debug_test.go` (redacted argv order, e.g. near line 67)

- [x] rewrite `Build` and delete `nativeOptions`/`flagValue` with their test: command, exec_args, model_args (when `choice.Model` set), effort_args (when `choice.Effort` set), all of `req.Raw`, prompt_args; drop `tailAt`/`tailLen`
- [x] update `Redacted` for the single raw range
- [x] tests: every native token byte-identical and in order (empty tokens, repeats, attached forms, missing values, later separators, codex `-c` expressions, aliases); generated args precede the whole native block
- [x] update redaction tests (one `<N raw argument(s)>` block, prompt still `<prompt>`)
- [x] update `cmd/agrouter` tests that expect stripping or the old order (`app_test.go`, `debug_test.go`)
- [x] run `make test` - must pass before task 3

### Task 3: Per-CLI native constraints in eligibility

**Files:**
- Modify: `pkg/args/request.go`
- Modify: `pkg/router/eligibility.go`
- Modify: `pkg/router/router.go`
- Modify: `pkg/router/router_test.go` (and eligibility tests)
- Modify: `cmd/agrouter/app.go` (temporary wiring from the existing `--model`/`--effort` values until Task 4)

- [x] replace `Request.Model`/`Request.Effort` with `Request.Native map[string]args.Selection`
- [x] `Eligible`: pin filter, native CLI filter, then per-CLI model/effort filters reusing `filterModel`/`filterEffort` logic; a catalog model of CLI A drops an effort-only CLI B
- [x] make `cannotDecide` keep the native values of the one CLI left (read from `req.Native`, so `Route` keeps its signature); reword drop/warning labels to native wording (`native model X`)
- [x] keep `ModelPassthrough`/`EffortPassthrough` semantics per CLI; existing goldens stay byte-identical (`go test ./pkg/router -update` gives no diff)
- [x] tests for every row of the routing cases table, plus multi-CLI: no selection keeps all CLIs; `--model opus` with a Claude-only template pins Claude; a template shared by two CLIs keeps both for the cli stage; conflicting model/effort detections across CLIs keep the model's CLI
- [x] run `make test` - must pass before task 4 (⚠️ temporary wiring: `nativeFromFlags` in `cmd/agrouter/app.go` maps the old `--model`/`--effort` values to `Request.Native`; Task 4 deletes it)

### Task 4: Exec command line: optional provider, `--cli` pin, removed options

**Files:**
- Modify: `cmd/agrouter/flags.go`
- Modify: `cmd/agrouter/app.go`
- Modify: `cmd/agrouter/reason.go`
- Modify: `cmd/agrouter/flags_test.go`, `cmd/agrouter/command_test.go`, `cmd/agrouter/app_test.go`
- Modify: `cmd/agrouter/fallback_test.go`, `cmd/agrouter/reason_test.go`, `cmd/agrouter/readiness_test.go`, `cmd/agrouter/debug_test.go`
- Modify: `cmd/agrouter/app_windows_test.go` (build-tagged; check with `GOOS=windows go vet ./cmd/agrouter`)

- [x] `execOperand` keeps a candidate operand instead of failing; after config load (`resolveExec`) it becomes the CLI if it names an enabled CLI, else the positional prompt; recheck "more than one positional argument" afterwards (⚠️ the config drops disabled CLIs, so a disabled name cannot be told from a prompt: `exec <disabled> "x"` is exit 2 as a second positional, `exec <disabled>` alone is a prompt; `exec --cli <disabled>` is exit 2)
- [x] `--cli` in exec pins the provider; operand and `--cli` disagreeing stays exit 2
- [x] mark `Model`/`Effort` `hidden:"true"`; `check` returns the fixed migration-hint errors (exit 2) in both modes
- [x] in exec, fill `req.Native` from `args.NativeSelection` for the pinned CLI or every enabled CLI (`nativeSelections`); build `Choice` without natively set fields (`generatedChoice`) (⚠️ without a pinned CLI the separator check and readiness probe are skipped until Task 5)
- [x] remove `dropUnpassableEffort`, `routed.effortDropped`, its `selectionOutput` branch and `nullEffortDropped`, with their tests
- [x] update `helpText` and `parser.Usage` (`exec [CLI] ...`; only the provider is pinned by agrouter; model/effort go after `--` as the CLI's own flags)
- [x] migrate every `cmd/agrouter` fixture that passes `--model`/`-m`/`--effort` before `--` to native flags after `--`, so each test keeps covering its concern (e.g. the Windows start failure keeps exit 127)
- [x] tests: every row of the exec command-line table; the rejection for every spelling in both modes (exit 2, hint, no classifier request, no child start); decision mode still rejects `--`; `--help` hides the options
- [x] run `make test` - must pass before task 5

### Task 5: Readiness and separator checks without a provider

**Files:**
- Modify: `cmd/agrouter/readiness.go`
- Modify: `cmd/agrouter/app.go`
- Modify: `cmd/agrouter/fallback.go`
- Modify: `cmd/agrouter/readiness_test.go`, `cmd/agrouter/app_test.go`

- [x] start one preflight per candidate CLI (pinned, or every CLI left after the native filter) under the shared deadline; wait for all on every return path
- [x] apply each report to its CLI's options; stop only when every candidate is blocked, printing each block line in config order with the first CLI's exit code
- [x] `execSelection` uses the chosen CLI's report for the delegated-model check and the readiness summary
- [x] run `CheckSeparator` for the pinned CLI before routing, and for a decided CLI after routing, before the child starts
- [x] tests with fake-peer fixtures: one of two CLIs quota-blocked routes to the other without asking the cli stage; both blocked prints two lines and exits with the first's code; strict unknown on one CLI; probes stop on early return
- [x] run `make test` - must pass before task 6

### Task 6: Report native values

**Files:**
- Modify: `cmd/agrouter/fallback.go`
- Modify: `cmd/agrouter/debug.go`
- Modify: `cmd/agrouter/fallback_test.go`, `cmd/agrouter/debug_test.go`, `cmd/agrouter/app_test.go`

- [x] rename `sourceCaller` (`caller`) to `sourceNative` (`native`) and update its comment
- [x] reword the `d.Undecided` debug line to "native values and the child's defaults"
- [x] tests: the selection line shows native values with source `native` on success and on classifier failure; routed values keep `router`; unknown native model leaves effort `delegated`
- [x] tests: no native token text other than the resolved model/effort appears on stderr, in the console log or in debug output
- [x] run `make test` - must pass before task 7

### Task 7: End-to-end shapes

**Files:**
- Modify: `cmd/agrouter/app_test.go` (or a new `cmd/agrouter/native_test.go`)

- [x] pinned provider: every row of the ralphex table in both spellings against the helper-process fake CLI, using the exact ralphex argv shapes (Claude with trailing `--print`; Codex with `features.multi_agent`, `--sandbox`, `stream_idle_timeout_ms`, `project_doc` around the model/effort `-c`); assert exact argv and classifier requests per stage
- [x] different settings per phase (task `opus:`, review `opus:medium`) produce different argv in two runs
- [x] no provider: `exec "fix" -- --model <claude alias>` runs the Claude-like CLI without cli/model stages; `exec "fix" -- --verbose` asks all three stages and passes `--verbose` to the winner; classifier failure with two CLIs left is exit 2 with nothing started
- [x] stdin-only call with native args: stdin replayed byte-identical, no prompt_args emitted
- [x] invalid native args (unknown flag, missing value, unknown model) reach the fake child; the child's exit code is agrouter's exit code
- [x] run `make test` - must pass before task 8

### Task 8: Codex descriptor uses the config layer for the model

**Files:**
- Modify: `pkg/config/defaults/config`

- [x] change codex `model_args` to `["-c", "model=\"{model}\""]`, with a comment (ralphex's spelling; Codex rejects a repeated `--model`)
- [x] rewrite the argv-order comment above `[cli.<name>]`: generated args only for fields the native args did not set, then native args unchanged; templates also define which native spellings are recognized
- [x] confirm parse-only with an empty `CODEX_HOME` (no credentials, no inference): `codex exec -c model="a" -m b --help` and a repeated `-c model_reasoning_effort` both exit 0 (confirmed on 0.167.1; a repeated `-m` still exits 2)
- [x] try to confirm the `-m` flag beats `-c model` without inference (e.g. the model in `codex exec`'s startup header with no credentials); if not possible, mark ⚠️ and keep it in Post-Completion (confirmed: with empty `CODEX_HOME`, `-c model="cfgmodel" -m flagmodel` shows `model: flagmodel` in the header; no inference ran)
- [x] run `make test` (embedded config smoke test) - must pass before task 9 (⚠️ also updated `TestApp_RalphexCodexMode` and `TestRouteSingleOptionPassthrough` expectations to the `-c model=` form; the ralphex-mode native fixture now uses `-c model="..."`)

### Task 9: Verify acceptance criteria

- [ ] native argv is never modified; generated args never duplicate a recognized native selection
- [ ] each routing case asks only the stages it should; model-only and effort-only overrides work, pinned and unpinned
- [ ] `exec <cli>`, `exec --cli`, provider-less exec and their conflicts behave per the command-line table
- [ ] removed options are rejected with the hint; decision mode rejects `--`; stdin replay; child exit code authoritative
- [ ] `grep` for leftover `nativeOptions`, `sourceCaller`, `effortDropped`, `Request.Model`, and "reserved"/"replaces native"/"exec needs the CLI" wording in code and docs
- [ ] `make test` and `make lint`; also lint `GOOS=linux`, `GOOS=darwin` and `--build-tags=eval` (golangci-lint v2.14.0)
- [ ] coverage stays at 80%+ per package

### Task 10: [Final] Update documentation

- [ ] `README.md` Usage: `agrouter exec [<cli>] [options] [prompt] [-- native args...]`; operand-or-prompt rule; `--cli` pins in exec; remove the `-m`/`--model`/`--effort` row; the `-- native args` row says they pass unchanged and the CLI's model/effort flags (per its templates) fix those fields and can imply the CLI
- [ ] `README.md` Staged routing: skip table rows refer to native model/effort; Failure and null reasons / Exec fallback: native wording, exit 2 when exec cannot decide between CLIs, remove `effort_not_passable`
- [ ] `README.md` Exec readiness: probing every candidate CLI without a provider, block lines when all are blocked
- [ ] `README.md` Invocation descriptors: new argv formula; templates double as recognized native spellings (exact, `--flag=value`, optional quotes); unrecognized spellings such as Codex `-m` pass through; keep the duplicate-separator paragraph
- [ ] `README.md` Migrating: rows for removed `--model`/`-m`/`--effort` and for native selections that are now honored
- [ ] `README.md` ralphex: the backlog's example config (`task_model = opus:`, `review_model = opus:medium`); Codex `codex_model`/`codex_reasoning_effort` are now honored, so keep them empty to let the classifier route Codex (ralphex's own defaults would otherwise fix both); drop any temporarily pinned flags; explain ralphex's fallback (`review_model` empty falls back to `task_model`, `:` emits nothing)
- [ ] `README.md` Exit codes: exec cannot-decide with more than one CLI left
- [ ] `AGENTS.md` and `CLAUDE.md` (identical): rewrite the Invocation rule (operand optional, `--cli` pins in exec, native model/effort recognized by the CLI's templates and never modified), the staged-routing "fixed by `--model`/`--effort`" clause, the "no per-CLI argument mappings" rule, the readiness/exit-code rules and the reserved-spelling sentence; update the `pkg/args` layout line
- [ ] delete `docs/backlog/ralphex-model-effort-overrides-discarded.md`
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Manual verification:**
- reinstall (`go install ./cmd/agrouter`), then run one real ralphex review phase with `review_model = opus:medium`:
  the selection line shows `opus`/`medium` with source `native`, no classifier call is logged, and the child argv holds one `--model` and one `--effort`.
- one ralphex phase with `task_model = opus:` should log only an effort-stage classifier call.
- one provider-less run, `agrouter exec "small task"`, should log a cli-stage call and start the chosen CLI.

**External system updates:**
- user-owned ralphex config: remove the temporary `--model opus --effort medium` pinned before agrouter's `--` **in the same step as installing the new agrouter** (otherwise phases without a spec exit 2); keep `codex_model =`/`codex_reasoning_effort =` empty for routed Codex; set per-phase `task_model`/`review_model`/`plan_model` (and `codex_model`/`codex_reasoning_effort` if wanted)
- user agrouter configs (global and local `.agrouter/config`) that set codex `model_args = ["--model", ...]` explicitly should switch to the `-c model=` form; comment-only global configs refresh automatically
- any script that passes `--model`/`-m`/`--effort` before agrouter's `--` now gets exit 2 and must move them after `--` as the CLI's own flags

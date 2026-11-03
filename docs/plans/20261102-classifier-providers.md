# Selectable classifier providers (TypeSafe / OpenAI)

## Overview
- agrouter speaks only TypeSafe's "System One" wire format today. OpenAI's Decisions API (`POST /v1/decisions`) answers the same kinds of questions with a different request and response shape, so pointing `[classifier] url` at OpenAI does not work.
- Add a provider switch. `[agrouter] classifier_provider = typesafe | openai` (default `typesafe`) selects one of two predefined sections, `[classifier.typesafe]` and `[classifier.openai]`. Each section holds that provider's own keys. `long_context` exists only for TypeSafe.
- The router keeps building one provider-neutral `classifier.Request`. A per-provider wire codec inside `pkg/classifier` turns it into the provider's body and turns the provider's answers back into `classifier.Response`. Retry, backoff, deadline and redaction stay shared.
- This is a hard break, by project rule: the flat `[classifier]` section becomes an unknown section with a hint. `--api-key` and `AGROUTER_CLASSIFIER_API_KEY` apply to the selected provider.

## Context (from discovery)
- files/components involved:
  - `pkg/classifier/types.go`, `client.go`: TypeSafe wire types, `Ask`, retry, redaction, `Limits`, `StatusError`, sentinels
  - `pkg/config/config.go`, `load.go`, `validate.go`: flat `Classifier` struct, `decodeClassifier`, `ResolveAPIKey`
  - `pkg/config/defaults/config`: the `[classifier]` block and a commented, unverified OpenAI Luna example (922K input, 2x billing past 272K)
  - `pkg/router/router.go` (`checkLimits`), `stage.go`, `pool.go`, `complexity.go`, `question.go` (build `classifier.Request`)
  - `pkg/router/eval_test.go` (`//go:build eval`), `cmd/agrouter/app.go` (`newClassifier`), `cmd/agrouter/reason.go`
  - `README.md` (Classifier, Request size, ralphex), `CLAUDE.md`
- related patterns found:
  - typed sentinels (`ErrTooLong`, `ErrOverloaded`, `ErrEmptyAnswer`, ...) mapped to reason codes in `cmd/agrouter/reason.go`, never from bodies
  - strict INI decode: unknown keys and sections are errors; removed keys get hints (`removedAgrouterKeys`)
  - golden requests in `pkg/router/testdata` (TypeSafe shape), rewritten with `-update`
  - provider specifics stay in `pkg/classifier`; user-facing names stay provider-neutral
- dependencies identified: `pkg/classifier` and `pkg/config` import nothing internal today. This plan adds `config -> classifier`, for provider names and provider capabilities only.

## Development Approach
- **testing approach**: Regular (code first, then tests)
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: leave the user's active agrouter config files untouched throughout implementation.** The previous agrouter version is being used to implement this version and needs the existing config format. Do not edit or migrate `~/.config/agrouter/config`, the config under `AGROUTER_CONFIG_DIR`, or any active `.agrouter/config`. Changes to `pkg/config/defaults/config` and synthetic test fixtures remain part of implementation. The user will manually migrate their active configs after implementation is complete.
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
  - tests are not optional - they are a required part of the checklist
  - write unit tests for new functions/methods
  - write unit tests for modified functions/methods
  - add new test cases for new code paths
  - update existing test cases if behavior changes
  - tests cover both success and error scenarios
- **CRITICAL: all tests must pass before starting next task** - no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- run tests after each change (`make test`), lint with `make lint`
- backward compatibility: TypeSafe requests stay byte-identical (router goldens unchanged); the config layout change is an intentional hard break

## Testing Strategy
- **unit tests**: table-driven with testify, required for every task; coverage 80%+ per package
- codec tests use synthetic `classifier.Request` fixtures and `httptest` servers; OpenAI request goldens live in `pkg/classifier/testdata/openai_*.json` with an `-update` flag
- config tests use synthetic INI layers, never the shipped values (keep the one smoke test that embedded config loads and validates)
- tests and development runs requiring the new config format use temporary config directories and an isolated working directory for the local `.agrouter/config`; do not rewrite active configs to make checks pass
- no UI or e2e suite; `make eval-routing` against the real APIs is manual (Post-Completion)

## Progress Tracking
- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope
- keep plan in sync with actual work done

## Solution Overview

```
config                      router                      pkg/classifier
──────                      ──────                      ──────────────
[agrouter]                  builds neutral              Client (shared)
 classifier_provider ──┐    classifier.Request ──Ask──>  retry · backoff · deadline · redact
[classifier.typesafe]  ├─>  cfg.Classifier                      │
[classifier.openai]  ──┘    (the selected one)            wire codec per provider
                                                          ├─ typesafe: /v1/systemone body (today's)
                                                          └─ openai:   /v1/decisions body
```
**Note:** the config picks one provider section, the router builds the same neutral request as today, and only the codec at the bottom knows how each provider wants it on the wire.

Key design decisions:
- **One `Client`, one codec per provider** (chosen over two client types or sub-packages). It avoids duplicating retry and redaction, and the router changes only its limit check.
- **`cfg.Classifier` stays the selected section** and gains a `Provider` field, so `stage.go`, `pool.go`, `complexity.go` and `ResolveAPIKey` need no change.
- **Provider names and capabilities live in `pkg/classifier`** (`ProviderTypeSafe`, `ProviderOpenAI`, `Providers()`, `SupportsLongContext(p)`, `Limits(p, longContext)`). `pkg/config` imports them to validate `classifier_provider` and to reject `long_context` in `[classifier.openai]`.
- **No new reason codes.** OpenAI refusals map to `ErrEmptyAnswer` (`classifier_empty_answer`), too-long 400s map to `ErrTooLong` (`request_too_long`), and 429/503 map to `ErrOverloaded`.

## Technical Details

### Config

```ini
[agrouter]
classifier_provider = typesafe

[classifier.typesafe]
url             = https://api.typesafe.ai/v1/systemone
api_key         =
model           = jev-latest
long_context    = false
request_tokens  = 30000
bytes_per_token = 3

[classifier.openai]
url             = https://api.openai.com/v1/decisions
api_key         =
model           = gpt-6-luna
request_tokens  = 250000
bytes_per_token = 3
```

Luna's default request budget is `250000`, leaving 22000 tokens below the 272K pricing threshold while using its larger context instead of TypeSafe's `30000` budget. [OpenAI documents a 1,050,000-token context window and 128,000 maximum output tokens](https://developers.openai.com/api/docs/models/gpt-6-luna); reserving that output allowance gives the adapter's conservative `922000` input limit. The budget covers the state plus all questions; smaller inputs use only what they need.

For large requests, raise `[agrouter] timeout` (start with `120s` and tune). Inputs above 272K tokens cost 2x the input and cache rates and 1.5x the output rate for the full request; the `250000` default aims to stay below that pricing step. Users can explicitly raise the budget toward the adapter's input limit, less the router's 2000-token reserve. These pricing and context figures come from the model documentation linked above; live Decisions API behavior still needs the checks below.

| Rule | Behavior |
|---|---|
| `classifier_provider` | default `typesafe`; any value outside `Providers()` is a config error listing the valid ones |
| `[classifier.<p>]` with unknown `<p>` | unknown section error |
| `long_context` in a provider that does not support it | unknown key error |
| flat `[classifier]` | error: `[classifier]: removed; use [classifier.<provider>] and [agrouter] classifier_provider` |
| required keys (`url`, `model`) and sizing checks | validated for the **selected** provider; the other section is decoded strictly but its required keys are not enforced |
| api key | `--api-key` > `AGROUTER_CLASSIFIER_API_KEY` > selected section's `api_key` (local > global > embedded) |

### Wire codec (inside `pkg/classifier`)

```go
type wire interface {
    encode(req Request) ([]byte, error)
    decode(data []byte, questions map[string]Question) (Response, error)
    errorType(data []byte) string              // provider error code, normalized (see below)
    setHeaders(h http.Header, longContext bool)
}
```
`StatusError` stays public and unchanged: `Status`, `Body`, `ErrorType`. Each codec's `errorType` normalizes its provider's too-long code to the existing neutral `max_tokens_exceeded` value, so `Is(ErrTooLong)` keeps its rule (400 + that type). `Is(ErrOverloaded)` matches 429, 503 and 529 for every provider. Error values built by hand in `cmd/agrouter/reason_test.go` and `pkg/router/helpers_test.go` keep working.

Redaction: OpenAI gets the state as JSON text inside the `input` string, so an error body that echoes it escapes every state string twice. `stateStrings` adds that doubly escaped form (with and without HTML escaping) next to the raw and once-escaped forms. This applies to every provider, which is harmless for TypeSafe.

### Neutral request to OpenAI mapping

| Neutral (`classifier.Request`) | OpenAI `/v1/decisions` |
|---|---|
| `Model` | `model` |
| `State` (any JSON) | `input`: compact JSON text of the state, as a string. Not `input_json`, which is capped at 256 KiB |
| `Questions` map | `questions` array sorted by id, `name` = id |
| `Instructions` string / object | `instructions`: the string, or compact JSON text of the object |
| `TypeChoice` + `Criteria` | `type: choice`, `choices: [{value: name, description: text}]`. A string value is used as is; an object or array becomes compact JSON text |
| `TypeNoul` + true/false criteria | `type: predicate`. Instructions = the question text plus "true when: ..." and "false when: ..." lines from the criteria |
| `TypeScore` + `Levels` | `type: score`, `levels: [{label: "0".."n-1", description: level}]` |

### OpenAI response to neutral mapping

| OpenAI answer | Neutral `Answer` |
|---|---|
| matched by `name`; missing or `refusal` | `ErrEmptyAnswer` (refusal says "refused" in the error) |
| `choice` (string; a boolean is invalid) | `Choice`, `Probabilities` map from `[{value, probability}]`, `Confidence` |
| `predicate.probability` | `Noul` (probability of "true") |
| `score` | `Score`, `Probabilities` keyed by label `"0".."n-1"`, `Legend` rebuilt from the request's levels, `Confidence` |
| `usage.input_tokens` | `InputTokens` (same permissive reader) |

The same validators (`validateChoice`, `validateDistribution`, `validateScore`, `unit`) run on the converted answer, so both providers share one definition of a valid answer.

### Errors and limits

| | TypeSafe | OpenAI |
|---|---|---|
| too long | 400 + `detail.error_type = max_tokens_exceeded` | 400 + `error.code = context_length_exceeded`, normalized to `max_tokens_exceeded` |
| retried | 429, 503, 529 (shared rule) | 429, 503, 529 (shared rule) |
| `Limits` | 32K/q, 64K/req; 262K with `long_context` | 922K per request (Luna input) |
| extra header | `TypeSafe-Danger-Zone-Long-Context: true` with `long_context` | none |

⚠️ Unverified until a live call: OpenAI's too-long error code, whether `score` is the 0-based expected level, and how probabilities' `value` is indexed. All three are fixed in the codec only. See Post-Completion.

Known limitation: `router.estimate`/`questionLen` size questions in the TypeSafe JSON shape. OpenAI's escaped JSON-in-string encoding is somewhat larger. The `250000` default is well below the adapter's input limit, but the byte estimate does not guarantee that actual usage stays below the 272K pricing threshold. Compare estimated and reported tokens on large requests and tune `bytes_per_token` or lower `request_tokens` if needed; overflow still fails without re-splitting or resending.

## What Goes Where
- **Implementation Steps** (`[ ]` checkboxes): code, tests, embedded defaults, README/CLAUDE.md
- **Post-Completion** (no checkboxes): live API checks and the user's manual migration of their own `.agrouter/config` and global config after implementation; active config migration is outside the implementation agent's scope

## Implementation Steps

### Task 1: Extract the TypeSafe wire codec behind a `wire` interface

**Files:**
- Create: `pkg/classifier/wire.go`
- Create: `pkg/classifier/typesafe.go`
- Create: `pkg/classifier/testdata/typesafe_request.json`
- Create: `pkg/classifier/validate.go` (shared validators), `pkg/classifier/golden_test.go` (`-update` flag and golden helper, reused by Task 2)
- Modify: `pkg/classifier/client.go`
- Modify: `pkg/classifier/types.go`
- Modify: `pkg/classifier/client_test.go`
- Modify: `pkg/router/router.go`, `pkg/router/eval_test.go` and router tests that call `classifier.New`/`Limits`
- Modify: `cmd/agrouter/app.go`, `cmd/agrouter/app_test.go`

- [x] define the `wire` interface and provider constants (`ProviderTypeSafe`, `ProviderOpenAI`), `Providers()`, `SupportsLongContext(p)` in `wire.go`
- [x] move TypeSafe encoding (`Request`/`Question`/`Criteria` marshaling), `decode`, `errorType` and the long-context header into the `typesafe` codec; keep the neutral types in `types.go`
- [x] `StatusError` stays as is; `Is(ErrOverloaded)` matches 429, 503 and 529; `Is(ErrTooLong)` keeps 400 + `max_tokens_exceeded`
- [x] change `New(url, key)` to `New(provider, url, key) (*Client, error)` (an unknown provider is an error) and `Limits(longContext)` to `Limits(provider, longContext)` (TypeSafe values unchanged)
- [x] update every caller in the same task: `cmd/agrouter/app.go` passes `classifier.ProviderTypeSafe` for now, `checkLimits` uses `Limits(classifier.ProviderTypeSafe, ...)` for now, plus router tests and `eval_test.go`
  - ➕ done early so Task 5 only swaps the constant: `app.newClassifier` already takes the provider and returns an error (an unknown provider is a `configError`); `setup` still passes `classifier.ProviderTypeSafe`
  - the `Question`/`Criteria` `MarshalJSON` methods moved to `typesafe.go` but stay methods of the neutral types: the router goldens and `questionLen` marshal them directly in the TypeSafe shape
  - ➕ `ErrUnknownProvider` sentinel (Task 5 maps it to a config error); `Providers()` is derived from the codec table, so `openai` is listed once Task 2 registers its codec; `validate.go` holds the shared validators over `rawAnswer`
- [x] write a wire-body golden test: an `httptest` server records the actual TypeSafe POST body (and long-context header) for a synthetic request; generate it **before** the refactor from the current client so it pins today's bytes
- [x] tests for unknown provider, `Providers()`, `SupportsLongContext`, 503 retried like 429/529
- [x] run `make test` and `go vet -tags eval ./pkg/router`; router goldens and the wire golden must be unchanged

### Task 2: OpenAI codec — request encoding

**Files:**
- Create: `pkg/classifier/openai.go`
- Create: `pkg/classifier/openai_test.go`
- Create: `pkg/classifier/testdata/openai_*.json`

- [x] encode `model`, `input` (compact state JSON text), and `questions` sorted by id with `name` = id
- [x] map choice criteria to `choices [{value, description}]` (a string as is, an object or array as compact JSON)
- [x] map noul to `predicate` with the true/false criteria folded into the instructions
- [x] map score levels to `levels [{label: "0".., description}]`; object instructions become compact JSON text
- [x] golden tests (with `-update`) of the actual HTTP body for choice, noul and score requests built from a synthetic catalog
- [x] error cases: unsupported question type, unmarshalable criterion value
  - ➕ the codec is registered in `providers` with its `Limits` (922000, no long-context mode) already, so the golden tests go through `New(ProviderOpenAI, ...)`; `decode` is a stub returning `ErrInvalidAnswer` and `errorType` returns "" until Task 3
  - ➕ a noul criterion named anything but `true`/`false` is an encode error (it would be dropped silently otherwise); the golden is one request with all three types: `testdata/openai_request.json`
  - ➕ state and instructions text use `SetEscapeHTML(false)`, so the model reads `<` and `&` as they are; Task 3's doubly escaped redaction forms must cover that and the HTML-escaped variant
- [x] run tests - must pass before next task

### Task 3: OpenAI codec — response decoding, errors, limits, redaction

**Files:**
- Modify: `pkg/classifier/openai.go`
- Modify: `pkg/classifier/openai_test.go`
- Modify: `pkg/classifier/client.go` (`stateStrings`)
- Modify: `pkg/classifier/client_test.go`

- [ ] decode `answers[]` by `name` into neutral `Answer`s and run the shared validators
- [ ] refusal and missing answers give `ErrEmptyAnswer`; a boolean choice, unknown name or bad distribution gives `ErrInvalidAnswer`; non-JSON gives `ErrInvalidAnswer`
- [ ] `errorType` reads `error.code` and normalizes `context_length_exceeded` to `max_tokens_exceeded`; no extra headers
- [ ] `Limits(ProviderOpenAI, _)` = 922000 per request and per question; `usage.input_tokens` read as for TypeSafe
- [ ] `stateStrings` adds the doubly escaped form of every state string (with and without HTML escaping)
- [ ] tests via `httptest`: valid choice, predicate and score responses; refusal; duplicate or extra names; too-long 400 matches `ErrTooLong`; 429 retry then 200; 401
- [ ] redaction regression tests: an OpenAI error body echoing the encoded `input` with a quoted, multiline prompt containing `<`, `&` is fully redacted; the TypeSafe echo tests still pass
- [ ] run tests - must pass before next task

### Task 4: Config — `classifier_provider`, `[classifier.<provider>]` sections and embedded defaults

**Files:**
- Modify: `pkg/config/config.go`
- Modify: `pkg/config/load.go`
- Modify: `pkg/config/validate.go`
- Modify: `pkg/config/defaults/config`
- Modify: `pkg/config/config_test.go`, `pkg/config/validate_test.go`, `pkg/config/defaults/defaults_test.go`
- Modify: test configs still using `[classifier]`: `pkg/catalog/catalog_test.go`, `pkg/args/build_test.go`, and any other hit of `grep -rn '\[classifier\]' --include='*_test.go' pkg cmd` (the reviewer named `pkg/readiness/readiness_test.go`)

- [ ] add `Agrouter.ClassifierProvider` (default `typesafe` when no layer sets it) and `Classifier.Provider`
- [ ] decode `[classifier.<p>]` per provider, layered per key as today; unknown `<p>` is an unknown section; `long_context` is an unknown key when `!classifier.SupportsLongContext(p)`
- [ ] the flat `[classifier]` section is an error with the "use `[classifier.<provider>]` and `classifier_provider`" hint
- [ ] after merge, set `cfg.Classifier` to the selected provider's section (with `Provider` set); validate the provider name and the selected section's required and sizing keys; validation messages name `[classifier.<provider>]`
- [ ] embedded defaults in the same task: `classifier_provider = typesafe` with a key reference line under `[agrouter]`; `[classifier]` becomes `[classifier.typesafe]` (today's keys and comments, including `request_tokens = 30000`) and `[classifier.openai]` (`model = gpt-6-luna`, `request_tokens = 250000`, url, api_key, bytes_per_token; no long_context); the old unverified Luna example becomes notes on Luna's 1.05M context, conservative 922K input limit, the default budget's 22K margin below the 272K pricing threshold, 2x input/cache and 1.5x output billing past that threshold, explicitly raising the budget for larger requests, and raising `timeout` (start with `120s`) for big `request_tokens`
- [ ] migrate the test configs in other packages listed above to `[classifier.typesafe]`
- [ ] tests: default provider, switching providers across layers, a local override of one provider key, unknown provider value, unknown provider section, `long_context` under openai, the flat `[classifier]` hint, the unselected section with an empty `url` not failing, `ResolveAPIKey` on the selected section
- [ ] keep the smoke test that the embedded config loads and validates; add one that it also validates with `classifier_provider = openai` (behavior, not values)
- [ ] run `make test` - all packages must pass before next task

### Task 5: Router limits, app wiring, help text and eval harness

**Files:**
- Modify: `pkg/router/router.go`, `pkg/router/question.go` (`budget` error)
- Modify: `pkg/router/router_test.go`
- Modify: `pkg/router/eval_test.go`
- Modify: `cmd/agrouter/app.go`, `cmd/agrouter/flags.go`
- Modify: `cmd/agrouter/app_test.go`, `cmd/agrouter/flags_test.go` (if it pins the help text)

- [ ] `checkLimits` uses `classifier.Limits(c.Provider, c.LongContext)`; it and the `budget` error name `[classifier.<provider>]` and print `long_context` only for providers that support it
- [ ] `newClassifier` takes `cfg.Classifier.Provider`; an unknown provider from `classifier.New` is a config error (exit 2)
- [ ] `--api-key` help (`flags.go`) says the key overrides the selected provider's `[classifier.<provider>] api_key`
- [ ] eval harness loads the embedded, global (`AGROUTER_CONFIG_DIR` or `~/.config/agrouter`) and local layers like the app does, instead of `embedded(t)`, and builds the client from `cfg.Classifier.Provider` with `LongContext` set
- [ ] tests: limit check per provider (over and under), app end-to-end with `classifier_provider = openai` against an `httptest` server answering in OpenAI's shape (decision JSON printed), the TypeSafe path unchanged
- [ ] `go vet -tags eval ./pkg/router` compiles the harness
- [ ] run tests - must pass before next task

### Task 6: Verify acceptance criteria
- [ ] verify `classifier_provider` switches providers and defaults to `typesafe`
- [ ] verify `[classifier.openai]` rejects `long_context` and the flat `[classifier]` gives the migration hint
- [ ] verify the TypeSafe wire golden and router goldens are unchanged (`git diff pkg/router/testdata pkg/classifier/testdata/typesafe_request.json` empty)
- [ ] `grep -rn '\[classifier\]' cmd pkg README.md AGENTS.md CLAUDE.md` finds only the migration hint and its tests
- [ ] run full test suite: `make test`
- [ ] run linters: `make lint`, also with `GOOS=linux`, `GOOS=darwin` and `--build-tags=eval`
- [ ] verify coverage stays 80%+ for `pkg/classifier`, `pkg/config`, `pkg/router`

### Task 7: [Final] Update documentation
- [ ] README: rewrite "Classifier" for `classifier_provider` and the two sections (key table per provider, an OpenAI example, the migration note from `[classifier]`); update "Request size" (long_context is TypeSafe only), the install and general configuration examples, and the ralphex env comment
- [ ] CLAUDE.md and AGENTS.md: `[classifier]` rule becomes `[classifier.<provider>]` plus `classifier_provider`; note the `config -> classifier` import edge in the dependency line and the per-provider codec in `pkg/classifier`
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion
*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Manual verification:**
- after the user manually migrates their active configs, run `make eval-routing` (it now reads the global and local config) with `classifier_provider = typesafe` (no regression) and with `classifier_provider = openai` and an OpenAI key; any earlier live checks must use isolated temporary configs without modifying active configs
- one live OpenAI call each to confirm: the too-long error code (send an oversized request), whether `score` is a 0-based level index, and what the score probabilities' `value` holds; fix the codec only if it differs
- check the refusal path once if a prompt reliably triggers it

**External system updates:**
- the user will manually migrate `~/.config/agrouter/config` (or the config under `AGROUTER_CONFIG_DIR`) and any active `.agrouter/config` from `[classifier]` to `[classifier.<provider>]` after implementation, when switching to the new build; the implementation agent must leave these files untouched so the previous version remains usable
- ralphex configs need no change (same env var and flags)

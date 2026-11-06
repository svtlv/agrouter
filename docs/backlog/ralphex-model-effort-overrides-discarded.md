---
worth: yes
where: pkg/args/native.go:14
added: 2026-11-06
---
# Honor native model and effort when agrouter options do not specify them

Ralphex can select a model and effort per phase using `task_model`, `review_model`
and `plan_model`, but these selections are discarded when its Claude command is
agrouter. Ralphex parses `model[:effort]` itself and appends `--model` and
`--effort` after the configured arguments. The documented integration puts those
arguments after agrouter's `--`; `nativeOptions` removes them and the classifier
chooses replacements. Observed with ralphex v1.7.0-windows.3 and agrouter v0.5.0:
`review_model = opus:medium` still ran Opus at xhigh effort.

The temporary workaround pins `--model opus --effort medium` before agrouter's
separator. It cannot express different selections for different ralphex phases.

## Proposed approach

In exec mode, take each model/effort value from agrouter's options before `--`
when present; otherwise take that field from native options after `--`; otherwise
let the classifier choose it. Import native `--model`/`-m` and `--effort` as caller
constraints by default, with no opt-in flag. Apply precedence independently per
field, not to the model/effort pair as a whole. This is the agreed direction for
the backlog item, not implemented behavior.

Ralphex remains responsible for reading its config, resolving phase fallbacks,
and parsing the colon syntax. Agrouter consumes the flags it already receives;
it does not read `.ralphex/config` or introduce another `model:effort` parser.

| Ralphex Claude setting | Flags emitted by ralphex | Behavior when no agrouter model/effort is supplied before `--` |
| --- | --- | --- |
| `opus:medium` | `--model opus --effort medium` | Fix both values |
| `opus:` or `opus` | `--model opus` | Fix model; classify effort |
| `:medium` | `--effort medium` | Classify model within the exec CLI; fix effort |
| `:` | Neither | Classify model and effort |

An empty ralphex setting retains ralphex's existing fallback rules; it does not
necessarily mean unconstrained routing. In Claude review mode, an empty
`review_model` falls back to `task_model`, whereas `review_model = :` emits
neither constraint.

Example after implementation, with the temporary fixed agrouter model/effort
removed:

```ini
claude_command = agrouter
claude_args = exec claude --doc=AGENTS.md -- --dangerously-skip-permissions --output-format stream-json --verbose
task_model = opus:
review_model = opus:medium
```

## Semantics and scope

- Import selections before catalog filtering, readiness evaluation, routing and
  fallback handling, so the reported selection matches the actual child argv.
  Resolve aliases and out-of-catalog values through the existing constraint path.
- Resolve model and effort independently: an explicit agrouter option before
  `--` wins for that field; otherwise use the inherited native value; otherwise
  let the classifier choose. Track explicit presence, including an empty value,
  rather than treating every empty string as an absent option. An explicit empty
  value before `--` leaves that field unconstrained and blocks native inheritance.
  For example, `agrouter exec claude --model opus -- --model sonnet --effort medium`
  fixes Opus and medium: the model before the separator wins, while the effort
  comes from after it. With only `--effort high` before the separator and
  `--model opus --effort medium` after it, the result is Opus and high.
- Recognize the reserved spellings already handled by `nativeOptions`, including
  attached and equals forms. Repeated native selections use the
  last occurrence per field; a supplied empty value leaves that field unconstrained.
  Reject missing values with usage exit 2 rather than silently routing around them.
- Stop scanning at the child's own `--`. Never interpret positional text, prompt
  contents, native settings files, profiles, or arbitrary `-c` expressions as
  constraints. Keep unrelated native arguments and stdin unchanged.
- Remove consumed native selection flags and emit one final selection through
  the existing invocation descriptors. Preserve fixed values on classifier
  failure and never broaden them to evade a quota/readiness block.
- Start with the native model/effort flags used by the Claude integration. Codex
  `-c model_reasoning_effort=...` translation is separate work; do not imply that
  native inheritance makes every Codex config expression a routing constraint.
- Update `--help`, README invocation and ralphex examples, and `AGENTS.md` rules
  that currently state native selections never constrain routing. Document this
  behavior change for existing exec callers: native selections previously ignored
  now constrain routing whenever the corresponding agrouter option is absent.

Likely implementation points: `cmd/agrouter/flags.go`, `pkg/args/request.go`,
`pkg/args/native.go`, and the request normalization before routing in
`cmd/agrouter/app.go`. Reuse `pkg/router/eligibility.go` and the existing readiness
and fallback paths rather than adding a second selection pipeline.

## Acceptance checks

Use synthetic catalogs, fake CLIs and a fake classifier; no live AI calls.
Cover the four setting shapes above using the argv ralphex emits, different
task/review selections, per-field precedence, aliases, out-of-catalog values,
repeated/empty/missing values, flag spellings, and the child's separator. Assert
that only unconstrained routing stages are classified, inherited constraints
reach readiness and survive classifier failure, and the selection log agrees
with the child's single model/effort selection. Cover mixed-source model/effort
pairs, conflicting values, and explicit empty agrouter values. Verify that exec
calls with no selections anywhere still classify both fields and that decision
mode retains its existing rejection of `--`.

## Alternatives considered

An opt-in import flag would preserve existing behavior but adds configuration to
every integration. Prefer automatic per-field fallback from before `--` to after
it, as requested by the user. A ralphex wrapper could move flags before the
separator without changing agrouter, but duplicates argument handling in each
integration.

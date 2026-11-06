---
worth: yes
where: pkg/args/native.go:14
added: 2026-11-06
---
# Preserve native arguments after the separator without interpreting selections

Ralphex selects a model and effort per phase through `task_model`, `review_model`
and `plan_model`. It parses `model[:effort]` and appends native flags after its
configured arguments. The documented agrouter integration puts those flags after
`--`, but `nativeOptions` strips model/effort selections and replaces them with
agrouter's choices. Observed with ralphex v1.7.0-windows.3 and agrouter v0.5.0:
`review_model = opus:medium` still ran Opus at xhigh effort.

The temporary workaround pins `--model opus --effort medium` before agrouter's
separator. It cannot express different selections for different ralphex phases.

## Agreed direction

Everything after agrouter's first `--` is an opaque native argv sequence. Pass
those tokens to the chosen CLI exactly as supplied: preserve values, spelling,
order, duplicates, empty arguments, and later separators. Do not validate,
normalize, map aliases, strip flags, or extract model/effort routing constraints
from that sequence. The target CLI owns validation and option precedence.

In exec mode, remove agrouter-owned `--model`/`-m` and `--effort` before `--`.
There is no opt-in flag or second place to configure model/effort overrides.
Keep routing options such as `--doc` before the separator. Reject the removed
exec options there with a migration hint to move them after `--`.

Agrouter still selects its CLI-specific model/effort defaults through routing.
Emit those generated defaults before the untouched native arguments, so native
selections can override them according to the target CLI's own rules. Do not
promise universal last-option-wins behavior: confirm duplicate-option handling
for supported CLIs, and delegate unsupported or conflicting combinations to
those CLIs rather than adding translation logic.

Conceptual argv order:

```text
command + exec_args + generated model_args + generated effort_args
        + native argv unchanged + configured prompt_args
```

Agrouter's existing invocation descriptors still generate its own defaults.
The no-mapping rule applies to the caller's native arguments. Preserve prompt
capture and stdin replay. Retain the existing duplicate-separator/prompt assembly
guard as an invocation-assembly check, not as validation of native option values.
The stdin-only ralphex integration must pass through without rewriting.

## Ralphex behavior

Ralphex remains responsible for config loading, phase fallback and colon parsing;
agrouter never reads `.ralphex/config` or parses `model:effort`.

| Ralphex Claude setting | Native flags forwarded unchanged | Intended result under CLI override rules |
| --- | --- | --- |
| `opus:medium` | `--model opus --effort medium` | Override routed model and effort |
| `opus:` or `opus` | `--model opus` | Override model; retain routed effort |
| `:medium` | `--effort medium` | Retain routed model; override effort |
| `:` | Neither | Use routed model and effort |

Routing does not inspect which fields the native arguments override, so it may
classify both model and effort even when the caller supplies both. In particular,
a model-only override retains an effort chosen for the routed model, not an
effort classified specifically for the native model. The target CLI validates
that combination.

An empty ralphex setting retains ralphex's fallback rules: an empty Claude
`review_model` falls back to `task_model`, whereas `review_model = :` emits
neither override.

Example after implementation, with the temporary fixed agrouter flags removed:

```ini
claude_command = agrouter
claude_args = exec claude --doc=AGENTS.md -- --dangerously-skip-permissions --output-format stream-json --verbose
task_model = opus:
review_model = opus:medium
```

## Implementation and reporting

Remove native selection filtering in `pkg/args/native.go` and reorder assembly
in `pkg/args/build.go`. Update exec option parsing in `cmd/agrouter/flags.go`,
request comments, help, README examples, and `AGENTS.md` rules that currently
reserve and replace native model/effort flags.

Native Codex `-c` arguments, unknown models, aliases, malformed values, and
repeated flags all pass through unchanged without adding CLI-specific mappings.
Generated defaults must precede the entire native sequence, including a child's
own `--`, without splitting or reordering that sequence.

Selection logs describe agrouter's routed defaults, not a verified final model
or effort after native overrides. Review effective-selection and fallback
reporting in `cmd/agrouter/fallback.go` so it does not claim otherwise. Readiness
probes still run in agrouter's configured context, but must not be presented as
validation of opaque native overrides. Do not reverse-engineer native options to
restore such a claim. Native overrides remain in argv even if routing falls back
to child defaults.

This item defines the exec interface. Decision mode has no native separator;
address its model/effort interface explicitly during implementation rather than
accidentally changing it through the shared parser.

## Acceptance checks

Use synthetic catalogs, a fake classifier and fake CLIs that record exact argv
and stdin; no live inference. Verify that generated defaults precede the native
sequence, every native token is preserved, aliases remain aliases, and no native
selection constrains routing or skips classifier stages. Cover arbitrary flags,
empty tokens, repeated/attached/equals forms, missing values, later separators,
Codex config expressions, classifier failure and stdin replay. Invalid native
arguments reach the fake child; agrouter does not reject or repair them. The
child's exit code remains authoritative.

Cover the ralphex argv shapes above, different task/review settings, rejection
of removed model/effort options before `--`, and reporting that distinguishes
routed defaults from an unknown effective child selection. Confirm supported
CLIs' actual duplicate-option behavior without running paid inference.

## Alternatives considered

Importing native model/effort into routing requires parsing, validation and alias
resolution of caller arguments. An opt-in flag adds configuration; accepting
selections both before and after `--` adds precedence rules. Prefer unchanged
native passthrough and target-CLI-owned overrides, as requested by the user.

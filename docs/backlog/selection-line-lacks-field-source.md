---
worth: yes
where: cmd/agrouter/fallback.go:100
added: 2026-11-19
---
# a fully populated selection line does not say who fixed each field

The exec selection line carries `effective.<field>.source` (`router`, `native`, `verified_child_default`,
`child_default_unknown`) only when a model or effort is left to the child (`s.Model == nil ||
s.Effort == nil`). When both are set, a caller cannot tell a classifier choice from a value that a
native argument fixed (for example ralphex's `--model opus --effort high` from `task_model =
opus:high`). `confidence` is a weak hint: it is missing for natively fixed fields but also for "the one
option left".

autopilot reports for every step who chose the model and effort. Today it rebuilds this from the
ralphex config (`plan_model`, `task_model`, `review_model`, `codex_model`, `codex_reasoning_effort`, run
flags), which goes wrong when another caller or a wrapper adds native arguments.

Fix: always emit `effective` (or a small `source: {"model": ..., "effort": ...}` object) in the selection
line and in decision JSON, with the existing source codes. Additive JSON field; update the README table
under "Exec fallback" and the selection-line tests.

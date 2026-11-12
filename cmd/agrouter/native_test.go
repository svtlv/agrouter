package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/svtlv/agrouter/pkg/config"
	"github.com/svtlv/agrouter/pkg/config/defaults"
)

// shapeCLIs disables the shipped CLIs and defines two made-up ones that spell their model and effort
// like the two real executors ralphex drives: alpha takes "--model X" and "--effort Y" (alpha models
// fast and deep, deep aliased "strong"), beta takes -c model="X" and -c model_reasoning_effort="Y"
// (models other and spare). Both offer the efforts low, medium and high.
func shapeCLIs(t *testing.T) string {
	t.Helper()
	cfg, err := config.Load(config.Sources{Embedded: defaults.Config})
	require.NoError(t, err)
	var disabled strings.Builder
	for _, cli := range cfg.CLIs {
		fmt.Fprintf(&disabled, "[cli.%s]\nenabled = false\n", cli.Name)
	}
	var clis strings.Builder
	cliTemplate := `
[cli.NAME]
command     = COMMAND
description = A coding agent that exists only in this test.
exec_args   = EXEC
model_args  = MODEL
effort_args = EFFORT
prompt_args = ["--", "{prompt}"]
`
	clis.WriteString(strings.NewReplacer("NAME", "alpha", "COMMAND", helperCommand(t), "EXEC", `["-p"]`,
		"MODEL", `["--model", "{model}"]`, "EFFORT", `["--effort", "{effort}"]`).Replace(cliTemplate))
	clis.WriteString(strings.NewReplacer("NAME", "beta", "COMMAND", helperCommand(t), "EXEC", `["exec"]`,
		"MODEL", `["-c", "model=\"{model}\""]`, "EFFORT", `["-c", "model_reasoning_effort=\"{effort}\""]`).Replace(cliTemplate))
	for _, cli := range []string{"alpha", "beta"} {
		for _, effort := range []string{"low", "medium", "high"} {
			fmt.Fprintf(&clis, "\n[effort.%s.%s]\ndescription = The %s effort.\n", cli, effort, effort)
		}
	}
	return disabled.String() + clis.String() + `
[model.fast]
cli         = alpha
name        = fast-1
efforts     = low, medium, high
description = Small and fast.

[model.deep]
cli         = alpha
name        = deep-1
aliases     = strong
efforts     = low, medium, high
description = Large and thorough.

[model.other]
cli         = beta
name        = other-1
efforts     = low, medium, high
description = The beta CLI's first model.

[model.spare]
cli         = beta
name        = spare-1
efforts     = low, medium, high
description = The beta CLI's second model.
`
}

// newShapeEnv is newEnv with shapeCLIs as the global config.
func newShapeEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.globalConfig(shapeCLIs(t))
	return e
}

// alphaNative is the argv ralphex hands a Claude-like executor after "--": its own flags around the
// phase's model and effort flags, ending with --print (the prompt comes on stdin).
func alphaNative(selection ...string) []string {
	return slices.Concat([]string{"--dangerously-skip-permissions", "--output-format", "stream-json", "--verbose"},
		selection, []string{"--print"})
}

// betaNative is the argv ralphex hands a Codex-like executor: config overrides, sandbox flags, the
// phase's model and effort flags, then the timeout and project doc overrides.
func betaNative(selection ...string) []string {
	pre := []string{
		"-c", "features.multi_agent=true",
		"-c", `agents.reviewer.description="general code review specialist"`,
		"-c", `project_doc_fallback_filenames=["CLAUDE.md"]`,
		"--dangerously-bypass-approvals-and-sandbox",
		"--sandbox", "danger-full-access",
	}
	post := []string{"-c", "stream_idle_timeout_ms=3600000", "-c", `project_doc="/tmp/doc.md"`}
	return slices.Concat(pre, selection, post)
}

func TestApp_RalphexShapes(t *testing.T) {
	const task = "implement task 3\nof the plan\x00\r\n"
	tests := []struct {
		name      string
		cli       string
		native    []string // the native argv after "--"
		generated []string // what agrouter adds before the native argv
		asked     []string // the stages the classifier is asked
		pick      string
		model     string
		effort    string
	}{
		// "opus:medium": both fields set natively
		{name: "alpha model and effort", cli: "alpha", native: alphaNative("--model", "strong", "--effort", "medium"),
			model: "deep-1", effort: "medium"},
		{name: "alpha model and effort, equals forms", cli: "alpha", native: alphaNative("--model=strong", "--effort=medium"),
			model: "deep-1", effort: "medium"},
		{name: "beta model and effort, ralphex quoting", cli: "beta",
			native: betaNative("-c", `model="other-1"`, "-c", "model_reasoning_effort=medium"),
			model:  "other-1", effort: "medium"},
		{name: "beta model and effort, both quoted", cli: "beta",
			native: betaNative("-c", `model="other-1"`, "-c", `model_reasoning_effort="medium"`),
			model:  "other-1", effort: "medium"},

		// "opus:": only the model is set, the effort is routed
		{name: "alpha model only", cli: "alpha", native: alphaNative("--model", "strong"),
			generated: []string{"--effort", "low"}, asked: []string{"effort"}, pick: "deep@low", model: "deep-1", effort: "low"},
		{name: "beta model only", cli: "beta", native: betaNative("-c", `model="other-1"`),
			generated: []string{"-c", `model_reasoning_effort="low"`}, asked: []string{"effort"}, pick: "other@low",
			model: "other-1", effort: "low"},

		// ":medium": only the effort is set, the model is routed
		{name: "alpha effort only", cli: "alpha", native: alphaNative("--effort", "medium"),
			generated: []string{"--model", "deep-1"}, asked: []string{"model"}, pick: "deep@medium", model: "deep-1", effort: "medium"},
		{name: "beta effort only", cli: "beta", native: betaNative("-c", "model_reasoning_effort=medium"),
			generated: []string{"-c", `model="spare-1"`}, asked: []string{"model"}, pick: "spare@medium", model: "spare-1", effort: "medium"},

		// ":": nothing is set, both are routed
		{name: "alpha nothing set", cli: "alpha", native: alphaNative(),
			generated: []string{"--model", "deep-1", "--effort", "low"}, asked: []string{"model", "effort"}, pick: "deep@low",
			model: "deep-1", effort: "low"},
		{name: "beta nothing set", cli: "beta", native: betaNative(),
			generated: []string{"-c", `model="spare-1"`, "-c", `model_reasoning_effort="high"`}, asked: []string{"model", "effort"},
			pick: "spare@high", model: "spare-1", effort: "high"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newShapeEnv(t)
			e.jev.pick = tc.pick
			r := e.run(slices.Concat([]string{"exec", tc.cli, "--"}, tc.native), strings.NewReader(task))

			require.Equal(t, exitOK, r.code, r.stderr)
			assert.Empty(t, r.stdout)
			assert.Subset(t, selectionLog(t, r.stderr), map[string]any{"cli": tc.cli, "model": tc.model, "effort": tc.effort})
			argv, stdin, _ := e.child()
			execArgs := map[string][]string{"alpha": {"-p"}, "beta": {"exec"}}[tc.cli]
			assert.Equal(t, slices.Concat(execArgs, tc.generated, tc.native), argv,
				"generated arguments first, then the native argv byte for byte, no prompt argument")
			assert.Equal(t, task, string(stdin))
			assert.Equal(t, tc.asked, e.jev.asked())
			assert.Len(t, e.jev.requests(), len(tc.asked), "one request per asked stage")
		})
	}
}

func TestApp_RalphexPhases(t *testing.T) {
	// a task phase with "opus:" and a review phase with "opus:medium" differ only in the effort
	e := newShapeEnv(t)
	e.jev.pick = "deep@high"

	r := e.run(slices.Concat([]string{"exec", "alpha", "--"}, alphaNative("--model", "strong")), strings.NewReader("task phase"))
	require.Equal(t, exitOK, r.code, r.stderr)
	taskArgv, _, _ := e.child()
	assert.Equal(t, slices.Concat([]string{"-p", "--effort", "high"}, alphaNative("--model", "strong")), taskArgv)
	assert.Equal(t, []string{"effort"}, e.jev.asked())

	r = e.run(slices.Concat([]string{"exec", "alpha", "--"}, alphaNative("--model", "strong", "--effort", "medium")), strings.NewReader("review phase"))
	require.Equal(t, exitOK, r.code, r.stderr)
	reviewArgv, stdin, _ := e.child()
	assert.Equal(t, slices.Concat([]string{"-p"}, alphaNative("--model", "strong", "--effort", "medium")), reviewArgv)
	assert.Equal(t, "review phase", string(stdin))
	assert.Equal(t, []string{"effort"}, e.jev.asked(), "the review phase asked nothing more")
	assert.NotEqual(t, taskArgv, reviewArgv)
}

func TestApp_ExecWithoutProvider(t *testing.T) {
	t.Run("a native model implies its CLI and skips the cli and model stages", func(t *testing.T) {
		e := newShapeEnv(t)
		e.jev.pick = "deep@low"
		r := e.run([]string{"exec", "fix", "--", "--model", "strong"}, nil)

		require.Equal(t, exitOK, r.code, r.stderr)
		assert.Subset(t, selectionLog(t, r.stderr), map[string]any{"cli": "alpha", "model": "deep-1", "effort": "low"})
		argv, _, _ := e.child()
		assert.Equal(t, []string{"-p", "--effort", "low", "--model", "strong", "--", "fix"}, argv)
		assert.Equal(t, []string{"effort"}, e.jev.asked())
	})

	t.Run("a native model of the other CLI implies that CLI", func(t *testing.T) {
		e := newShapeEnv(t)
		e.jev.pick = "other@medium"
		r := e.run([]string{"exec", "fix", "--", "-c", `model="other-1"`}, nil)

		require.Equal(t, exitOK, r.code, r.stderr)
		argv, _, _ := e.child()
		assert.Equal(t, []string{"exec", "-c", `model_reasoning_effort="medium"`, "-c", `model="other-1"`, "--", "fix"}, argv)
		assert.Equal(t, []string{"effort"}, e.jev.asked())
	})

	t.Run("no native selection: all three stages, the native argv goes to the winner", func(t *testing.T) {
		e := newShapeEnv(t)
		e.jev.pick = "spare@low"
		r := e.run([]string{"exec", "fix", "--", "--verbose"}, nil)

		require.Equal(t, exitOK, r.code, r.stderr)
		assert.Subset(t, selectionLog(t, r.stderr), map[string]any{"cli": "beta", "model": "spare-1", "effort": "low"})
		argv, _, _ := e.child()
		assert.Equal(t, []string{"exec", "-c", `model="spare-1"`, "-c", `model_reasoning_effort="low"`, "--verbose", "--", "fix"}, argv)
		assert.Equal(t, []string{"cli", "model", "effort"}, e.jev.asked())
	})

	t.Run("classifier failure with two CLIs left is exit 2 and starts nothing", func(t *testing.T) {
		e := newShapeEnv(t)
		e.jev.failStage = "cli"
		r := e.run([]string{"exec", "fix", "--", "--verbose"}, nil)

		assert.Equal(t, 2, r.code, r.stderr)
		assert.Empty(t, r.stdout)
		assert.Contains(t, r.stderr, "agrouter:")
		assert.NoFileExists(t, filepath.Join(e.out, "argv.json"), "the child must not run")
	})

	t.Run("stdin alone: replayed byte for byte, no prompt arguments", func(t *testing.T) {
		const stdin = "prompt on stdin\x00\r\n\xff"
		e := newShapeEnv(t)
		r := e.run([]string{"exec", "--", "--model", "strong", "--effort", "medium"}, strings.NewReader(stdin))

		require.Equal(t, exitOK, r.code, r.stderr)
		argv, got, _ := e.child()
		assert.Equal(t, []string{"-p", "--model", "strong", "--effort", "medium"}, argv)
		assert.Equal(t, stdin, string(got))
		assert.Empty(t, e.jev.requests())
	})
}

func TestApp_ExecStdinWithNativeArgs(t *testing.T) {
	const stdin = "line one\r\n\x00 line two\n"
	e := newShapeEnv(t)
	e.jev.pick = "fast@low"
	r := e.run([]string{"exec", "alpha", "--", "--effort", "high", "--json"}, strings.NewReader(stdin))

	require.Equal(t, exitOK, r.code, r.stderr)
	argv, got, _ := e.child()
	assert.Equal(t, []string{"-p", "--model", "fast-1", "--effort", "high", "--json"}, argv, "no prompt_args with the prompt on stdin")
	assert.Equal(t, stdin, string(got))
	assert.Equal(t, []string{"model"}, e.jev.asked())
}

func TestApp_ExecInvalidNativeArgs(t *testing.T) {
	// the native arguments are not validated by agrouter: the child sees them as given and its exit
	// code is agrouter's
	tests := []struct {
		name   string
		native []string
		want   []string // the argv after "-p"
		asked  []string
	}{
		{name: "unknown flag", native: []string{"--no-such-flag", "value"},
			want: []string{"--model", "fast-1", "--effort", "low", "--no-such-flag", "value"}, asked: []string{"model", "effort"}},
		{name: "model flag without its value", native: []string{"--model"},
			want: []string{"--model", "fast-1", "--effort", "low", "--model"}, asked: []string{"model", "effort"}},
		{name: "unknown model", native: []string{"--model", "no-such-model"},
			want: []string{"--model", "no-such-model"}},
		{name: "unknown effort", native: []string{"--effort", "no-such-effort"},
			want: []string{"--model", "fast-1", "--effort", "no-such-effort"}, asked: []string{"model"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newShapeEnv(t)
			e.jev.pick = "fast@low"
			t.Setenv(helperExit, "3")
			r := e.run(slices.Concat([]string{"exec", "alpha", "--"}, tc.native), strings.NewReader("prompt"))

			assert.Equal(t, 3, r.code, r.stderr)
			argv, stdin, _ := e.child()
			assert.Equal(t, slices.Concat([]string{"-p"}, tc.want), argv)
			assert.Equal(t, "prompt", string(stdin))
			assert.Equal(t, tc.asked, e.jev.asked())
		})
	}
}

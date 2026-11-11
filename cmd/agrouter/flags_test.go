package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/svtlv/agrouter/pkg/args"
)

func parseOK(t *testing.T, argv []string) *args.Request {
	t.Helper()
	cmd, err := parseArgs(argv)
	require.NoError(t, err)
	require.NotNil(t, cmd.req)
	return cmd.req
}

// parseResolved parses argv and resolves the exec operand and --cli against the enabled synthetic CLIs
// tool-a and tool-b, as run does once the config is loaded.
func parseResolved(t *testing.T, argv []string) (*args.Request, error) {
	t.Helper()
	cmd, err := parseArgs(argv)
	require.NoError(t, err)
	err = resolveExec(cmd, func(name string) bool { return name == "tool-a" || name == "tool-b" })
	return cmd.req, err
}

func positional(v string) args.Optional { return args.Optional{Value: v, Set: true} }

func TestParseArgs_FlagsAroundPositional(t *testing.T) {
	tests := []struct {
		name string
		argv []string
	}{
		{"prompt first", []string{"fix it", "--verbose", "--doc=a.md"}},
		{"prompt last, = form", []string{"--doc=a.md", "--verbose", "fix it"}},
		{"prompt in the middle", []string{"--verbose", "fix it", "--doc", "a.md"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := parseArgs(tc.argv)
			require.NoError(t, err)
			assert.Equal(t, args.ModeDecision, cmd.req.Mode)
			assert.Equal(t, positional("fix it"), cmd.req.Prompt)
			assert.Equal(t, []string{"a.md"}, cmd.req.Docs)
			assert.Empty(t, cmd.req.Raw)
		})
	}
}

// TestParseArgs_RemovedModelEffort covers every spelling of the removed options in both modes: a fixed
// usage error with the hint, before any config is read.
func TestParseArgs_RemovedModelEffort(t *testing.T) {
	tests := []struct {
		name     string
		argv     []string
		wantHint string
	}{
		{"--model split", []string{"--model", "m", "x"}, "--model/-m is not an agrouter option"},
		{"--model =", []string{"--model=m", "x"}, "--model/-m is not an agrouter option"},
		{"--model empty", []string{"--model=", "x"}, "--model/-m is not an agrouter option"},
		{"-m split", []string{"-m", "m", "x"}, "--model/-m is not an agrouter option"},
		{"-m attached", []string{"-mm", "x"}, "--model/-m is not an agrouter option"},
		{"--effort split", []string{"--effort", "high", "x"}, "--effort is not an agrouter option"},
		{"--effort =", []string{"--effort=high", "x"}, "--effort is not an agrouter option"},
		{"--effort after the prompt", []string{"x", "--effort=high"}, "--effort is not an agrouter option"},
		{"exec --model", []string{"exec", "tool-a", "--model", "m", "x"}, "--model/-m is not an agrouter option"},
		{"exec -m without CLI", []string{"exec", "-m", "m", "x"}, "--model/-m is not an agrouter option"},
		{"exec --effort", []string{"exec", "tool-a", "--effort", "high", "--", "--verbose"}, "--effort is not an agrouter option"},
		{"model wins over effort", []string{"--effort=high", "--model=m", "x"}, "--model/-m is not an agrouter option"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseArgs(tc.argv)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantHint)
			assert.Contains(t, err.Error(), `after "--"`)
			assert.NotContains(t, err.Error(), "\n")
		})
	}

	t.Run("after the child's separator they are native tokens", func(t *testing.T) {
		cmd, err := parseArgs([]string{"exec", "tool-a", "x", "--", "--model", "m", "-m", "n", "--effort", "high"})
		require.NoError(t, err)
		assert.Equal(t, []string{"--model", "m", "-m", "n", "--effort", "high"}, cmd.req.Raw)
	})
}

func TestParseArgs_Mode(t *testing.T) {
	tests := []struct {
		name         string
		argv         []string
		wantMode     args.Mode
		wantOperand  string
		wantPrompt   args.Optional
		wantNoPrompt bool
	}{
		{"decision, no prompt", []string{"--verbose"}, args.ModeDecision, "", args.Optional{}, true},
		{"decision with prompt", []string{"hello"}, args.ModeDecision, "", positional("hello"), false},
		{"exec operand and prompt", []string{"exec", "tool-a", "hello"}, args.ModeExec, "tool-a", positional("hello"), false},
		{"exec operand alone", []string{"exec", "tool-a"}, args.ModeExec, "tool-a", args.Optional{}, true},
		{"exec with no operand", []string{"exec"}, args.ModeExec, "", args.Optional{}, true},
		{"exec option first", []string{"exec", "-p", "x"}, args.ModeExec, "", args.Optional{}, true},
		{"exec separator first", []string{"exec", "--", "x"}, args.ModeExec, "", args.Optional{}, true},
		{"later exec is the positional", []string{"--verbose", "exec"}, args.ModeDecision, "", positional("exec"), false},
		{"exec as the operand", []string{"exec", "exec", "x"}, args.ModeExec, "exec", positional("x"), false},
		{"empty positional is still a prompt", []string{""}, args.ModeDecision, "", positional(""), false},
		{"exec empty first token is a positional", []string{"exec", "", "--"}, args.ModeExec, "", positional(""), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := parseArgs(tc.argv)
			require.NoError(t, err)
			assert.Equal(t, tc.wantMode, cmd.req.Mode)
			assert.Equal(t, tc.wantOperand, cmd.operand)
			assert.Empty(t, cmd.req.CLI, "the CLI is resolved against the config later")
			assert.Equal(t, tc.wantPrompt, cmd.req.Prompt)
			assert.Equal(t, tc.wantNoPrompt, !cmd.req.Prompt.Set)
		})
	}
}

// TestParseArgs_DocumentedExamples parses the invocation shapes README and --help document, with
// synthetic CLI names: the parser never reads the catalog.
func TestParseArgs_DocumentedExamples(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want args.Request
	}{
		{"decision with -p", []string{"-p", "fix the flaky test"}, args.Request{
			Mode: args.ModeDecision, PromptFlag: positional("fix the flaky test")}},
		{"decision with docs", []string{"--doc", "AGENTS.md", "--doc", "docs/architecture.md", "-p", "refactor"}, args.Request{
			Mode: args.ModeDecision, PromptFlag: positional("refactor"), Docs: []string{"AGENTS.md", "docs/architecture.md"}}},
		{"exec with stdin prompt and native args", []string{"exec", "tool-a", "--", "--yes", "--format", "stream", "--verbose"}, args.Request{
			Mode: args.ModeExec, CLI: "tool-a", Raw: []string{"--yes", "--format", "stream", "--verbose"}}},
		{"caller args ending in -- with an appended native flag", []string{"exec", "tool-a", "--doc=AGENTS.md", "--", "--yes", "--print"}, args.Request{
			Mode: args.ModeExec, CLI: "tool-a", Docs: []string{"AGENTS.md"}, Raw: []string{"--yes", "--print"}}},
		{"wrapper forwarding its arguments after --", []string{"exec", "tool-b", "--doc=AGENTS.md", "--", "--json", "-"}, args.Request{
			Mode: args.ModeExec, CLI: "tool-b", Docs: []string{"AGENTS.md"}, Raw: []string{"--json", "-"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := parseResolved(t, tc.argv)
			require.NoError(t, err)
			assert.Equal(t, tc.want, *req)
		})
	}
}

// TestResolveExec covers the exec command-line table: the operand is the CLI only when it names an
// enabled one, otherwise the prompt; --cli pins too, and the two must agree.
func TestResolveExec(t *testing.T) {
	tests := []struct {
		name       string
		argv       []string
		wantCLI    string
		wantPrompt args.Optional
		wantErr    string
	}{
		{"operand pins", []string{"exec", "tool-a", "fix", "--", "--yes"}, "tool-a", positional("fix"), ""},
		{"--cli pins", []string{"exec", "--cli", "tool-b", "fix"}, "tool-b", positional("fix"), ""},
		{"--cli with a prompt operand", []string{"exec", "fix bug", "--cli=tool-b", "--", "--yes"}, "tool-b", positional("fix bug"), ""},
		{"redundant --cli accepted", []string{"exec", "tool-a", "--cli=tool-a", "x"}, "tool-a", positional("x"), ""},
		{"operand that is no CLI is the prompt", []string{"exec", "fix bug", "--", "--verbose"}, "", positional("fix bug"), ""},
		{"no operand, stdin prompt", []string{"exec", "--doc=AGENTS.md", "--", "--verbose"}, "", args.Optional{}, ""},
		{"empty operand token", []string{"exec", ""}, "", positional(""), ""},
		{"operand and --cli disagree", []string{"exec", "tool-a", "--cli", "tool-b", "x"}, "", args.Optional{}, "--cli tool-b conflicts with exec tool-a"},
		{"empty --cli conflicts", []string{"exec", "tool-a", "--cli=", "x"}, "", args.Optional{}, "conflicts with exec tool-a"},
		{"unknown --cli", []string{"exec", "--cli=nope", "x"}, "", args.Optional{}, "exec --cli nope: not a configured, enabled CLI"},
		{"prompt operand and a second positional", []string{"exec", "fix bug", "more"}, "", args.Optional{}, "more than one positional"},
		{"empty --cli is no pin", []string{"exec", "--cli=", "x"}, "", positional("x"), ""},
		{"--cli in decision mode restricts routing", []string{"--cli", "tool-a", "x"}, "tool-a", positional("x"), ""},
		{"the last --cli wins, even empty", []string{"--cli=tool-a", "--cli=", "x"}, "", positional("x"), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := parseResolved(t, tc.argv)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.NotContains(t, err.Error(), "\n")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantCLI, req.CLI)
			assert.Equal(t, tc.wantPrompt, req.Prompt)
		})
	}
}

func TestResolveExec_PromptFlagKeepsCLIWord(t *testing.T) {
	cmd, err := parseArgs([]string{"exec", "-p", "tool-a"})
	require.NoError(t, err)
	require.NoError(t, resolveExec(cmd, func(string) bool { return true }))
	assert.Empty(t, cmd.req.CLI, "a word after -p is the prompt, not the CLI")
	assert.Equal(t, positional("tool-a"), cmd.req.PromptFlag)
}

func TestParseArgs_OwnFlags(t *testing.T) {
	t.Run("--verbose before -- belongs only to agrouter", func(t *testing.T) {
		cmd, err := parseArgs([]string{"--verbose", "x"})
		require.NoError(t, err)
		assert.True(t, cmd.verbose)
		cmd, err = parseArgs([]string{"exec", "tool-a", "x", "--", "--verbose"})
		require.NoError(t, err)
		assert.False(t, cmd.verbose)
		assert.Equal(t, []string{"--verbose"}, cmd.req.Raw)
	})
	t.Run("--api-key absent", func(t *testing.T) {
		req := parseOK(t, []string{"x"})
		assert.Equal(t, args.Optional{}, req.APIKey)
	})
	t.Run("--api-key= clears", func(t *testing.T) {
		req := parseOK(t, []string{"--api-key=", "x"})
		assert.Equal(t, args.Optional{Set: true}, req.APIKey)
	})
	t.Run("--api-key split form", func(t *testing.T) {
		req := parseOK(t, []string{"--api-key", "k-1", "x"})
		assert.Equal(t, args.Optional{Value: "k-1", Set: true}, req.APIKey)
	})
	t.Run("--help", func(t *testing.T) {
		cmd, err := parseArgs([]string{"--verbose", "--help"})
		require.NoError(t, err)
		assert.Nil(t, cmd.req)
		assert.Contains(t, cmd.help, "AGROUTER_CLASSIFIER_API_KEY")
		assert.Contains(t, cmd.help, "--api-key=KEY")
		assert.NotContains(t, strings.ToLower(cmd.help), "jev")
		assert.Contains(t, cmd.help, "process")
		assert.Contains(t, cmd.help, "--cli=NAME")
		assert.Contains(t, cmd.help, "exec [CLI]")
		assert.NotContains(t, cmd.help, "--model=", "the removed options are hidden")
		assert.NotContains(t, cmd.help, "--effort=")
		assert.NotContains(t, cmd.help, "-m, ")
		assert.Contains(t, cmd.help, "-p, --prompt=TEXT")
		assert.Contains(t, cmd.help, "--print=TEXT")
		assert.Contains(t, cmd.help, "--prompt-file=PATH")
		assert.Contains(t, cmd.help, "--doc=PATH")
		assert.NotContains(t, cmd.help, "/cli")
		assert.NotContains(t, cmd.help, "--config")
		assert.NotContains(t, cmd.help, "AGROUTER_CLI")
		assert.NotContains(t, cmd.help, "skipped")
	})
	t.Run("--version", func(t *testing.T) {
		cmd, err := parseArgs([]string{"--version"})
		require.NoError(t, err)
		assert.True(t, cmd.version)
		assert.Nil(t, cmd.req)
	})
}

func TestParseArgs_PrintIsPromptAlias(t *testing.T) {
	for _, mode := range [][]string{nil, {"exec", "tool-a"}} {
		for _, spelling := range [][]string{{"--print", "fix it"}, {"--print=fix it"}} {
			want := parseOK(t, append(slices.Clone(mode), "-p", "fix it"))
			got := parseOK(t, append(slices.Clone(mode), spelling...))
			assert.Equal(t, want, got, spelling)
			assert.Equal(t, positional("fix it"), got.PromptFlag)
			assert.False(t, got.Prompt.Set)
		}
	}
	t.Run("--print after -- goes to the child", func(t *testing.T) {
		req := parseOK(t, []string{"exec", "tool-a", "-p", "x", "--", "--print"})
		assert.Equal(t, positional("x"), req.PromptFlag)
		assert.Equal(t, []string{"--print"}, req.Raw)
	})
}

func TestParseArgs_PromptSources(t *testing.T) {
	tests := []struct {
		name              string
		argv              []string
		wantFlag, wantPos args.Optional
		wantFile          args.Optional
	}{
		{name: "-p alone", argv: []string{"-p", "fix it"}, wantFlag: positional("fix it")},
		{name: "--prompt = form", argv: []string{"--prompt=fix it"}, wantFlag: positional("fix it")},
		{name: "-p attached", argv: []string{"-pfix"}, wantFlag: positional("fix")},
		{name: "empty -p is set", argv: []string{"-p", ""}, wantFlag: positional("")},
		{name: "--prompt-file alone", argv: []string{"--prompt-file", "task.md"}, wantFile: positional("task.md")},
		{name: "every source", argv: []string{"--prompt-file=task.md", "positional", "-p", "flag", "--verbose"},
			wantFlag: positional("flag"), wantPos: positional("positional"), wantFile: positional("task.md")},
		{name: "slash values survive", argv: []string{"-p", "/review", "--prompt-file", "/tmp/task.md"},
			wantFlag: positional("/review"), wantFile: positional("/tmp/task.md")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := parseOK(t, tc.argv)
			assert.Equal(t, tc.wantFlag, req.PromptFlag)
			assert.Equal(t, tc.wantPos, req.Prompt)
			assert.Equal(t, tc.wantFile, req.PromptFile)
			assert.Empty(t, req.FileText, "the file is read by the app, not the parser")
		})
	}
}

func TestParseArgs_Docs(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want []string
	}{
		{name: "none", argv: []string{"fix it"}},
		{name: "one", argv: []string{"--doc", "CLAUDE.md", "fix it"}, want: []string{"CLAUDE.md"}},
		{name: "repeated, order kept", argv: []string{"--doc=b.md", "fix it", "--doc", "a.md", "--doc", "b.md"},
			want: []string{"b.md", "a.md", "b.md"}},
		{name: "slash path survives", argv: []string{"--doc", "/repo/AGENTS.md", "fix it"}, want: []string{"/repo/AGENTS.md"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := parseOK(t, tc.argv)
			assert.Equal(t, tc.want, req.Docs)
			assert.Equal(t, positional("fix it"), req.Prompt)
		})
	}
}

func TestParseArgs_Raw(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want []string
	}{
		{"tokens kept apart from the positional, later -- kept",
			[]string{"exec", "tool-a", "fix it", "--", "--add-dir", "../x", "--", "extra"},
			[]string{"--add-dir", "../x", "--", "extra"}},
		{"a positional-looking token is not a second positional", []string{"exec", "tool-a", "fix it", "--", "other"},
			[]string{"other"}},
		{"-- with nothing after", []string{"exec", "tool-a", "x", "--"}, []string{}},
		{"empty tokens, spaces and repeats kept", []string{"exec", "tool-a", "x", "--", "", "a b", "-v", "-v", "/abs"},
			[]string{"", "a b", "-v", "-v", "/abs"}},
		{"prompt text after -- is not a prompt", []string{"exec", "tool-a", "--", "fix it"}, []string{"fix it"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := parseOK(t, tc.argv)
			assert.Equal(t, tc.want, req.Raw)
			if tc.name == "prompt text after -- is not a prompt" {
				assert.False(t, req.Prompt.Set)
				assert.False(t, req.PromptFlag.Set)
			} else {
				assert.True(t, req.Prompt.Set)
			}
		})
	}
}

func TestParseArgs_SlashTokens(t *testing.T) {
	// go-flags on Windows takes "/x" as an option; prompts and values starting with "/" must survive
	cmd, err := parseArgs([]string{"--doc=/weird", "/review the diff"})
	require.NoError(t, err)
	req := cmd.req
	assert.Equal(t, positional("/review the diff"), req.Prompt)
	assert.Equal(t, []string{"/weird"}, req.Docs)
}

func TestParseArgs_Errors(t *testing.T) {
	tests := []struct {
		name    string
		argv    []string
		wantErr string
	}{
		{"unknown long flag", []string{"--no-such-flag", "x"}, "no-such-flag"},
		{"unknown short flag", []string{"-z", "x"}, "z"},
		{"prompt-looking flag", []string{"-fix it"}, "unknown flag"},
		{"second positional", []string{"one", "two"}, "more than one positional"},
		{"second positional around flags", []string{"exec", "tool-a", "one", "--verbose", "two"}, "more than one positional"},
		{"missing value", []string{"x", "--model"}, "model"},
		{"second -p", []string{"-p", "a", "--prompt", "b"}, "-p/--prompt/--print given more than once"},
		{"-p and --print", []string{"-p", "a", "--print", "b"}, "-p/--prompt/--print given more than once"},
		{"bare --print", []string{"x", "--print"}, "print"},
		{"bare --print before --", []string{"exec", "tool-a", "x", "--print", "--", "y"}, "print"},
		{"-- in decision mode", []string{"x", "--", "--add-dir"}, `"--" is only accepted with exec`},
		{"empty -- in decision mode", []string{"x", "--"}, `"--" is only accepted with exec`},
		{"removed --permission-mode", []string{"--permission-mode", "plan", "x"}, "permission-mode"},
		{"removed --dangerously-skip-permissions", []string{"--dangerously-skip-permissions", "x"}, "dangerously-skip-permissions"},
		{"removed --dangerously-bypass-approvals-and-sandbox", []string{"exec", "tool-a",
			"--dangerously-bypass-approvals-and-sandbox", "x"}, "dangerously-bypass-approvals-and-sandbox"},
		{"removed --output-format", []string{"--output-format=json", "x"}, "output-format"},
		{"removed --sandbox", []string{"--sandbox", "read-only", "x"}, "sandbox"},
		{"removed -c", []string{"exec", "tool-a", "-c", "k=v", "x"}, "unknown flag"},
		{"removed --config", []string{"--config=k=v", "x"}, "config"},
		{"removed --jev-api-key", []string{"--jev-api-key=k", "x"}, "jev-api-key"},
		{"second --prompt-file", []string{"--prompt-file", "a", "--prompt-file=b"}, "--prompt-file given more than once"},
		{"-p value looks like a flag", []string{"-p", "--model", "x"}, "expected argument"},
		{"-p without value", []string{"x", "-p"}, "prompt"},
		{"--prompt-file without value", []string{"x", "--prompt-file"}, "prompt-file"},
		{"--doc without value", []string{"x", "--doc"}, "doc"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseArgs(tc.argv)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.NotContains(t, err.Error(), "\n")
			assert.NotContains(t, err.Error(), slashEscape)
		})
	}
}

func TestRun_SecondPositional(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"one", "two"}, strings.NewReader(""), &stdout, &stderr)

	assert.Equal(t, 2, code)
	assert.Empty(t, stdout.String())
	assert.Equal(t, "agrouter: more than one positional argument: pass the prompt as one quoted argument\n", stderr.String())
}

func TestPosixHelp(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"value option", "      /cli:NAME      restrict", "      --cli=NAME     restrict"},
		{"short and long", "  /p, /prompt:TEXT        the", "  -p, --prompt=TEXT       the"},
		{"long flag", "      /verbose       verbose", "      --verbose      verbose"},
		{"windows-only help line dropped", "x\n  /?                 Show\ny", "x\ny"},
		{"description continuation kept", "                     CLI (env)", "                     CLI (env)"},
		{"prose untouched", "a local .agrouter/config", "a local .agrouter/config"},
		{"posix help untouched", "      --cli=NAME     restrict", "      --cli=NAME     restrict"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, posixHelp(tt.in))
		})
	}
}

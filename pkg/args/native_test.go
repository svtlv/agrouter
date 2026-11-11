package args

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/svtlv/agrouter/pkg/config"
)

func TestNativeSelection(t *testing.T) {
	claudeLike := config.CLI{ModelArgs: []string{"--model", "{model}"}, EffortArgs: []string{"--effort", "{effort}"}}
	codexLike := config.CLI{
		ModelArgs:  []string{"-c", `model="{model}"`},
		EffortArgs: []string{"-c", `model_reasoning_effort="{effort}"`},
	}
	modelOnly := config.CLI{ModelArgs: []string{"--model", "{model}"}}
	tests := []struct {
		name string
		cli  config.CLI
		raw  []string
		want Selection
	}{
		{name: "no native args", cli: claudeLike},
		{name: "exact sequences", cli: claudeLike, raw: []string{"-v", "--model", "opus", "--effort", "low"}, want: Selection{"opus", "low"}},
		{name: "attached long flag", cli: claudeLike, raw: []string{"--model=opus", "--effort=low"}, want: Selection{"opus", "low"}},
		{name: "model only", cli: claudeLike, raw: []string{"--model", "opus"}, want: Selection{Model: "opus"}},
		{name: "effort only", cli: claudeLike, raw: []string{"--effort", "low"}, want: Selection{Effort: "low"}},
		{name: "last match wins", cli: claudeLike, raw: []string{"--model", "a", "--model=b", "--model", "c"}, want: Selection{Model: "c"}},
		{name: "attached after exact", cli: claudeLike, raw: []string{"--model", "a", "--model=b"}, want: Selection{Model: "b"}},
		{name: "quoted codex values", cli: codexLike, raw: []string{"-c", `model="m"`, "-c", `model_reasoning_effort="high"`}, want: Selection{"m", "high"}},
		{name: "unquoted codex values", cli: codexLike, raw: []string{"-c", "model=m", "-c", "model_reasoning_effort=high"}, want: Selection{"m", "high"}},
		{name: "ralphex shape", cli: codexLike, raw: []string{
			"-c", "features.multi_agent=true", "--sandbox", "workspace-write", "-c", `model="m"`,
			"-c", "model_reasoning_effort=medium", "-c", "stream_idle_timeout_ms=60000", "-c", `project_doc="p"`,
		}, want: Selection{"m", "medium"}},
		{name: "single quotes optional", cli: config.CLI{ModelArgs: []string{"-c", "model='{model}'"}}, raw: []string{"-c", "model=m"}, want: Selection{Model: "m"}},
		{name: "child separator ends the scan", cli: claudeLike, raw: []string{"--model", "a", "--", "--model", "b", "--effort", "x"}, want: Selection{Model: "a"}},
		{name: "separator first", cli: claudeLike, raw: []string{"--", "--model", "a"}},
		{name: "model flag without value", cli: claudeLike, raw: []string{"--json", "--model"}},
		{name: "value is another option", cli: claudeLike, raw: []string{"--model", "--effort", "low"}, want: Selection{Effort: "low"}},
		{name: "empty values", cli: claudeLike, raw: []string{"--model", "", "--effort="}},
		{name: "empty codex values", cli: codexLike, raw: []string{"-c", "model=", "-c", `model_reasoning_effort=""`}},
		{name: "alias spelling is not recognized", cli: claudeLike, raw: []string{"-m", "x", "-mx", "-m=x"}},
		{name: "codex alias is not recognized", cli: codexLike, raw: []string{"-m", "x", "--model", "y"}},
		{name: "lookalike long flags", cli: claudeLike, raw: []string{"--modelx", "a", "--model-provider=b", "--models=c", "--effortx=d"}},
		{name: "lookalike config keys", cli: codexLike, raw: []string{"-c", "model_x=1", "-c", "xmodel=1", "-c", "model_reasoning_effort_x=1", "-c", "models=1"}},
		{name: "claude flags do not match codex", cli: codexLike, raw: []string{"--model", "opus", "--effort", "low"}},
		{name: "config flag without a key", cli: codexLike, raw: []string{"-c"}},
		{name: "cli without effort_args", cli: modelOnly, raw: []string{"--model", "a", "--effort", "low"}, want: Selection{Model: "a"}},
		{name: "cli without templates", cli: config.CLI{}, raw: []string{"--model", "a"}},
		{name: "lone placeholder never matches", cli: config.CLI{ModelArgs: []string{"{model}"}}, raw: []string{"x", "y"}},
		{name: "suffix tokens must follow", cli: config.CLI{ModelArgs: []string{"--model", "{model}", "--yes"}}, raw: []string{"--model", "a", "--no", "--model", "b", "--yes"}, want: Selection{Model: "b"}},
		{name: "value with equals sign", cli: claudeLike, raw: []string{"--model=a=b"}, want: Selection{Model: "a=b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			original := append([]string(nil), tc.raw...)
			assert.Equal(t, tc.want, NativeSelection(tc.cli, tc.raw))
			assert.Equal(t, original, tc.raw, "the native slice stays unchanged")
		})
	}
}

func TestNativeOptions(t *testing.T) {
	tests := []struct {
		name      string
		raw, want []string
	}{
		{name: "empty", want: []string{}},
		{name: "model and effort", raw: []string{"--model", "native", "--effort", "native", "--json"}, want: []string{"--json"}},
		{name: "equals values", raw: []string{"--model=native", "--effort=native", "--json"}, want: []string{"--json"}},
		{name: "short forms", raw: []string{"-m", "a", "-mb", "-m=c", "-m", ""}, want: []string{}},
		{name: "prefixes are unrelated", raw: []string{"--models", "--model-provider", "x", "--efforts"}, want: []string{"--models", "--model-provider", "x", "--efforts"}},
		{name: "settings and config unchanged", raw: []string{"--profile", "work", "-c", `model="native"`, "--config=reasoning=native"}, want: []string{"--profile", "work", "-c", `model="native"`, "--config=reasoning=native"}},
		{name: "empty values and repeats", raw: []string{"", "two words", "-v", "-v"}, want: []string{"", "two words", "-v", "-v"}},
		{name: "child separator stops scanning", raw: []string{"-m", "native", "--", "-m", "text", "--effort=x"}, want: []string{"--", "-m", "text", "--effort=x"}},
		{name: "dangling selection", raw: []string{"--json", "-m"}, want: []string{"--json"}},
		{name: "dangling model before another option", raw: []string{"-m", "--json"}, want: []string{"--json"}},
		{name: "selection before separator", raw: []string{"--effort", "--", "--model", "text"}, want: []string{"--", "--model", "text"}},
		{name: "selection before another option", raw: []string{"--model", "--effort", "native", "--json"}, want: []string{"--json"}},
		{name: "unrelated attached flag value", raw: []string{"--add-dir=-m"}, want: []string{"--add-dir=-m"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			original := append([]string(nil), tc.raw...)
			assert.Equal(t, tc.want, nativeOptions(tc.raw))
			assert.Equal(t, original, tc.raw, "the caller's native slice stays unchanged")
		})
	}
}

func TestCheckSeparator(t *testing.T) {
	alpha := fixtureCLI(t, "alpha")
	noSeparator := alpha
	noSeparator.PromptArgs = []string{"{prompt}"}
	withPrompt := Request{Prompt: Optional{Value: "task", Set: true}}
	tests := []struct {
		name    string
		req     Request
		raw     []string
		wantErr bool
	}{
		{name: "prompt and a native --", req: withPrompt, raw: []string{"-x", "--", "y"}, wantErr: true},
		{name: "prompt from the file", req: Request{FileText: "task"}, raw: []string{"--"}, wantErr: true},
		{name: "prompt without native --", req: withPrompt, raw: []string{"-x"}},
		{name: "no command-line prompt: stdin only", raw: []string{"--"}},
		{name: "empty prompt sources", req: Request{Prompt: Optional{Set: true}}, raw: []string{"--"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.req.Raw = tc.raw
			err := CheckSeparator(alpha, &tc.req)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "pass the prompt on stdin, or drop the extra \"--\"")
			assert.NotContains(t, err.Error(), "task")
		})
	}
	t.Run("prompt_args without --", func(t *testing.T) {
		req := withPrompt
		req.Raw = []string{"--"}
		require.NoError(t, CheckSeparator(noSeparator, &req))
	})
}

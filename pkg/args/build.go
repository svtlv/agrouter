package args

import (
	"strings"

	"github.com/svtlv/agrouter/pkg/config"
)

// Choice is what the argv is built for: the model and effort to emit through the CLI's descriptors.
// An empty Model or Effort emits no model or effort argument, so the CLI's default applies.
type Choice struct {
	Model  string // the model's name, or a value outside the catalog passed through
	Effort string
}

// Result is the child's argv.
type Result struct {
	Argv []string
	// promptAt is the {prompt} index, -1 when absent. The native tokens occupy one range, rawLen tokens
	// from rawAt, after the generated selection arguments.
	promptAt int
	rawAt    int
	rawLen   int
}

// Build emits the configured model and effort arguments for the fields choice sets, then the native
// tokens exactly as given, including the child's own "--" and everything after it. PromptArgs are last
// and only emitted for a command-line prompt.
func Build(cli config.CLI, req *Request, choice Choice) Result {
	res := Result{promptAt: -1}
	res.Argv = append(res.Argv, cli.Command)
	res.Argv = append(res.Argv, cli.ExecArgs...)
	if choice.Model != "" {
		res.Argv = appendTemplate(res.Argv, cli.ModelArgs, config.PlaceholderModel, choice.Model)
	}
	if choice.Effort != "" {
		res.Argv = appendTemplate(res.Argv, cli.EffortArgs, config.PlaceholderEffort, choice.Effort)
	}
	res.rawAt, res.rawLen = len(res.Argv), len(req.Raw)
	res.Argv = append(res.Argv, req.Raw...)
	if p := req.ArgvPrompt(); p != "" {
		for _, tok := range cli.PromptArgs {
			if tok == config.PlaceholderPrompt {
				res.promptAt = len(res.Argv)
				res.Argv = append(res.Argv, p)
				continue
			}
			res.Argv = append(res.Argv, tok)
		}
	}
	return res
}

// appendTemplate appends tmpl to argv with placeholder replaced by value in every token.
func appendTemplate(argv, tmpl []string, placeholder, value string) []string {
	for _, tok := range tmpl {
		argv = append(argv, strings.ReplaceAll(tok, placeholder, value))
	}
	return argv
}

package main

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/jessevdk/go-flags"

	"github.com/svtlv/agrouter/pkg/args"
)

// execToken selects exec mode, but only as the first token; an optional CLI to run follows it.
const execToken = "exec"

// legacyCLIEnv once restricted routing to one CLI; it is ignored now, with a warning when set.
const legacyCLIEnv = "AGROUTER_CLI"

// slashEscape is prepended to every token starting with "/" before go-flags sees it, and removed
// from every value it hands back. go-flags on Windows takes "/x" as an option, which would make a
// prompt such as "/review" or a value such as "/tmp/doc.md" an unknown flag.
const slashEscape = "\x00"

// helpText is the long description in --help, including the API key cautions.
const helpText = `agrouter asks a routing classifier which (cli, model, effort) should run a prompt.

  agrouter [OPTIONS] [PROMPT]
  agrouter exec [CLI] [OPTIONS] [PROMPT] [-- NATIVE ARGS...]

Without "exec" it prints that decision as JSON. With "exec" as the first token it
routes and runs the chosen CLI. The token after "exec" pins the CLI when it names
an enabled one (--cli does too); otherwise it is the prompt and routing chooses
the CLI. Everything after "--" reaches the chosen CLI unchanged: the CLI's own
model and effort flags there fix those fields, so routing skips them, and can
imply the CLI. "--" is only accepted with exec.
The default decision includes the selection, project complexity and the classifier's
confidence at each routing stage (cli, model, effort). --verbose prints indented
JSON with every routing stage, chunk, candidate probability, complexity score,
argv and a copyable command. --verbose before "--" controls agrouter's output
only; after "--" it is the chosen CLI's own option. Exec always logs a concise
selection line to stderr.

The prompt is -p TEXT (also --prompt or --print), the positional argument, the
--prompt-file text and stdin, in that order, joined by blank lines; at least one
must be non-empty. Nothing after "--" is read as a prompt. The chosen CLI gets
the first three as its prompt argument and stdin replayed.
--doc files (the project's agent instructions and docs) describe the project to
the classifier; they are read on every call but never passed to the chosen CLI.

The classifier API key comes from --api-key, then AGROUTER_CLASSIFIER_API_KEY, then
api_key in the selected provider's [classifier.<provider>] config section
(classifier_provider). With exec, a child's own --api-key goes after "--". A key on the command line is visible to other local users in
process listings; prefer the environment variable or the global config with
user-only permissions, and never put it in a local .agrouter/config that may
be committed.`

// command is the outcome of parsing: a request to route, or --help / --version.
type command struct {
	req     *args.Request
	help    string // the help text when --help was given
	version bool
	verbose bool
	// operand is the token right after exec when it is not an option: the CLI to pin if it names an
	// enabled one, else the positional prompt. The config is needed to tell, so run resolves it.
	operand string
	// cliFlag is the --cli value for exec, which pins the provider; cliFlagSet records that it was given.
	cliFlag    string
	cliFlagSet bool
}

// parseArgs parses agrouter's command line. The error is a usage error, for one "agrouter:" line
// and exit 2. Whether the exec operand names a CLI, and whether that CLI is enabled, is decided once the
// config is loaded.
func parseArgs(argv []string) (command, error) {
	req, operand, argv := execOperand(argv)

	flagArgv := argv
	separator := false
	if i := slices.Index(argv, "--"); i >= 0 {
		flagArgv, req.Raw, separator = argv[:i], append([]string{}, argv[i+1:]...), true
	}

	var seen flagCounts
	opts := newFlagSet(req, &seen)
	parser := flags.NewParser(opts, flags.HelpFlag)
	parser.Name = "agrouter"
	parser.Usage = "[exec [CLI]] [OPTIONS] [-p TEXT] [--prompt-file PATH] [--doc PATH]... [PROMPT] [-- NATIVE ARGS...]"
	parser.LongDescription = helpText

	rest, err := parser.ParseArgs(escapeSlashes(flagArgv))
	if err != nil {
		var flagsErr *flags.Error
		if errors.As(err, &flagsErr) && flagsErr.Type == flags.ErrHelp {
			return command{help: posixHelp(flagsErr.Message)}, nil
		}
		return command{}, fmt.Errorf("%s", unescape(err.Error()))
	}
	if opts.Version {
		return command{version: true}, nil
	}
	if err := seen.check(req, separator); err != nil {
		return command{}, err
	}

	// an operand is a positional too until the config says it is the CLI: then one more is the prompt
	if len(rest) > 1 {
		return command{}, errPositionals
	}
	if len(rest) == 1 {
		req.Prompt = args.Optional{Value: unescape(rest[0]), Set: true}
	}

	cmd := command{req: req, verbose: opts.Verbose, operand: operand}
	if seen.cli {
		if req.Mode == args.ModeExec {
			cmd.cliFlag, cmd.cliFlagSet = seen.cliValue, true
		} else {
			req.CLI = seen.cliValue
		}
	}
	return cmd, nil
}

// errPositionals is the usage error for a second positional argument.
var errPositionals = errors.New("more than one positional argument: pass the prompt as one quoted argument")

// resolveExec decides what the exec operand and --cli are once the enabled CLIs are known: the operand
// is the CLI when it names one, an error when it names a disabled one, else the positional prompt (a
// second positional is then too many); --cli pins the provider too, and a different CLI than the
// operand is a conflict. An unknown pin is an error.
func resolveExec(cmd command, enabled, disabled func(string) bool) error {
	req := cmd.req
	if req.Mode != args.ModeExec {
		return nil
	}
	if cmd.operand != "" {
		switch {
		case enabled(cmd.operand):
			req.CLI = cmd.operand
		case disabled(cmd.operand):
			return fmt.Errorf("exec %s: not a configured, enabled CLI", cmd.operand)
		case req.Prompt.Set:
			return errPositionals
		default:
			req.Prompt = args.Optional{Value: cmd.operand, Set: true}
		}
	}
	switch {
	case !cmd.cliFlagSet:
	case req.CLI != "" && cmd.cliFlag != req.CLI:
		return fmt.Errorf("--cli %s conflicts with exec %s: drop --cli", cmd.cliFlag, req.CLI)
	case cmd.cliFlag != "" && !enabled(cmd.cliFlag):
		return fmt.Errorf("exec --cli %s: not a configured, enabled CLI", cmd.cliFlag)
	default:
		req.CLI = cmd.cliFlag
	}
	return nil
}

// flagSet is agrouter's options. The func fields set the request as go-flags parses them.
type flagSet struct {
	CLI     func(string) `long:"cli" value-name:"NAME" unquote:"false" description:"restrict routing to one CLI; with exec, pins the CLI to run, like the CLI right after exec"`
	APIKey  func(string) `long:"api-key" value-name:"KEY" unquote:"false" description:"the routing classifier's API key, overriding the selected provider's [classifier.<provider>] api_key; empty clears it (env AGROUTER_CLASSIFIER_API_KEY)"`
	Version bool         `long:"version" description:"print agrouter's version and exit"`

	Prompt     func(string) `short:"p" long:"prompt" value-name:"TEXT" unquote:"false" description:"the prompt, before the positional prompt, --prompt-file and stdin"`
	Print      func(string) `long:"print" value-name:"TEXT" unquote:"false" description:"the same as -p TEXT"`
	PromptFile func(string) `long:"prompt-file" value-name:"PATH" unquote:"false" description:"read prompt text from a file, after -p and the positional prompt"`
	Doc        func(string) `long:"doc" value-name:"PATH" unquote:"false" description:"project doc for routing only, repeatable; never passed to the chosen CLI"`

	// Model and Effort are no longer options: they are declared hidden so that every spelling is
	// recognized and rejected with a hint instead of reading as an unknown flag or a prompt.
	Model  func(string) `short:"m" long:"model" hidden:"true" value-name:"MODEL" unquote:"false"`
	Effort func(string) `long:"effort" hidden:"true" value-name:"EFFORT" unquote:"false"`

	Verbose bool `long:"verbose" description:"include full decision details (agrouter only; after -- it goes to the chosen CLI)"`
}

// flagCounts records options whose repetition or presence matters after parsing.
type flagCounts struct {
	cli        bool   // --cli was given, even empty
	cliValue   string // the last --cli value
	prompt     int    // -p, --prompt and --print together
	promptFile int
	model      bool // --model/-m was given, even empty
	effort     bool // --effort was given, even empty
}

// newFlagSet binds the options to req, recording in seen what parseArgs checks afterwards.
func newFlagSet(req *args.Request, seen *flagCounts) *flagSet {
	promptFlag := func(v string) {
		req.PromptFlag = args.Optional{Value: unescape(v), Set: true}
		seen.prompt++
	}
	return &flagSet{
		CLI:    func(v string) { seen.cliValue, seen.cli = unescape(v), true },
		Prompt: promptFlag,
		Print:  promptFlag,
		PromptFile: func(v string) {
			req.PromptFile = args.Optional{Value: unescape(v), Set: true}
			seen.promptFile++
		},
		Doc: func(v string) { req.Docs = append(req.Docs, unescape(v)) },
		APIKey: func(v string) {
			req.APIKey = args.Optional{Value: unescape(v), Set: true}
		},
		Model:  func(string) { seen.model = true },
		Effort: func(string) { seen.effort = true },
	}
}

// execOperand starts the request: exec mode when argv starts with "exec", decision mode otherwise. When
// the token after exec is not empty or an option it is returned as the operand, and the arguments after
// it come back as the rest.
func execOperand(argv []string) (*args.Request, string, []string) {
	req := &args.Request{}
	if len(argv) == 0 || argv[0] != execToken {
		return req, "", argv
	}
	req.Mode = args.ModeExec
	if len(argv) < 2 || argv[1] == "" || strings.HasPrefix(argv[1], "-") {
		return req, "", argv[1:]
	}
	return req, argv[1], argv[2:]
}

// check reports the usage errors go-flags cannot see: repeated prompt options, the removed --model/-m and
// --effort options, and "--" outside exec.
func (c *flagCounts) check(req *args.Request, separator bool) error {
	switch {
	case c.prompt > 1:
		return errors.New("-p/--prompt/--print given more than once: pass the prompt as one argument")
	case c.promptFile > 1:
		return errors.New("--prompt-file given more than once")
	case c.model:
		return errors.New(`--model/-m is not an agrouter option: routing chooses the model; with exec, ` +
			`pass the CLI's own model flag after "--" (agrouter exec [CLI] ... -- --model X)`)
	case c.effort:
		return errors.New(`--effort is not an agrouter option: routing chooses the effort; with exec, ` +
			`pass the CLI's own effort flag after "--" (agrouter exec [CLI] ... -- --effort X)`)
	case separator && req.Mode != args.ModeExec:
		return errors.New(`"--" is only accepted with exec: agrouter exec [CLI] [OPTIONS] [PROMPT] [-- NATIVE ARGS...]`)
	}
	return nil
}

// escapeSlashes returns argv with slashEscape prepended to every token starting with "/".
func escapeSlashes(argv []string) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if strings.HasPrefix(a, "/") {
			a = slashEscape + a
		}
		out[i] = a
	}
	return out
}

// windowsOption matches one option name as go-flags renders it in Windows help: "/p", "/prompt",
// "/cli:NAME", with the spaces that pad it to the description column.
var windowsOption = regexp.MustCompile(`/([^\s,:]+)(:\S+)?( *)`)

// posixHelp rewrites go-flags' Windows help, which lists "/cli:NAME" and "/p, /prompt:TEXT", to the
// spellings agrouter accepts, "--cli=NAME" and "-p, --prompt=TEXT", keeping the description column. The
// "/?" line is dropped: escapeSlashes makes "/?" a prompt. Help on other platforms is unchanged.
func posixHelp(help string) string {
	lines := strings.Split(help, "\n")
	out := lines[:0]
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		if !strings.HasPrefix(trimmed, "/") {
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(trimmed, "/? ") {
			continue
		}
		indent := line[:len(line)-len(trimmed)]
		name, desc := trimmed, ""
		if i := strings.Index(trimmed, "  "); i >= 0 {
			name, desc = trimmed[:i], trimmed[i:]
		}
		renamed := windowsOption.ReplaceAllStringFunc(name, func(m string) string {
			sub := windowsOption.FindStringSubmatch(m)
			dash := "--"
			if len(sub[1]) == 1 {
				dash = "-"
			}
			return dash + sub[1] + strings.Replace(sub[2], ":", "=", 1) + sub[3]
		})
		// take the added width out of the padding so descriptions stay aligned
		pad := len(desc) - len(strings.TrimLeft(desc, " "))
		desc = desc[min(max(len(renamed)-len(name), 0), max(pad-1, 0)):]
		out = append(out, indent+renamed+desc)
	}
	return strings.Join(out, "\n")
}

// unescape reverses escapeSlashes for one value, or inside an error message.
func unescape(s string) string {
	return strings.ReplaceAll(s, slashEscape+"/", "/")
}

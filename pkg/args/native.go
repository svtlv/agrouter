package args

import (
	"errors"
	"slices"
	"strings"

	"github.com/svtlv/agrouter/pkg/config"
)

// Selection is the model and effort the native arguments set for one CLI; an empty field is unset.
type Selection struct {
	Model  string
	Effort string
}

// NativeSelection reads the model and effort the native arguments set for cli, by matching the CLI's own
// model_args and effort_args templates. It only reads: raw is never modified. The child's "--" ends the
// scan, and the last match of each template wins.
func NativeSelection(cli config.CLI, raw []string) Selection {
	if end := slices.Index(raw, config.EndOfOptions); end >= 0 {
		raw = raw[:end]
	}
	return Selection{
		Model:  templateValue(cli.ModelArgs, config.PlaceholderModel, raw),
		Effort: templateValue(cli.EffortArgs, config.PlaceholderEffort, raw),
	}
}

// templateValue returns the value the last match of tmpl in raw carries for placeholder, or "". A
// template matches as its exact token sequence, with the value in the placeholder's token; a template
// of a long flag and the bare placeholder also matches the attached "--flag=value" form, and quotes
// around the placeholder inside a token are optional in the native text.
func templateValue(tmpl []string, placeholder string, raw []string) string {
	at := slices.IndexFunc(tmpl, func(tok string) bool { return strings.Contains(tok, placeholder) })
	if at < 0 || strings.Count(tmpl[at], placeholder) != 1 {
		return ""
	}
	before, after, _ := strings.Cut(tmpl[at], placeholder)
	bare := before == "" && after == ""
	if bare && len(tmpl) == 1 {
		return "" // a lone placeholder would match every token
	}
	variants := tokenVariants(before, after)
	attached := bare && at == 1 && len(tmpl) == 2 && strings.HasPrefix(tmpl[0], "--")
	value := ""
	for i := range raw {
		if attached {
			if v, ok := strings.CutPrefix(raw[i], tmpl[0]+"="); ok && v != "" {
				value = v
			}
		}
		if i+len(tmpl) > len(raw) || !slices.Equal(raw[i:i+at], tmpl[:at]) ||
			!slices.Equal(raw[i+at+1:i+len(tmpl)], tmpl[at+1:]) {
			continue
		}
		// a bare value never starts with "-": it is the next option, so the template's value is missing
		if v, ok := tokenValue(raw[i+at], variants); ok && (!bare || !strings.HasPrefix(v, "-")) {
			value = v
		}
	}
	return value
}

// tokenVariants returns the literal text around the placeholder to try, quoted form first; when the
// placeholder sits inside matching quotes, the unquoted form is a second variant.
func tokenVariants(before, after string) [][2]string {
	variants := [][2]string{{before, after}}
	if before != "" && after != "" && before[len(before)-1] == after[0] && (after[0] == '"' || after[0] == '\'') {
		variants = append(variants, [2]string{before[:len(before)-1], after[1:]})
	}
	return variants
}

// tokenValue extracts the placeholder's value from tok for the first variant that matches.
func tokenValue(tok string, variants [][2]string) (string, bool) {
	for _, v := range variants {
		rest, ok := strings.CutPrefix(tok, v[0])
		if !ok {
			continue
		}
		if value, ok := strings.CutSuffix(rest, v[1]); ok {
			return value, value != "" // the first structural match decides, so quoted "" stays empty
		}
	}
	return "", false
}

// CheckSeparator rejects a command-line prompt that prompt_args puts after "--" when the native tokens
// already hold "--": everything after the first one is positional for the child, so the prompt's "--"
// would reach it as prompt text.
func CheckSeparator(cli config.CLI, req *Request) error {
	if req.ArgvPrompt() == "" || !slices.Contains(cli.PromptArgs, config.EndOfOptions) ||
		!slices.Contains(req.Raw, config.EndOfOptions) {
		return nil
	}
	return errors.New(`the native arguments after "--" hold another "--", and the prompt would follow a second one: ` +
		`pass the prompt on stdin, or drop the extra "--"`)
}

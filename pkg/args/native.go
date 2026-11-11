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
// template matches as its exact token sequence, with the value in each placeholder token; when the
// placeholder repeats, every occurrence must carry the same value. A template of a long flag and the
// bare placeholder also matches the attached "--flag=value" form, and quotes around the placeholder
// inside a token are optional in the native text.
func templateValue(tmpl []string, placeholder string, raw []string) string {
	slots, ok := placeholderSlots(tmpl, placeholder)
	if !ok || len(tmpl) == 1 && slots[0].bare {
		return "" // no placeholder, or a lone one that would match every token
	}
	attached := len(slots) == 1 && slots[0].bare && slots[0].at == 1 && len(tmpl) == 2 && strings.HasPrefix(tmpl[0], "--")
	value := ""
	for i := range raw {
		if attached {
			if v, ok := strings.CutPrefix(raw[i], tmpl[0]+"="); ok && v != "" {
				value = v
			}
		}
		if i+len(tmpl) > len(raw) {
			continue
		}
		window := raw[i : i+len(tmpl)]
		if v, ok := matchAt(tmpl, window, placeholder, func(j int) (string, bool) { return slotValue(slots, j, window[j]) }); ok {
			value = v
		}
	}
	return value
}

// slot is a template token that holds the placeholder, with the literal text around it.
type slot struct {
	at       int
	bare     bool // the token is the placeholder alone
	variants [][2]string
}

// placeholderSlots returns the template tokens that hold the placeholder; false when there is none or a
// token holds it twice.
func placeholderSlots(tmpl []string, placeholder string) ([]slot, bool) {
	var slots []slot
	for i, tok := range tmpl {
		switch n := strings.Count(tok, placeholder); {
		case n == 0:
		case n > 1:
			return nil, false
		default:
			before, after, _ := strings.Cut(tok, placeholder)
			slots = append(slots, slot{at: i, bare: before == "" && after == "", variants: tokenVariants(before, after)})
		}
	}
	return slots, len(slots) > 0
}

// slotValue extracts the value of the slot at template index j from tok. A bare value never starts with
// "-": it is the next option, so the template's value is missing.
func slotValue(slots []slot, j int, tok string) (string, bool) {
	for _, sl := range slots {
		if sl.at != j {
			continue
		}
		v, ok := tokenValue(tok, sl.variants)
		return v, ok && (!sl.bare || !strings.HasPrefix(v, "-"))
	}
	return "", false
}

// matchAt reports the value when window matches tmpl: literal tokens equal, and extract yielding one
// and the same value for every token that holds the placeholder.
func matchAt(tmpl, window []string, placeholder string, extract func(j int) (string, bool)) (string, bool) {
	value := ""
	found := false
	for j, tok := range tmpl {
		if !strings.Contains(tok, placeholder) {
			if window[j] != tok {
				return "", false
			}
			continue
		}
		v, ok := extract(j)
		if !ok || found && v != value {
			return "", false
		}
		value, found = v, true
	}
	return value, found
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

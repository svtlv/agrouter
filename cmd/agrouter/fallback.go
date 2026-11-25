package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/svtlv/agrouter/pkg/args"
	"github.com/svtlv/agrouter/pkg/config"
	"github.com/svtlv/agrouter/pkg/readiness"
	"github.com/svtlv/agrouter/pkg/router"
)

// Sources of an effective value: what the child runs with and who fixed it.
const (
	sourceRouter          = "router"                 // agrouter chose it: the classifier, or the one option left
	sourceNative          = "native"                 // a native model or effort argument fixed it
	sourceVerifiedDefault = "verified_child_default" // left to the child, whose probe reported its default
	sourceUnknownDefault  = "child_default_unknown"  // left to the child, whose default is not known
)

// Why a child default is unknown.
const (
	defaultNotReported = "default_not_reported"
	defaultOtherModel  = "default_for_other_model"
	defaultNotProbed   = "default_not_probed" // decision mode, or no preflight result for the chosen CLI
)

// defaultReasonTexts are the warning's words for an unknown default.
var defaultReasonTexts = map[string]string{
	defaultNotReported: "child did not report it",
	defaultOtherModel:  "child reported it for another model",
	defaultNotProbed:   "no probe ran",
}

// fallbackAction is the only fallback: the child runs with its own defaults.
const fallbackAction = "child_defaults"

// fallbackJSON says which fields fell back to the child's defaults after routing failed.
type fallbackJSON struct {
	Action  string   `json:"action"`
	Fields  []string `json:"fields"`
	Message string   `json:"message"`
}

// effectiveJSON is what the child runs with and who fixed each value.
type effectiveJSON struct {
	Model  *effectiveValueJSON `json:"model,omitempty"`
	Effort *effectiveValueJSON `json:"effort,omitempty"`
}

// effectiveValueJSON is one effective value and where it comes from; value is null when unknown.
type effectiveValueJSON struct {
	Value  *string   `json:"value"`
	Source string    `json:"source"`
	Reason *codeJSON `json:"reason,omitempty"`
}

type codeJSON struct {
	Code string `json:"code"`
}

// readinessJSON summarizes how readiness shaped the selection: the options excluded by reason, and
// the unknown warning's reason.
type readinessJSON struct {
	Excluded map[string]int `json:"excluded,omitempty"`
	Unknown  string         `json:"unknown,omitempty"`
}

// execSelection is exec's selection line for d: the fallback, what the child runs with and who fixed
// each value (effective), and the readiness summary. A delegated model whose verified default is
// known blocked, or unverified under unknown = block, stops exec as a preflight block would; that
// line carries no effective. Without a preflight result (d.checked is nil) no default is known, so
// a value left to the child is unknown with the reason defaultNotProbed.
func (a *app) execSelection(cli config.CLI, req *args.Request, d routed) (selectionJSON, *preflightError) {
	s := selectionOutput(d, false, nil)
	pre := d.checked
	var defaults readiness.Defaults
	unknown := ""
	if pre != nil {
		defaults = pre.report.Defaults
		unknown = pre.ev.Unknown
		if s.Model == nil {
			v := pre.ev.Model(deref(defaults.Model))
			switch {
			case v.Status == readiness.StatusUnavailable, v.Status == readiness.StatusUnknown && unknownPolicy(cli) == config.UnknownBlock:
				return s, preflightBlock(cli, v)
			case v.Status == readiness.StatusUnknown && unknown == "":
				unknown = v.Reason
				a.warnUnknown(cli, unknown)
			}
		}
	}

	model := effective(s.Model, req.Native[cli.Name].Model, defaults.Model, defaultNotProbedFor(pre))
	var effort *effectiveValueJSON
	if !d.NoEffort {
		// the probe's default effort belongs to the child's default model, not to one agrouter set
		def, why := defaults.Effort, defaultNotProbedFor(pre)
		if def != nil && s.Model != nil && !pre.ev.SameModel(*s.Model, deref(defaults.Model)) {
			def, why = nil, defaultOtherModel
		}
		effort = effective(s.Effort, req.Native[cli.Name].Effort, def, why)
	}
	s.Effective = &effectiveJSON{Model: model, Effort: effort}

	if d.Undecided != nil {
		var fields []string
		if s.Model == nil {
			fields = append(fields, router.LevelModel)
		}
		if effort != nil && s.Effort == nil {
			fields = append(fields, router.LevelEffort)
		}
		if len(fields) > 0 {
			s.Fallback = &fallbackJSON{Action: fallbackAction, Fields: fields, Message: fallbackMessage(fields)}
			fmt.Fprintln(a.stderr, fallbackWarning(cli.Name, s.Reason, fields, s.Effective))
		}
	}
	if pre != nil {
		s.Readiness = readinessSummary(pre.ev, unknown)
	}
	return s, nil
}

// defaultNotProbedFor is the reason for an unreported default: defaultNotProbed when no probe ran,
// else empty so effective picks defaultNotReported.
func defaultNotProbedFor(pre *checked) string {
	if pre == nil {
		return defaultNotProbed
	}
	return ""
}

// effective is a value's effective form: chosen (natively when fixed is set, else by agrouter),
// or the default reported by the probe in agrouter's configured context.
// why is the reason for a default that is not reported, defaultNotReported when empty.
func effective(chosen *string, fixed string, def *string, why string) *effectiveValueJSON {
	switch {
	case chosen != nil && fixed != "":
		return &effectiveValueJSON{Value: chosen, Source: sourceNative}
	case chosen != nil:
		return &effectiveValueJSON{Value: chosen, Source: sourceRouter}
	case def != nil:
		return &effectiveValueJSON{Value: def, Source: sourceVerifiedDefault}
	case why == "":
		why = defaultNotReported
	}
	return &effectiveValueJSON{Source: sourceUnknownDefault, Reason: &codeJSON{why}}
}

// fallbackMessage is the fallback's fixed message for fields.
func fallbackMessage(fields []string) string {
	if len(fields) == 1 {
		return capitalize(fields[0]) + " falls back to the child's default, not a classifier selection"
	}
	return capitalize(strings.Join(fields, " and ")) + " fall back to the child's defaults, not a classifier selection"
}

// fallbackWarning is the one line saying routing failed and which fields fell back to what:
// "agrouter: warning: Classifier routing failed at the <stage> stage (<code>); <fields> fall back to <cli>
// defaults, not a classifier selection: <field> <value> (verified child default), <field> unknown (<why>)".
func fallbackWarning(cli string, reason *reasonJSON, fields []string, eff *effectiveJSON) string {
	var b strings.Builder
	b.WriteString("agrouter: warning: Classifier routing failed")
	if reason.Stage != "" {
		fmt.Fprintf(&b, " at the %s stage", reason.Stage)
	}
	fmt.Fprintf(&b, " (%s); ", reason.Code)
	if len(fields) == 1 {
		fmt.Fprintf(&b, "%s falls back to the %s default", fields[0], cli)
	} else {
		fmt.Fprintf(&b, "%s fall back to %s defaults", strings.Join(fields, " and "), cli)
	}
	b.WriteString(", not a classifier selection: ")
	values := make([]string, len(fields))
	for i, f := range fields {
		v := eff.Model
		if f == router.LevelEffort {
			v = eff.Effort
		}
		if v.Source == sourceVerifiedDefault {
			values[i] = fmt.Sprintf("%s %s (verified child default)", f, *v.Value)
		} else {
			values[i] = fmt.Sprintf("%s unknown (%s)", f, defaultReasonTexts[v.Reason.Code])
		}
	}
	b.WriteString(strings.Join(values, ", "))
	return b.String()
}

// readinessSummary counts ev's excluded options by reason, beside the unknown warning's reason; nil
// when readiness neither excluded an option nor warned.
func readinessSummary(ev *readiness.Evaluation, unknown string) *readinessJSON {
	out := &readinessJSON{Unknown: unknown}
	for _, o := range ev.Options {
		if slices.Contains(ev.Excluded, o.ID) {
			if out.Excluded == nil {
				out.Excluded = map[string]int{}
			}
			out.Excluded[o.Reason]++
		}
	}
	if len(out.Excluded) == 0 && out.Unknown == "" {
		return nil
	}
	return out
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

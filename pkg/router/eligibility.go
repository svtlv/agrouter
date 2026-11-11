// Package router decides which catalog option runs a prompt: eligibility from the caller's
// --cli and native model and effort, then the classifier.
package router

import (
	"fmt"
	"slices"
	"strings"

	"github.com/svtlv/agrouter/pkg/args"
	"github.com/svtlv/agrouter/pkg/catalog"
	"github.com/svtlv/agrouter/pkg/config"
)

// Drop is a CLI eligibility removed, and why, for debug output.
type Drop struct {
	CLI    string
	Reason string
}

// Eligibility is what the filters leave for the classifier to choose from.
type Eligibility struct {
	// Options are in catalog order. Eligible never empties them; Exclude can.
	Options []catalog.Option
	// Pinned is true when a valid --cli or the exec CLI fixed the CLI.
	Pinned bool
	// ModelPassthrough: for some CLI the native model was not among its remaining options, so that
	// CLI has one option carrying it unresolved (or resolved to its catalog name).
	ModelPassthrough bool
	// EffortPassthrough: for some CLI no remaining option has the native effort, so that CLI's options
	// carry no effort and the native value is emitted as given.
	EffortPassthrough bool
	Dropped           []Drop
	// Warnings are the stderr lines for routing arguments left out (an unusable --cli).
	Warnings []string
	native   map[string]args.Selection
	efforts  map[string]string // cli -> the native effort its options pass through
}

// EffortFor returns the effort to emit for o: its own, or the native value passed through for its CLI.
func (e *Eligibility) EffortFor(o catalog.Option) string { return optionEffort(o, e.efforts[o.CLI]) }

// optionEffort returns o's own effort, or passed when o has none.
func optionEffort(o catalog.Option, passed string) string {
	if o.Effort != "" {
		return o.Effort
	}
	return passed
}

// CLIs returns the distinct CLIs still eligible, in catalog order.
func (e *Eligibility) CLIs() []string {
	return catalog.CLIs(e.Options)
}

// Eligible applies the filters in order: --cli, the CLIs the native selection implies, then each
// remaining CLI's native model and effort. No step ever empties the list: an unusable --cli is skipped
// with a warning, and a native model or effort nothing satisfies is passed through.
func Eligible(cat *catalog.Catalog, req *args.Request) *Eligibility {
	e := &Eligibility{Options: slices.Clone(cat.Options), native: req.Native, efforts: map[string]string{}}
	e.filterCLI(req.CLI)
	e.filterNative(cat)
	return e
}

func (e *Eligibility) filterCLI(name string) {
	if name == "" {
		return
	}
	kept := catalog.ByCLI(e.Options, name)
	if len(kept) == 0 {
		e.Warnings = append(e.Warnings,
			fmt.Sprintf("agrouter: warning: skipped --cli %s: not an enabled CLI; routing across every CLI", name))
		return
	}
	e.drop(kept, "--cli "+name)
	e.Options, e.Pinned = kept, true
}

// filterNative keeps the CLIs whose templates detected a native model or effort, then narrows each
// one's options to its own selection. A native model in a CLI's own catalog wins over an effort-only
// detection of another CLI, which is dropped. Without any detection it changes nothing.
func (e *Eligibility) filterNative(cat *catalog.Catalog) {
	var detected []string
	for _, cli := range catalog.CLIs(e.Options) {
		if sel := e.native[cli]; sel.Model != "" || sel.Effort != "" {
			detected = append(detected, cli)
		}
	}
	if len(detected) == 0 {
		return
	}
	reason := nativeReason(e.native, detected)
	owner := ""
	for _, cli := range detected {
		if _, ok := ownModel(cat, catalog.ByCLI(e.Options, cli), e.native[cli].Model); ok {
			owner = cli
			break
		}
	}
	var opts []catalog.Option
	for _, cli := range catalog.CLIs(e.Options) {
		switch {
		case !slices.Contains(detected, cli):
			e.Dropped = append(e.Dropped, Drop{CLI: cli, Reason: reason})
		case owner != "" && e.native[cli].Model == "":
			e.Dropped = append(e.Dropped, Drop{CLI: cli, Reason: "native model " + e.native[owner].Model})
		default:
			opts = append(opts, e.narrow(cat, cli, e.native[cli])...)
		}
	}
	e.Options = opts
}

// ownModel resolves value against the catalog and reports the model when it is one of opts' models.
func ownModel(cat *catalog.Catalog, opts []catalog.Option, value string) (config.Model, bool) {
	if value == "" {
		return config.Model{}, false
	}
	m, ok := cat.LookupModel(value)
	if !ok || len(catalog.ByModel(opts, m.Section)) == 0 {
		return config.Model{}, false
	}
	return m, true
}

// nativeReason names the native selection of the detected CLIs, for the CLIs it left out.
func nativeReason(native map[string]args.Selection, detected []string) string {
	if len(detected) > 1 {
		return "native selection of " + strings.Join(detected, ", ")
	}
	sel := native[detected[0]]
	var parts []string
	if sel.Model != "" {
		parts = append(parts, "model "+sel.Model)
	}
	if sel.Effort != "" {
		parts = append(parts, "effort "+sel.Effort)
	}
	return "native " + strings.Join(parts, ", ")
}

// narrow returns cli's options under its native selection: the model's options (or one option
// carrying a model outside the catalog, for the CLI to validate), then those with the effort (or each
// model once without an effort, which is passed through as given).
func (e *Eligibility) narrow(cat *catalog.Catalog, cli string, sel args.Selection) []catalog.Option {
	opts := catalog.ByCLI(e.Options, cli)
	modelPassed := false
	if sel.Model != "" {
		if m, ok := ownModel(cat, opts, sel.Model); ok {
			opts = catalog.ByModel(opts, m.Section)
		} else {
			// outside the catalog, or a catalog model of another CLI
			name := sel.Model
			if m, ok := cat.LookupModel(sel.Model); ok {
				name = m.Name
			}
			opts = []catalog.Option{{ID: cli, CLI: cli, Name: name}}
			modelPassed = true
			e.ModelPassthrough = true
		}
	}
	if sel.Effort == "" {
		return opts
	}
	if modelPassed {
		e.passEffort(cli, sel.Effort)
		return opts
	}
	if kept := catalog.ByEffort(opts, sel.Effort); len(kept) > 0 {
		return kept
	}
	// nothing has it: keep each model once, without an effort, and pass the value through
	var once []catalog.Option
	for _, o := range opts {
		if !slices.ContainsFunc(once, func(k catalog.Option) bool { return k.Section == o.Section }) {
			once = append(once, catalog.Option{ID: o.Section, CLI: o.CLI, Section: o.Section, Name: o.Name})
		}
	}
	e.passEffort(cli, sel.Effort)
	return once
}

// passEffort makes cli's options carry effort as given.
func (e *Eligibility) passEffort(cli, effort string) {
	e.efforts[cli] = effort
	e.EffortPassthrough = true
}

// Exclude removes the options with the given ids, such as those the exec CLI cannot run now. It
// never adds or broadens an option, and may leave none: Route then returns ErrNoEligible.
func (e *Eligibility) Exclude(ids []string) {
	if len(ids) == 0 {
		return
	}
	kept := slices.DeleteFunc(slices.Clone(e.Options), func(o catalog.Option) bool { return slices.Contains(ids, o.ID) })
	e.drop(kept, "unavailable")
	e.Options = kept
}

// drop records the CLIs of the current options that kept no longer has.
func (e *Eligibility) drop(kept []catalog.Option, reason string) {
	remaining := catalog.CLIs(kept)
	for _, cli := range catalog.CLIs(e.Options) {
		if !slices.Contains(remaining, cli) {
			e.Dropped = append(e.Dropped, Drop{CLI: cli, Reason: reason})
		}
	}
}

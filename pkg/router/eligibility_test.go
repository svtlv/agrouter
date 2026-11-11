package router

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/svtlv/agrouter/pkg/args"
	"github.com/svtlv/agrouter/pkg/catalog"
	"github.com/svtlv/agrouter/pkg/config"
	"github.com/svtlv/agrouter/pkg/config/defaults"
)

func embedded(t *testing.T) (*config.Config, *catalog.Catalog) {
	t.Helper()
	cfg, err := config.Load(config.Sources{Embedded: defaults.Config})
	require.NoError(t, err)
	cat, err := catalog.Build(cfg)
	require.NoError(t, err)
	return cfg, cat
}

// natAll is the selection as every CLI of the test catalogs detects it (a template they share).
func natAll(model, effort string) map[string]args.Selection {
	out := map[string]args.Selection{}
	for _, cli := range []string{"alpha", "beta", "claude", "codex"} {
		out[cli] = args.Selection{Model: model, Effort: effort}
	}
	return out
}

// natOf is the selection only cli's templates detect.
func natOf(cli, model, effort string) map[string]args.Selection {
	return map[string]args.Selection{cli: {Model: model, Effort: effort}}
}

func ids(opts []catalog.Option) []string {
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		out = append(out, o.ID)
	}
	return out
}

func idsOf(cat *catalog.Catalog, keep func(catalog.Option) bool) []string {
	var out []string
	for _, o := range cat.Options {
		if keep(o) {
			out = append(out, o.ID)
		}
	}
	return out
}

func TestEligibleIgnoresNativeArguments(t *testing.T) {
	_, cat := embedded(t)
	for _, raw := range [][]string{nil, {"--output-format", "json"}, {"--sandbox", "read-only"}, {"--", "--unknown"}} {
		e := Eligible(cat, &args.Request{Mode: args.ModeExec, Raw: raw})
		assert.Equal(t, ids(cat.Options), ids(e.Options), "%v: native tokens never filter or prefer CLIs", raw)
		assert.Empty(t, e.Dropped)
		assert.Empty(t, e.Warnings)
	}
}

func TestEligibleDropReasons(t *testing.T) {
	_, cat := embedded(t)

	e := Eligible(cat, &args.Request{CLI: "codex"})
	assert.Equal(t, []Drop{{CLI: "claude", Reason: "--cli codex"}}, e.Dropped)

	e = Eligible(cat, &args.Request{Native: natOf("claude", "opus", "")})
	assert.Equal(t, []Drop{{CLI: "codex", Reason: "native model opus"}}, e.Dropped)

	e = Eligible(cat, &args.Request{Native: natOf("codex", "", "ultra")})
	assert.Equal(t, []Drop{{CLI: "claude", Reason: "native effort ultra"}}, e.Dropped)
}

func TestEligibleCLI(t *testing.T) {
	_, cat := embedded(t)

	e := Eligible(cat, &args.Request{CLI: "claude"})
	assert.True(t, e.Pinned)
	assert.Equal(t, []string{"claude"}, e.CLIs())

	for _, name := range []string{"gemini", "Claude"} {
		e = Eligible(cat, &args.Request{CLI: name})
		assert.False(t, e.Pinned, name)
		assert.Equal(t, ids(cat.Options), ids(e.Options), name)
		assert.Equal(t, []string{"agrouter: warning: skipped --cli " + name +
			": not an enabled CLI; routing across every CLI"}, e.Warnings)
	}
}

func TestEligibleDisabledCLI(t *testing.T) {
	local := []byte("[cli.codex]\nenabled = false\n")
	path := filepath.Join(t.TempDir(), "config")
	require.NoError(t, os.WriteFile(path, local, 0o600))
	cfg, err := config.Load(config.Sources{Embedded: defaults.Config, LocalPath: path})
	require.NoError(t, err)
	cat, err := catalog.Build(cfg)
	require.NoError(t, err)

	e := Eligible(cat, &args.Request{CLI: "codex"})
	assert.False(t, e.Pinned)
	assert.Equal(t, []string{"claude"}, e.CLIs(), "the only CLI left")
	assert.Equal(t, []string{"agrouter: warning: skipped --cli codex: not an enabled CLI; routing across every CLI"}, e.Warnings)
}

func TestEligibleModel(t *testing.T) {
	_, cat := embedded(t)
	opus := idsOf(cat, func(o catalog.Option) bool { return o.Section == "claude-opus-5-5" })

	for _, value := range []string{"claude-opus-5-5", "opus"} {
		t.Run("catalog "+value, func(t *testing.T) {
			e := Eligible(cat, &args.Request{Native: natOf("claude", value, "")})
			assert.Equal(t, opus, ids(e.Options))
			assert.False(t, e.ModelPassthrough)
			for _, o := range e.Options {
				assert.Equal(t, "claude-opus-5-5", o.Name, "an alias resolves to the model's name")
			}
		})
	}

	t.Run("outside the catalog, no --cli: one option per CLI", func(t *testing.T) {
		e := Eligible(cat, &args.Request{Native: natAll("gpt-9", "")})
		assert.True(t, e.ModelPassthrough)
		assert.Equal(t, []catalog.Option{
			{ID: "claude@", CLI: "claude", Name: "gpt-9"},
			{ID: "codex@", CLI: "codex", Name: "gpt-9"},
		}, e.Options)
	})

	t.Run("outside the catalog with --cli", func(t *testing.T) {
		e := Eligible(cat, &args.Request{CLI: "codex", Native: natAll("gpt-9", "")})
		assert.Equal(t, []catalog.Option{{ID: "codex@", CLI: "codex", Name: "gpt-9"}}, e.Options)
	})

	t.Run("a catalog model of a CLI --cli dropped is passed through by name", func(t *testing.T) {
		e := Eligible(cat, &args.Request{CLI: "claude", Native: natAll("gpt-6.1-sol", "")})
		assert.True(t, e.ModelPassthrough)
		assert.Equal(t, []catalog.Option{{ID: "claude@", CLI: "claude", Name: "gpt-6.1-sol"}}, e.Options)
	})
}

func TestEligibleEffort(t *testing.T) {
	cfg, cat := embedded(t)

	t.Run("alone", func(t *testing.T) {
		e := Eligible(cat, &args.Request{Native: natAll("", "high")})
		assert.Equal(t, idsOf(cat, func(o catalog.Option) bool { return o.Effort == "high" }), ids(e.Options))
		assert.False(t, e.EffortPassthrough)
		assert.Empty(t, e.Dropped)
	})

	t.Run("only one CLI has it", func(t *testing.T) {
		e := Eligible(cat, &args.Request{Native: natOf("codex", "", "ultra")})
		assert.Equal(t, idsOf(cat, func(o catalog.Option) bool { return o.Effort == "ultra" }), ids(e.Options))
	})

	t.Run("nothing left has it: models kept without the effort", func(t *testing.T) {
		e := Eligible(cat, &args.Request{CLI: "claude", Native: natAll("", "ultra")})
		assert.True(t, e.EffortPassthrough)
		var models []string
		for _, m := range cfg.Models {
			if m.CLI == "claude" {
				models = append(models, m.Section)
			}
		}
		assert.Equal(t, models, ids(e.Options))
		for _, o := range e.Options {
			assert.Empty(t, o.Effort)
			assert.Equal(t, "ultra", e.EffortFor(o))
		}
	})

	t.Run("a model without efforts passes it through", func(t *testing.T) {
		_, fixCat := requestFixture(t)
		e := Eligible(fixCat, &args.Request{Native: natOf("alpha", "fast-model", "high")})
		assert.True(t, e.EffortPassthrough)
		require.Len(t, e.Options, 1)
		assert.Equal(t, "fast", e.Options[0].ID)
		assert.Equal(t, "high", e.EffortFor(e.Options[0]))
	})

	t.Run("model and effort leave one option", func(t *testing.T) {
		e := Eligible(cat, &args.Request{Native: natOf("claude", "opus", "high")})
		assert.Equal(t, []string{"claude-opus-5-5@high"}, ids(e.Options))
		assert.Equal(t, "high", e.EffortFor(e.Options[0]))
	})

	t.Run("model outside the catalog passes it through", func(t *testing.T) {
		e := Eligible(cat, &args.Request{CLI: "codex", Native: natAll("gpt-9", "high")})
		assert.True(t, e.ModelPassthrough)
		assert.True(t, e.EffortPassthrough)
		require.Len(t, e.Options, 1)
		assert.Equal(t, "high", e.EffortFor(e.Options[0]))
	})

	t.Run("no effort given, model without efforts emits none", func(t *testing.T) {
		_, fixCat := requestFixture(t)
		e := Eligible(fixCat, &args.Request{Native: natOf("alpha", "fast-model", "")})
		require.Len(t, e.Options, 1)
		assert.Empty(t, e.EffortFor(e.Options[0]))
	})
}

func TestEligibleNeverEmpty(t *testing.T) {
	_, cat := embedded(t)
	for _, cli := range []string{"", "claude", "codex", "gemini"} {
		for _, model := range []string{"", "opus", "gpt-6-luna", "gpt-9"} {
			for _, effort := range []string{"", "high", "ultra", "bogus"} {
				e := Eligible(cat, &args.Request{CLI: cli, Native: natAll(model, effort)})
				assert.NotEmpty(t, e.Options, "cli=%q model=%q effort=%q", cli, model, effort)
			}
		}
	}
}

func TestEligibilityExclude(t *testing.T) {
	_, cat := requestFixture(t)
	cases := []struct {
		name    string
		req     *args.Request
		exclude map[string][]string // cli -> ids
		want    []string
		dropped []Drop
	}{
		{name: "nothing to exclude", exclude: nil,
			want: []string{"strong@low", "strong@high", "fast", "worker@low", "worker@high"}},
		{name: "one model of a cli", exclude: map[string][]string{"alpha": {"strong@low", "strong@high"}},
			want: []string{"fast", "worker@low", "worker@high"}},
		{name: "every option of a cli is a drop", exclude: map[string][]string{"beta": {"worker@low", "worker@high"}},
			want: []string{"strong@low", "strong@high", "fast"}, dropped: []Drop{{CLI: "beta", Reason: "unavailable"}}},
		{name: "unknown ids are ignored", exclude: map[string][]string{"alpha": {"missing"}},
			want: []string{"strong@low", "strong@high", "fast", "worker@low", "worker@high"}},
		{name: "a fixed cli is never broadened", req: &args.Request{CLI: "alpha"}, exclude: map[string][]string{"alpha": {"strong@low", "strong@high"}},
			want: []string{"fast"}, dropped: []Drop{{CLI: "beta", Reason: "--cli alpha"}}},
		{name: "a passed-through model", req: &args.Request{CLI: "beta", Native: natAll("other-model", "")}, exclude: map[string][]string{"beta": {catalog.PassthroughID("beta")}},
			want: []string{}, dropped: []Drop{{CLI: "alpha", Reason: "--cli beta"}, {CLI: "beta", Reason: "unavailable"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req
			if req == nil {
				req = &args.Request{}
			}
			e := Eligible(cat, req)
			for cli, ids := range tc.exclude {
				e.Exclude(cli, ids)
			}
			assert.Equal(t, tc.want, append([]string{}, ids(e.Options)...))
			assert.Equal(t, tc.dropped, e.Dropped)
		})
	}
}

func TestEligibilityExcludeKeepsOriginal(t *testing.T) {
	_, cat := requestFixture(t)
	e := Eligible(cat, &args.Request{})
	before := e.Options
	e.Exclude("alpha", []string{"fast"})
	assert.Len(t, before, 5, "the slice handed out earlier is not rewritten")
	assert.Len(t, e.Options, 4)
}

func TestEligibleNativeSelection(t *testing.T) {
	_, cat := requestFixture(t)
	cases := []struct {
		name       string
		req        *args.Request
		want       []string
		efforts    map[string]string // option id -> the effort emitted for it, when it differs from its own
		modelPass  bool
		effortPass bool
		dropped    []Drop
	}{
		{name: "no selection keeps every option", req: &args.Request{Native: natAll("", "")},
			want: []string{"strong@low", "strong@high", "fast", "worker@low", "worker@high"}},
		{name: "a selection for a CLI that is not enabled is ignored", req: &args.Request{Native: natOf("gamma", "opus", "high")},
			want: []string{"strong@low", "strong@high", "fast", "worker@low", "worker@high"}},
		{name: "catalog model and effort", req: &args.Request{Native: natOf("alpha", "strong-model", "high")},
			want: []string{"strong@high"}, dropped: []Drop{{CLI: "beta", Reason: "native model strong-model, effort high"}}},
		{name: "catalog model only", req: &args.Request{Native: natOf("alpha", "strong-model", "")},
			want: []string{"strong@low", "strong@high"}, dropped: []Drop{{CLI: "beta", Reason: "native model strong-model"}}},
		{name: "catalog effort only", req: &args.Request{Native: natOf("alpha", "", "high")},
			want: []string{"strong@high"}, dropped: []Drop{{CLI: "beta", Reason: "native effort high"}}},
		{name: "model outside the catalog", req: &args.Request{Native: natOf("alpha", "gpt-x", "")},
			want: []string{"alpha@"}, modelPass: true, dropped: []Drop{{CLI: "beta", Reason: "native model gpt-x"}}},
		{name: "model and effort outside the catalog", req: &args.Request{Native: natOf("alpha", "gpt-x", "turbo")},
			want: []string{"alpha@"}, modelPass: true, effortPass: true, efforts: map[string]string{"alpha@": "turbo"},
			dropped: []Drop{{CLI: "beta", Reason: "native model gpt-x, effort turbo"}}},
		{name: "effort outside the catalog keeps each model once", req: &args.Request{Native: natOf("alpha", "", "turbo")},
			want: []string{"strong", "fast"}, effortPass: true,
			efforts: map[string]string{"strong": "turbo", "fast": "turbo"},
			dropped: []Drop{{CLI: "beta", Reason: "native effort turbo"}}},
		{name: "catalog model with an effort it lacks", req: &args.Request{Native: natOf("alpha", "strong-model", "turbo")},
			want: []string{"strong"}, effortPass: true, efforts: map[string]string{"strong": "turbo"},
			dropped: []Drop{{CLI: "beta", Reason: "native model strong-model, effort turbo"}}},
		{name: "a model of another CLI is passed through by name", req: &args.Request{Native: natOf("alpha", "worker-model", "")},
			want: []string{"alpha@"}, modelPass: true, dropped: []Drop{{CLI: "beta", Reason: "native model worker-model"}}},
		{name: "a shared template keeps both CLIs for the cli stage", req: &args.Request{Native: natAll("", "high")},
			want: []string{"strong@high", "worker@high"}},
		{name: "a catalog model read through a shared template pins its CLI", req: &args.Request{Native: natAll("strong-model", "")},
			want: []string{"strong@low", "strong@high"}, dropped: []Drop{{CLI: "beta", Reason: "native model strong-model"}}},
		{name: "a shared template with a catalog model and effort pins its CLI", req: &args.Request{Native: natAll("worker-model", "high")},
			want: []string{"worker@high"}, dropped: []Drop{{CLI: "alpha", Reason: "native model worker-model"}}},
		{name: "each CLI with its own catalog model stays", req: &args.Request{Native: map[string]args.Selection{
			"alpha": {Model: "strong-model"}, "beta": {Model: "worker-model"}}},
			want: []string{"strong@low", "strong@high", "worker@low", "worker@high"}},
		{name: "a catalog model drops an effort-only CLI", req: &args.Request{Native: map[string]args.Selection{
			"alpha": {Model: "strong-model"}, "beta": {Effort: "high"}}},
			want: []string{"strong@low", "strong@high"}, dropped: []Drop{{CLI: "beta", Reason: "native model strong-model"}}},
		{name: "a pinned CLI only reads its own selection", req: &args.Request{CLI: "alpha", Native: natOf("beta", "worker-model", "high")},
			want: []string{"strong@low", "strong@high", "fast"}, dropped: []Drop{{CLI: "beta", Reason: "--cli alpha"}}},
		{name: "a pinned CLI with its selection", req: &args.Request{CLI: "alpha", Native: natAll("fast-model", "high")},
			want: []string{"fast"}, effortPass: true, efforts: map[string]string{"fast": "high"},
			dropped: []Drop{{CLI: "beta", Reason: "--cli alpha"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := Eligible(cat, tc.req)
			assert.Equal(t, tc.want, ids(e.Options))
			assert.Equal(t, tc.modelPass, e.ModelPassthrough)
			assert.Equal(t, tc.effortPass, e.EffortPassthrough)
			assert.Equal(t, tc.dropped, e.Dropped)
			for _, o := range e.Options {
				want := o.Effort
				if v, ok := tc.efforts[o.ID]; ok {
					want = v
				}
				assert.Equal(t, want, e.EffortFor(o), o.ID)
			}
		})
	}
}

func TestEligibleNativeEffortStaysWithItsCLI(t *testing.T) {
	_, cat := requestFixture(t)
	e := Eligible(cat, &args.Request{Native: map[string]args.Selection{
		"alpha": {Effort: "turbo"}, "beta": {Effort: "high"}}})
	assert.Equal(t, []string{"strong", "fast", "worker@high"}, ids(e.Options))
	for _, o := range e.Options {
		switch o.CLI {
		case "alpha":
			assert.Equal(t, "turbo", e.EffortFor(o), o.ID)
		case "beta":
			assert.Equal(t, "high", e.EffortFor(o), o.ID)
		}
	}
}

func TestEligiblePassthroughIDNeverCollides(t *testing.T) {
	_, cat := requestFixture(t)
	e := Eligible(cat, &args.Request{CLI: "beta", Native: natAll("other-model", "")})
	require.Len(t, e.Options, 1)
	id := e.Options[0].ID
	assert.Equal(t, catalog.PassthroughID("beta"), id)
	for _, o := range cat.Options {
		assert.NotEqual(t, id, o.ID, "a catalog option id equals the pass-through id")
	}
	// excluding a catalog option named like the cli leaves the other CLI's pass-through option
	e = Eligible(cat, &args.Request{Native: map[string]args.Selection{"alpha": {Model: "other-model"}}})
	e.Exclude("alpha", []string{"alpha"})
	assert.Equal(t, []string{catalog.PassthroughID("alpha")}, ids(e.Options))
}

func TestEligibilityExcludeIsScopedToCLI(t *testing.T) {
	_, cat := requestFixture(t)
	// alpha's native model is outside the catalog, beta only fixes an effort; both carry an option
	// whose id can be spelled the same way, so only the CLI tells them apart
	e := Eligible(cat, &args.Request{Native: map[string]args.Selection{
		"alpha": {Model: "other-model"},
		"beta":  {Effort: "low"},
	}})
	require.Contains(t, catalog.CLIs(e.Options), "alpha")
	e.Exclude("beta", ids(catalog.ByCLI(e.Options, "beta")))
	assert.NotEmpty(t, catalog.ByCLI(e.Options, "alpha"), "blocking beta must not remove alpha")
	assert.Empty(t, catalog.ByCLI(e.Options, "beta"))

	// the same id under another CLI is not touched
	e = Eligible(cat, &args.Request{})
	e.Exclude("beta", []string{"strong@low"})
	assert.Contains(t, ids(e.Options), "strong@low")
}

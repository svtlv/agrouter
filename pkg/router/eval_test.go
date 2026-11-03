//go:build eval

package router

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/svtlv/agrouter/pkg/catalog"
	"github.com/svtlv/agrouter/pkg/classifier"
	"github.com/svtlv/agrouter/pkg/config"
	"github.com/svtlv/agrouter/pkg/config/defaults"
)

// evalConfig loads the layers the app does: the embedded defaults, the global config
// (AGROUTER_CONFIG_DIR or ~/.config/agrouter) and the repository's local .agrouter/config. It only
// reads them, so the provider, its section and the catalog are the ones a real run would use.
func evalConfig(t *testing.T) (*config.Config, *catalog.Catalog) {
	t.Helper()
	global, local, err := config.DefaultPaths(filepath.Join("..", ".."))
	require.NoError(t, err)
	cfg, err := config.Load(config.Sources{Embedded: defaults.Config, GlobalPath: global, LocalPath: local})
	require.NoError(t, err)
	cat, err := catalog.Build(cfg)
	require.NoError(t, err)
	return cfg, cat
}

// TestEvalRouting routes every case of testdata/routing against the real classifier and reports accuracy,
// latency per case and in total, and the confidence distribution of each routing stage, which is
// conditional on the stages before it. It reports rather than fails on accuracy: run it with
// make eval-routing (needs AGROUTER_CLASSIFIER_API_KEY), never in CI.
func TestEvalRouting(t *testing.T) {
	key := os.Getenv(config.EnvAPIKey)
	if key == "" {
		t.Skip(config.EnvAPIKey + " is not set")
	}
	cfg, cat := evalConfig(t)
	client, err := classifier.New(cfg.Classifier.Provider, cfg.Classifier.URL, key)
	require.NoError(t, err)
	client.LongContext = cfg.Classifier.LongContext
	t.Logf("classifier provider %s, model %s", cfg.Classifier.Provider, cfg.Classifier.Model)
	r, err := New(cfg, cat, client)
	require.NoError(t, err)
	cases, err := loadEvalCases(evalDir, cat)
	require.NoError(t, err)

	results := make([]evalResult, 0, len(cases))
	for _, c := range cases {
		results = append(results, runEvalCase(context.Background(), r, cat, c, t.TempDir()))
	}
	for _, line := range resultLines(results) {
		t.Log(line)
	}
	t.Log(scoreEval(results))
}

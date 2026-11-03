package defaults_test

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/svtlv/agrouter/pkg/classifier"
	"github.com/svtlv/agrouter/pkg/config"
	"github.com/svtlv/agrouter/pkg/config/defaults"
)

func TestEmbeddedLoadsAndValidates(t *testing.T) {
	_, err := config.Load(config.Sources{Embedded: defaults.Config})
	require.NoError(t, err)
}

// every provider the classifier package supports has a section in the embedded config that validates
// when a layer selects it, and selecting it makes it the classifier the config hands out
func TestEmbeddedValidatesWithEachProvider(t *testing.T) {
	for _, provider := range classifier.Providers() {
		t.Run(provider, func(t *testing.T) {
			local := filepath.Join(t.TempDir(), "config")
			require.NoError(t, os.WriteFile(local, []byte("[agrouter]\nclassifier_provider = "+provider+"\n"), 0o600))

			cfg, err := config.Load(config.Sources{Embedded: defaults.Config, LocalPath: local})
			require.NoError(t, err)
			assert.Equal(t, provider, cfg.Agrouter.ClassifierProvider)
			assert.Equal(t, provider, cfg.Classifier.Provider)
		})
	}
}

// TestNoInlineComments guards against a trailing "; ..." or "# ..." on a value line: with
// IgnoreInlineComment it would silently become part of the value.
func TestNoInlineComments(t *testing.T) {
	inline := regexp.MustCompile(`\s[;#]`)
	sc := bufio.NewScanner(bytes.NewReader(defaults.Config))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' || line[0] == '[' {
			continue
		}
		_, value, ok := strings.Cut(line, "=")
		require.True(t, ok, "line %d is neither a comment, a section nor a key: %q", n, line)
		assert.False(t, inline.MatchString(value), "line %d carries an inline comment: %q", n, line)
	}
	require.NoError(t, sc.Err())
}

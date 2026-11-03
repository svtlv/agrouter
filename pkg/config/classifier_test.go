package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/svtlv/agrouter/pkg/classifier"
)

const (
	selectOpenAI   = "[agrouter]\nclassifier_provider = openai\n"
	selectTypeSafe = "[agrouter]\nclassifier_provider = typesafe\n"
)

func TestLoad_ClassifierProviderSelection(t *testing.T) {
	tests := []struct {
		name          string
		global, local string
		want          Classifier
		wantProvider  string
	}{
		{
			name:         "typesafe when no layer sets classifier_provider",
			wantProvider: classifier.ProviderTypeSafe,
			want: Classifier{
				Provider: classifier.ProviderTypeSafe, URL: "https://classifier.example.test/v1",
				APIKeySource: LayerEmbedded, Model: "test-classifier", RequestTokens: 20000, BytesPerToken: 3,
			},
		},
		{
			name:         "global selects openai",
			global:       selectOpenAI,
			wantProvider: classifier.ProviderOpenAI,
			want: Classifier{
				Provider: classifier.ProviderOpenAI, URL: "https://openai.example.test/v1/decisions",
				APIKeySource: LayerEmbedded, Model: "test-openai", RequestTokens: 200000, BytesPerToken: 2.5,
			},
		},
		{
			name:         "local overrides the global selection",
			global:       selectOpenAI,
			local:        selectTypeSafe,
			wantProvider: classifier.ProviderTypeSafe,
			want: Classifier{
				Provider: classifier.ProviderTypeSafe, URL: "https://classifier.example.test/v1",
				APIKeySource: LayerEmbedded, Model: "test-classifier", RequestTokens: 20000, BytesPerToken: 3,
			},
		},
		{
			name:         "a section listed before the selection is still the selected one",
			local:        "[classifier.openai]\nmodel = local-openai\n[agrouter]\nclassifier_provider = openai\n",
			wantProvider: classifier.ProviderOpenAI,
			want: Classifier{
				Provider: classifier.ProviderOpenAI, URL: "https://openai.example.test/v1/decisions",
				APIKeySource: LayerEmbedded, Model: "local-openai", RequestTokens: 200000, BytesPerToken: 2.5,
			},
		},
		{
			name:         "a local override of one key keeps the others of the provider",
			global:       selectOpenAI + "[classifier.openai]\napi_key = global-openai-key\nurl = https://global.example.test/v1\n",
			local:        "[classifier.openai]\nrequest_tokens = 90000\n",
			wantProvider: classifier.ProviderOpenAI,
			want: Classifier{
				Provider: classifier.ProviderOpenAI, URL: "https://global.example.test/v1",
				APIKey: "global-openai-key", APIKeySource: LayerGlobal, Model: "test-openai",
				RequestTokens: 90000, BytesPerToken: 2.5,
			},
		},
		{
			name:         "the other provider's keys never reach the selected section",
			global:       "[classifier.typesafe]\napi_key = typesafe-key\nrequest_tokens = 12345\nlong_context = true\n",
			local:        selectOpenAI,
			wantProvider: classifier.ProviderOpenAI,
			want: Classifier{
				Provider: classifier.ProviderOpenAI, URL: "https://openai.example.test/v1/decisions",
				APIKeySource: LayerEmbedded, Model: "test-openai", RequestTokens: 200000, BytesPerToken: 2.5,
			},
		},
		{
			name:         "long_context stays a typesafe key",
			local:        "[classifier.typesafe]\nlong_context = true\n",
			wantProvider: classifier.ProviderTypeSafe,
			want: Classifier{
				Provider: classifier.ProviderTypeSafe, URL: "https://classifier.example.test/v1",
				APIKeySource: LayerEmbedded, Model: "test-classifier", LongContext: true,
				RequestTokens: 20000, BytesPerToken: 3,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := load(t, tc.global, tc.local)
			require.NoError(t, err)
			assert.Equal(t, tc.wantProvider, cfg.Agrouter.ClassifierProvider)
			assert.Equal(t, tc.want, cfg.Classifier)
		})
	}
}

func TestLoad_ClassifierProviderErrors(t *testing.T) {
	tests := []struct {
		name    string
		local   string
		want    []string
		notWant []string
	}{
		{
			name:    "unknown provider value lists the valid ones",
			local:   "[agrouter]\nclassifier_provider = azure\n",
			want:    []string{`[agrouter] classifier_provider = "azure": unknown provider (use openai or typesafe)`},
			notWant: []string{"must not be empty"},
		},
		{
			name:  "an empty provider is not the default",
			local: "[agrouter]\nclassifier_provider =\n",
			want:  []string{`[agrouter] classifier_provider = "": unknown provider`},
		},
		{
			name:  "provider names are exact",
			local: "[agrouter]\nclassifier_provider = OpenAI\n",
			want:  []string{`[agrouter] classifier_provider = "OpenAI": unknown provider`},
		},
		{
			name:  "unknown provider section",
			local: "[classifier.azure]\nurl = https://azure.example.test\n",
			want:  []string{"[classifier.azure]: unknown section (use [classifier.openai] or [classifier.typesafe])"},
		},
		{
			name:  "a subsection of a provider section",
			local: "[classifier.typesafe.extra]\nurl = x\n",
			want:  []string{"[classifier.typesafe.extra]: unknown section"},
		},
		{
			name:  "an empty provider section name",
			local: "[classifier.]\nurl = x\n",
			want:  []string{"[classifier.]: unknown section"},
		},
		{
			name:    "long_context under a provider without a long-context mode",
			local:   "[classifier.openai]\nlong_context = hidden-value\n",
			want:    []string{"[classifier.openai] long_context (local ", "unknown key"},
			notWant: []string{"hidden-value"},
		},
		{
			name:  "long_context false is no more accepted there",
			local: "[classifier.openai]\nlong_context = false\n",
			want:  []string{"[classifier.openai] long_context (local ", "unknown key"},
		},
		{
			name:    "the flat section carries the migration hint",
			local:   "[classifier]\nurl = https://classifier.example.test/v1\napi_key = sk-secret\n",
			want:    []string{"[classifier]: removed; use [classifier.<provider>] and [agrouter] classifier_provider"},
			notWant: []string{"sk-secret"},
		},
		{
			name:  "an empty flat section is removed too",
			local: "[classifier]\n",
			want:  []string{"[classifier]: removed"},
		},
		{
			name:  "the unselected section is decoded strictly",
			local: "[classifier.openai]\nrequest_tokens = many\n",
			want:  []string{`[classifier.openai] request_tokens = "many" (local `},
		},
		{
			name:  "unknown key in the unselected section",
			local: "[classifier.openai]\nendpoint = x\n",
			want:  []string{"[classifier.openai] endpoint (local ", "unknown key"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(t, "", tc.local)
			require.Error(t, err)
			for _, w := range tc.want {
				assert.Contains(t, err.Error(), w)
			}
			for _, w := range tc.notWant {
				assert.NotContains(t, err.Error(), w)
			}
		})
	}
}

func TestLoad_FlatClassifierSectionInAnyLayer(t *testing.T) {
	flat := "[classifier]\nmodel = old\n"
	_, err := Load(Sources{Embedded: []byte(baseConfig + flat)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "[classifier]: removed")

	_, err = load(t, flat, "")
	require.Error(t, err, "global")
	assert.Contains(t, err.Error(), "[classifier]: removed")
}

// only the selected provider's required and sizing keys are enforced
func TestLoad_SelectedProviderValidated(t *testing.T) {
	tests := []struct {
		name     string
		local    string
		wantErr  string
		notNamed string // the other provider's section, which must not be reported
	}{
		{name: "unselected section with an empty url", local: "[classifier.openai]\nurl =\n"},
		{name: "unselected section with a bad budget", local: "[classifier.openai]\nrequest_tokens = 0\nmodel =\n"},
		{
			name:    "the same empty url under the selected provider",
			local:   selectOpenAI + "[classifier.openai]\nurl =\n",
			wantErr: "[classifier.openai] url: must not be empty", notNamed: "[classifier.typesafe]",
		},
		{
			name:    "sizing keys are named after the selected section",
			local:   selectOpenAI + "[classifier.openai]\nrequest_tokens = 0\nbytes_per_token = -1\nmodel =\n",
			wantErr: "[classifier.openai] request_tokens = 0: must be positive", notNamed: "[classifier.typesafe]",
		},
		{
			name:    "the typesafe section is checked when it is selected",
			local:   "[classifier.typesafe]\nmodel =\n",
			wantErr: "[classifier.typesafe] model: must not be empty", notNamed: "[classifier.openai]",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(t, "", tc.local)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.NotContains(t, err.Error(), tc.notNamed, "only the selected section is reported")
		})
	}
}

func TestValidate_SelectedProviderWithoutSection(t *testing.T) {
	embedded := "[agrouter]\ntimeout = 10s\nrouting_policy = cheap\ncomplexity_policy = whole\nlog_dir = logs\n" +
		"[classifier.typesafe]\nurl = https://classifier.example.test/v1\nmodel = m\nrequest_tokens = 1000\nbytes_per_token = 3\n"
	_, err := Load(Sources{Embedded: []byte(embedded)})
	require.NoError(t, err, "the default provider has its section")

	local := writeFile(t, t.TempDir(), "local/.agrouter/config", selectOpenAI)
	_, err = Load(Sources{Embedded: []byte(embedded), LocalPath: local})
	require.Error(t, err)
	for _, want := range []string{
		"[classifier.openai] url: must not be empty",
		"[classifier.openai] model: must not be empty",
		"[classifier.openai] request_tokens = 0: must be positive",
		"[classifier.openai] bytes_per_token = 0: must be a finite number greater than zero",
	} {
		assert.Contains(t, err.Error(), want)
	}
}

// ResolveAPIKey reads the selected provider's api_key, from whichever layer set it
func TestResolveAPIKey_SelectedSection(t *testing.T) {
	both := "[classifier.typesafe]\napi_key = typesafe-key\n[classifier.openai]\napi_key = openai-key\n"
	tests := []struct {
		name       string
		global     string
		local      string
		env        string
		wantKey    string
		wantSource string
	}{
		{name: "typesafe by default", global: both, wantKey: "typesafe-key", wantSource: LayerGlobal},
		{name: "openai when selected", global: both, local: selectOpenAI, wantKey: "openai-key", wantSource: LayerGlobal},
		{
			name: "a local key of the selected section wins", global: both + selectOpenAI,
			local: "[classifier.openai]\napi_key = local-openai\n", wantKey: "local-openai", wantSource: LayerLocal,
		},
		{
			name: "a local key of the other section does not", global: both + selectOpenAI,
			local: "[classifier.typesafe]\napi_key = local-typesafe\n", wantKey: "openai-key", wantSource: LayerGlobal,
		},
		{name: "the env key applies to the selected provider", global: both, local: selectOpenAI, env: "env-key", wantKey: "env-key", wantSource: LayerEnv},
		{name: "no key set for the selected provider", global: "[classifier.typesafe]\napi_key = typesafe-key\n", local: selectOpenAI, wantKey: "", wantSource: LayerEmbedded},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := load(t, tc.global, tc.local)
			require.NoError(t, err)
			key, source := ResolveAPIKey("", false, tc.env, cfg.Classifier)
			assert.Equal(t, tc.wantKey, key)
			assert.Equal(t, tc.wantSource, source)
		})
	}
}

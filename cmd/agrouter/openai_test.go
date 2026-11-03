package main

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/svtlv/agrouter/pkg/classifier"
	"github.com/svtlv/agrouter/pkg/router"
	"github.com/svtlv/agrouter/pkg/runner"
)

// fakeOpenAI is a Decisions API server for the end-to-end tests: it answers every choice question
// with pick's part (as fakeJev does), every predicate with 0.5 and a score with a level normalized to
// 0 to 10, in OpenAI's answer shape, and records each request's input text, bearer key and stage.
type fakeOpenAI struct {
	t      *testing.T
	mu     sync.Mutex
	pick   string
	level  string
	inputs []string
	keys   []string
	stages []string
	models []string
	srv    *httptest.Server
}

func newFakeOpenAI(t *testing.T) *fakeOpenAI {
	f := &fakeOpenAI{t: t, level: "7"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOpenAI) handle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model     string `json:"model"`
		Input     string `json:"input"`
		Questions []struct {
			Name         string `json:"name"`
			Type         string `json:"type"`
			Instructions string `json:"instructions"`
			Choices      []struct {
				Value string `json:"value"`
			} `json:"choices"`
			Levels []struct {
				Label string `json:"label"`
			} `json:"levels"`
		} `json:"questions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Errorf("fake openai: decode request: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.inputs = append(f.inputs, req.Input)
	f.keys = append(f.keys, r.Header.Get("Authorization"))
	f.models = append(f.models, req.Model)
	pick, level := f.pick, f.level
	f.mu.Unlock()

	answers := make([]map[string]any, 0, len(req.Questions))
	for _, q := range req.Questions {
		switch q.Type {
		case "predicate":
			answers = append(answers, map[string]any{"name": q.Name, "type": q.Type, "predicate": map[string]any{"probability": 0.5}})
		case "score":
			rating, err := strconv.ParseFloat(level, 64)
			if err != nil {
				f.t.Errorf("fake openai: parse score level: %v", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			score := rating * float64(len(q.Levels)-1) / 10
			low := int(math.Floor(score))
			probs := make([]map[string]any, 0, len(q.Levels))
			for i, lv := range q.Levels {
				p := 0.0
				switch i {
				case low:
					p = 1 - (score - float64(low))
				case low + 1:
					p = score - float64(low)
				}
				probs = append(probs, map[string]any{"value": lv.Label, "probability": p})
			}
			answers = append(answers, map[string]any{"name": q.Name, "type": q.Type, "score": score, "probabilities": probs, "confidence": 0.9})
		case "choice":
			choice, err := f.choose(q.Name, pick, q.Instructions)
			if err != nil {
				f.t.Errorf("fake openai: %s stage: %v", q.Name, err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			probs := make([]map[string]any, 0, len(q.Choices))
			found := false
			for _, c := range q.Choices {
				p := 0.0
				if c.Value == choice {
					p, found = 1, true
				}
				probs = append(probs, map[string]any{"value": c.Value, "probability": p})
			}
			if !found {
				f.t.Errorf("fake openai: %s stage: pick %q (%q) is not among the choices sent", q.Name, pick, choice)
			}
			answers = append(answers, map[string]any{"name": q.Name, "type": q.Type, "choice": choice, "probabilities": probs, "confidence": 0.9})
			f.mu.Lock()
			f.stages = append(f.stages, q.Name)
			f.mu.Unlock()
		default:
			f.t.Errorf("fake openai: unexpected question type %q", q.Type)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"answers": answers, "usage": map[string]any{"input_tokens": 100}})
}

// choose is pick's choice for the stage asked under id; the instructions arrive as JSON text.
func (f *fakeOpenAI) choose(id, pick, instructions string) (string, error) {
	return stagePick(id, pick, json.RawMessage(instructions))
}

func (f *fakeOpenAI) seen() (inputs, keys, stages []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.inputs), slices.Clone(f.keys), slices.Clone(f.stages)
}

// openaiApp is an invocation of the environment whose classifier client is built by the app for the
// configured provider but points at the fake OpenAI server. The provider it was asked for is recorded.
func (e *env) openaiApp(f *fakeOpenAI, providers *[]string, stdout, stderr io.Writer) *app {
	a := e.app(nil, stdout, stderr, runner.Runner{})
	a.newClassifier = func(provider, _, key string, longContext bool) (router.Classifier, error) {
		*providers = append(*providers, provider)
		return newClassifierClient(provider, f.srv.URL, key, longContext)
	}
	return a
}

func TestApp_OpenAIProvider(t *testing.T) {
	t.Run("decision JSON is printed", func(t *testing.T) {
		e := newEnv(t)
		e.globalConfig("[agrouter]\nclassifier_provider = openai\n")
		f := newFakeOpenAI(t)
		f.pick = "claude-sonnet-5-5@low"
		var stdout, stderr bytes.Buffer
		var providers []string

		code := e.openaiApp(f, &providers, &stdout, &stderr).run([]string{"fix the typo in README"})

		require.Equal(t, 0, code, stderr.String())
		assert.Empty(t, stderr.String())
		assert.Equal(t, map[string]any{
			"cli": "claude", "model": "claude-sonnet-5-5", "effort": "low",
			"confidence": map[string]any{"cli": 0.9, "model": 0.9, "effort": 0.9},
		}, decision(t, stdout.String()))
		assert.Equal(t, []string{classifier.ProviderOpenAI}, providers)
		inputs, keys, stages := f.seen()
		assert.Equal(t, repeated(`{"prompt":"fix the typo in README"}`, 3), inputs, "the state is the input text of every stage request")
		assert.Equal(t, repeated("Bearer "+testKey, 3), keys)
		assert.Equal(t, []string{"cli", "model", "effort"}, stages)
	})

	t.Run("the selected section's model and key reach the request", func(t *testing.T) {
		e := newEnv(t)
		e.globalConfig("[agrouter]\nclassifier_provider = openai\n[classifier.openai]\nmodel = synthetic-decider\n")
		f := newFakeOpenAI(t)
		f.pick = "claude-sonnet-5-5@low"
		var stdout, stderr bytes.Buffer
		var providers []string

		code := e.openaiApp(f, &providers, &stdout, &stderr).run([]string{"fix the typo in README"})

		require.Equal(t, 0, code, stderr.String())
		assert.Equal(t, repeated("synthetic-decider", 3), f.models)
	})

	t.Run("exec routes and starts the child", func(t *testing.T) {
		e := newEnv(t)
		exe := helperCommand(t)
		e.globalConfig("[agrouter]\nclassifier_provider = openai\n[cli.claude]\ncommand = " + exe + "\n[cli.codex]\ncommand = " + exe + "\n")
		f := newFakeOpenAI(t)
		f.pick = "claude-sonnet-5-5@medium"
		var stdout, stderr bytes.Buffer
		var providers []string

		code := e.openaiApp(f, &providers, &stdout, &stderr).run([]string{"exec", "claude", "plan the migration"})

		require.Equal(t, 0, code, stderr.String())
		assert.Equal(t, map[string]any{"cli": "claude", "model": "claude-sonnet-5-5", "effort": "medium",
			"confidence": map[string]any{"model": 0.9, "effort": 0.9}}, selectionLog(t, stderr.String()))
		_, _, stages := f.seen()
		assert.Equal(t, []string{"model", "effort"}, stages, "exec fixes the cli stage")
	})

	t.Run("no classifier_provider selects the default, TypeSafe", func(t *testing.T) {
		e := newEnv(t)
		e.jev.pick = "claude-sonnet-5-5@low"
		var stdout, stderr bytes.Buffer
		a := e.app(nil, &stdout, &stderr, runner.Runner{})
		var providers []string
		factory := a.newClassifier
		a.newClassifier = func(provider, url, key string, longContext bool) (router.Classifier, error) {
			providers = append(providers, provider)
			return factory(provider, url, key, longContext)
		}

		code := a.run([]string{"fix the typo in README"})

		require.Equal(t, 0, code, stderr.String())
		assert.Equal(t, []string{classifier.ProviderTypeSafe}, providers)
		assert.Equal(t, repeated(`{"prompt":"fix the typo in README"}`, 3), e.jev.requests(), "TypeSafe's wire body, not OpenAI's")
	})

	t.Run("an unselected provider's section is not enforced", func(t *testing.T) {
		e := newEnv(t)
		e.globalConfig("[classifier.openai]\nurl =\n")
		e.jev.pick = "claude-sonnet-5-5@low"

		r := e.run([]string{"fix the typo in README"}, nil)

		require.Equal(t, 0, r.code, r.stderr)
		assert.Equal(t, "claude-sonnet-5-5", decision(t, r.stdout)["model"])
	})
}

func TestApp_ClassifierProviderErrors(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   string
	}{
		{name: "unknown provider", config: "[agrouter]\nclassifier_provider = nowhere\n", want: "classifier_provider"},
		{name: "long_context under openai", config: "[classifier.openai]\nlong_context = true\n", want: "long_context"},
		{name: "flat classifier section", config: "[classifier]\nurl = https://classifier.example.test\n", want: "[classifier.<provider>]"},
		{name: "request_tokens over the openai limit", config: "[agrouter]\nclassifier_provider = openai\n[classifier.openai]\nrequest_tokens = 950000\n", want: "[classifier.openai]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.globalConfig(tt.config)

			r := e.run([]string{"fix the typo in README"}, nil)

			assert.Equal(t, exitUsage, r.code, r.stderr)
			assert.Contains(t, r.stderr, tt.want)
			assert.Empty(t, r.stdout)
			assert.Empty(t, e.jev.requests(), "no classifier request")
		})
	}
}

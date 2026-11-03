package classifier

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// assertGolden compares got with testdata/name, or rewrites the file when -update is set. Line
// endings are LF on every platform (.gitattributes), so the comparison is byte for byte.
func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		require.NoError(t, os.MkdirAll("testdata", 0o750))
		require.NoError(t, os.WriteFile(path, got, 0o600))
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // test fixture path
	require.NoError(t, err, "missing golden file; run the test with -update")
	assert.Equal(t, string(want), string(got), "run the test with -update to accept the change")
}

// sentRequest is what a client put on the wire: the provider's header switches and the exact body.
type sentRequest struct {
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

// goldenRequest is a synthetic request touching every question type, both kinds of instructions
// and criteria, and characters that JSON encoding escapes.
func goldenRequest() Request {
	return Request{
		Model: "synthetic-classifier",
		State: map[string]any{
			"prompt": "fix <the> \"parser\" & its tests\nthen stop",
			"docs":   []string{"first", "second"},
		},
		Questions: map[string]Question{
			"route": {
				Type:         TypeChoice,
				Instructions: map[string]any{"question": "which option?", "policy": "prefer cheap"},
				Criteria: Criteria{
					{Name: "b@high", Value: map[string]string{"cli": "x", "model": "b", "effort": "high"}},
					{Name: "a", Value: "plain description"},
					{Name: "c", Value: []string{"one", "two"}},
				},
			},
			"relevance": {
				Type:         TypeNoul,
				Instructions: "does the chunk matter?",
				Criteria:     Criteria{{Name: "true", Value: "it changes the plan"}, {Name: "false", Value: "it does not"}},
			},
			"complexity": {
				Type:         TypeScore,
				Instructions: "how hard is the project?",
				Levels:       []string{"trivial", "moderate", "hard"},
			},
		},
	}
}

// capture asks goldenRequest through the client newClient builds for an httptest server and returns
// the headers and body that server received. The server answers with a body no question accepts:
// the request is what matters here, so Ask's error is ignored.
func capture(t *testing.T, newClient func(url string) *Client, headers []string) sentRequest {
	t.Helper()
	var got sentRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		got.Body = body
		got.Headers = map[string]string{}
		for _, h := range headers {
			got.Headers[h] = r.Header.Get(h)
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	_, _ = newClient(srv.URL).Ask(t.Context(), goldenRequest())
	require.NotEmpty(t, got.Body, "the client sent no request")
	return got
}

// TestWireGoldenTypeSafe pins the bytes and headers TypeSafe receives, with and without the
// long-context mode.
func TestWireGoldenTypeSafe(t *testing.T) {
	sent := map[string]sentRequest{}
	for name, longContext := range map[string]bool{"default": false, "long_context": true} {
		sent[name] = capture(t, func(url string) *Client {
			c := mustNew(t, url)
			c.LongContext = longContext
			return c
		}, []string{longContextHeader})
	}
	data, err := json.MarshalIndent(sent, "", "  ")
	require.NoError(t, err)
	assertGolden(t, "typesafe_request.json", append(data, '\n'))

	// the golden is indented for review; the body on the wire is compact
	for name, s := range sent {
		assert.False(t, bytes.ContainsAny(s.Body, "\n\t"), "%s body is compact", name)
	}
}

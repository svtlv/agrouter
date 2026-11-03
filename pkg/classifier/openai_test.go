package classifier

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustNewOpenAI(t *testing.T, url string) *Client {
	t.Helper()
	c, err := New(ProviderOpenAI, url, testKey)
	require.NoError(t, err)
	return c
}

// TestWireGoldenOpenAI pins the bytes OpenAI receives for a request with a choice, a noul and a
// score, and that no provider header is sent with or without the long-context setting.
func TestWireGoldenOpenAI(t *testing.T) {
	sent := map[string]sentRequest{}
	for name, longContext := range map[string]bool{"default": false, "long_context": true} {
		sent[name] = capture(t, func(url string) *Client {
			c := mustNewOpenAI(t, url)
			c.LongContext = longContext
			return c
		}, []string{longContextHeader})
	}
	assert.Equal(t, sent["default"], sent["long_context"], "the setting does not change the request")

	data, err := json.MarshalIndent(sent["default"], "", "  ")
	require.NoError(t, err)
	assertGolden(t, "openai_request.json", append(data, '\n'))
	assert.False(t, bytes.ContainsAny(sent["default"].Body, "\n\t"), "the body on the wire is compact")
}

// encodeOpenAI is the decoded body encode builds for req.
func encodeOpenAI(t *testing.T, req Request) map[string]any {
	t.Helper()
	data, err := openaiWire{}.encode(req)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(data, &body))
	return body
}

func TestOpenAIEncodeInput(t *testing.T) {
	tests := []struct {
		name  string
		state any
		want  string
	}{
		{name: "object with sorted keys", state: map[string]any{"b": 1, "a": []string{"x"}}, want: `{"a":["x"],"b":1}`},
		{name: "html and quotes stay as they are", state: map[string]string{"p": `<a> & "b"`}, want: `{"p":"<a> & \"b\""}`},
		{name: "string state is JSON text too", state: "plain", want: `"plain"`},
		{name: "nil state", state: nil, want: `null`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := encodeOpenAI(t, Request{Model: "m", State: tt.state})
			assert.Equal(t, "m", body["model"])
			assert.Equal(t, tt.want, body["input"], "the state is one string, never an object")
			assert.Empty(t, body["questions"], "no questions is an empty array")
			assert.NotContains(t, body, "state")
			assert.NotContains(t, body, "input_json")
		})
	}
}

func TestOpenAIEncodeQuestions(t *testing.T) {
	type obj = map[string]any
	tests := []struct {
		name string
		q    Question
		want obj
	}{
		{
			name: "choice keeps criteria order and text forms",
			q: Question{Type: TypeChoice, Instructions: "which?", Criteria: Criteria{
				{Name: "b", Value: "plain"},
				{Name: "a", Value: map[string]string{"y": "2", "x": "<1>"}},
				{Name: "c", Value: []string{"one", "two"}},
			}},
			want: obj{"name": "q", "type": "choice", "instructions": "which?", "choices": []any{
				obj{"value": "b", "description": "plain"},
				obj{"value": "a", "description": `{"x":"<1>","y":"2"}`},
				obj{"value": "c", "description": `["one","two"]`},
			}},
		},
		{
			name: "object instructions become compact JSON text",
			q: Question{Type: TypeChoice, Instructions: struct {
				Question string `json:"question"`
				Policy   string `json:"policy"`
			}{"which?", "cheap"}, Criteria: Criteria{{Name: "a", Value: "d"}}},
			want: obj{"name": "q", "type": "choice", "instructions": `{"question":"which?","policy":"cheap"}`, "choices": []any{
				obj{"value": "a", "description": "d"},
			}},
		},
		{
			name: "noul is a predicate with the criteria in the instructions",
			q: Question{Type: TypeNoul, Instructions: "does it matter?", Criteria: Criteria{
				{Name: "true", Value: "it changes the plan"}, {Name: "false", Value: "it does not"},
			}},
			want: obj{"name": "q", "type": "predicate", "instructions": "does it matter?\ntrue when: it changes the plan\nfalse when: it does not"},
		},
		{
			name: "noul with object instructions and one criterion",
			q:    Question{Type: TypeNoul, Instructions: obj{"q": "x"}, Criteria: Criteria{{Name: "false", Value: obj{"k": "v"}}}},
			want: obj{"name": "q", "type": "predicate", "instructions": "{\"q\":\"x\"}\nfalse when: {\"k\":\"v\"}"},
		},
		{
			name: "noul without instructions or criteria",
			q:    Question{Type: TypeNoul},
			want: obj{"name": "q", "type": "predicate"},
		},
		{
			name: "score labels its levels from zero",
			q:    Question{Type: TypeScore, Instructions: "how hard?", Levels: []string{"easy", "mid", "hard"}},
			want: obj{"name": "q", "type": "score", "instructions": "how hard?", "levels": []any{
				obj{"label": "0", "description": "easy"},
				obj{"label": "1", "description": "mid"},
				obj{"label": "2", "description": "hard"},
			}},
		},
		{
			name: "missing instructions are left out",
			q:    Question{Type: TypeScore, Levels: []string{"only"}},
			want: obj{"name": "q", "type": "score", "levels": []any{obj{"label": "0", "description": "only"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := encodeOpenAI(t, Request{Model: "m", State: "s", Questions: map[string]Question{"q": tt.q}})
			assert.Equal(t, []any{tt.want}, body["questions"])
		})
	}
}

func TestOpenAIEncodeQuestionOrder(t *testing.T) {
	score := Question{Type: TypeScore, Levels: []string{"a"}}
	body := encodeOpenAI(t, Request{Model: "m", Questions: map[string]Question{"zeta": score, "alpha": score, "mid": score}})
	questions := body["questions"].([]any)
	names := make([]string, 0, len(questions))
	for _, q := range questions {
		names = append(names, q.(map[string]any)["name"].(string))
	}
	assert.Equal(t, []string{"alpha", "mid", "zeta"}, names, "sorted by id, with name = id")
}

func TestOpenAIEncodeDeterministic(t *testing.T) {
	first, err := openaiWire{}.encode(goldenRequest())
	require.NoError(t, err)
	for range 20 {
		again, err := openaiWire{}.encode(goldenRequest())
		require.NoError(t, err)
		require.Equal(t, string(first), string(again))
	}
}

func TestOpenAIEncodeErrors(t *testing.T) {
	bad := make(chan int) // json cannot marshal a channel
	tests := []struct {
		name    string
		req     Request
		wantErr string
	}{
		{
			name:    "unsupported question type",
			req:     Request{Questions: map[string]Question{"q": {Type: "ranking"}}},
			wantErr: `question "q": unsupported question type "ranking"`,
		},
		{
			name:    "empty question type",
			req:     Request{Questions: map[string]Question{"q": {}}},
			wantErr: `unsupported question type ""`,
		},
		{
			name: "unmarshalable choice criterion",
			req: Request{Questions: map[string]Question{"q": {Type: TypeChoice, Criteria: Criteria{
				{Name: "ok", Value: "fine"}, {Name: "broken", Value: bad},
			}}}},
			wantErr: `question "q": criterion "broken"`,
		},
		{
			name: "unmarshalable noul criterion",
			req: Request{Questions: map[string]Question{"q": {Type: TypeNoul, Criteria: Criteria{
				{Name: "true", Value: bad},
			}}}},
			wantErr: `question "q": criterion "true"`,
		},
		{
			name: "noul criterion that is neither true nor false",
			req: Request{Questions: map[string]Question{"q": {Type: TypeNoul, Criteria: Criteria{
				{Name: "maybe", Value: "x"},
			}}}},
			wantErr: `noul criterion "maybe" is not true or false`,
		},
		{
			name:    "unmarshalable instructions",
			req:     Request{Questions: map[string]Question{"q": {Type: TypeScore, Instructions: bad, Levels: []string{"a"}}}},
			wantErr: `question "q": instructions`,
		},
		{
			name:    "unmarshalable state",
			req:     Request{State: bad},
			wantErr: "state",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := openaiWire{}.encode(tt.req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Nil(t, data)
		})
	}
}

// TestAskOpenAIEncodeError is the encode failure through the client: no request is made.
func TestAskOpenAIEncodeError(t *testing.T) {
	c := mustNewOpenAI(t, "http://classifier.test")
	c.HTTPClient = nil // a request would panic
	_, err := c.Ask(t.Context(), Request{Questions: map[string]Question{"q": {Type: "ranking"}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encode request")
	assert.Contains(t, err.Error(), "unsupported question type")
}

func TestOpenAILimits(t *testing.T) {
	for _, longContext := range []bool{false, true} {
		perQuestion, perRequest := Limits(ProviderOpenAI, longContext)
		assert.Equal(t, 922_000, perQuestion)
		assert.Equal(t, 922_000, perRequest)
	}
	assert.False(t, SupportsLongContext(ProviderOpenAI))
	assert.Contains(t, Providers(), ProviderOpenAI)
}

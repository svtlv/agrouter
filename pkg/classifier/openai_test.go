package classifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// OpenAI answers in these shapes; a test composes a body from them and breaks one at a time.
const (
	oaRoute = `{"name":"route","type":"choice","choice":"a","probabilities":` +
		`[{"value":"a","probability":0.7},{"value":"b@high","probability":0.3}],"confidence":0.6}`
	oaRelevance  = `{"name":"relevance","type":"predicate","predicate":{"probability":0.8}}`
	oaComplexity = `{"name":"complexity","type":"score","score":1.4,"probabilities":` +
		`[{"value":"0","probability":0.1},{"value":"1","probability":0.5},{"value":"2","probability":0.4}],"confidence":0.5}`
)

// oaBody is a 200 body with the given answers and a usage count of 42.
func oaBody(answers ...string) string {
	return `{"answers":[` + strings.Join(answers, ",") + `],"usage":{"input_tokens":42}}`
}

// oaQuestions is one question of each type.
func oaQuestions() map[string]Question {
	return map[string]Question{
		"route":      routeQuestion(),
		"relevance":  relevanceQuestion(),
		"complexity": {Type: TypeScore, Instructions: "how hard?", Levels: []string{"trivial", "moderate", "hard"}},
	}
}

func oaRequest() Request {
	return Request{Model: "synthetic-openai", State: map[string]string{"prompt": "fix it"}, Questions: oaQuestions()}
}

func TestOpenAIDecodeValid(t *testing.T) {
	forty := 42
	want := Response{InputTokens: &forty, Answers: map[string]Answer{
		"route":     {Type: TypeChoice, Choice: "a", Confidence: 0.6, Probabilities: map[string]float64{"a": 0.7, "b@high": 0.3}},
		"relevance": {Type: TypeNoul, Noul: 0.8},
		"complexity": {
			Type: TypeScore, Score: 1.4, Confidence: 0.5,
			Probabilities: map[string]float64{"0": 0.1, "1": 0.5, "2": 0.4},
			Legend:        map[string]string{"0": "trivial", "1": "moderate", "2": "hard"},
		},
	}}
	tests := []struct {
		name string
		body string
	}{
		{name: "in request order", body: oaBody(oaRoute, oaRelevance, oaComplexity)},
		{name: "in any order", body: oaBody(oaComplexity, oaRoute, oaRelevance)},
		{name: "untyped answers", body: oaBody(
			strings.Replace(oaRoute, `"type":"choice",`, "", 1),
			strings.Replace(oaRelevance, `"type":"predicate",`, "", 1),
			strings.Replace(oaComplexity, `"type":"score",`, "", 1))},
		{name: "null and empty refusals are none", body: oaBody(
			strings.Replace(oaRoute, `"name":"route",`, `"name":"route","refusal":null,`, 1),
			strings.Replace(oaRelevance, `"name":"relevance",`, `"name":"relevance","refusal":"",`, 1),
			oaComplexity)},
		{name: "unknown fields are ignored", body: strings.Replace(oaBody(oaRoute, oaRelevance, oaComplexity),
			`"answers"`, `"id":"dec_1","model":"m","answers"`, 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := openaiWire{}.decode([]byte(tt.body), oaQuestions())
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

func TestOpenAIDecodeOnlyTheQuestionsSent(t *testing.T) {
	questions := map[string]Question{"relevance": relevanceQuestion()}
	got, err := openaiWire{}.decode([]byte(oaBody(oaRelevance)), questions)
	require.NoError(t, err)
	assert.Equal(t, map[string]Answer{"relevance": {Type: TypeNoul, Noul: 0.8}}, got.Answers)
}

func TestOpenAIDecodeInputTokens(t *testing.T) {
	forty := 42
	tests := []struct {
		name  string
		usage string
		want  *int
	}{
		{name: "reported", usage: `,"usage":{"input_tokens":42,"output_tokens":3}`, want: &forty},
		{name: "no usage", usage: ``},
		{name: "null usage", usage: `,"usage":null`},
		{name: "negative", usage: `,"usage":{"input_tokens":-1}`},
		{name: "fraction", usage: `,"usage":{"input_tokens":4.5}`},
		{name: "string", usage: `,"usage":{"input_tokens":"42"}`},
		{name: "usage not an object", usage: `,"usage":42`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"answers":[` + oaRelevance + `]` + tt.usage + `}`
			got, err := openaiWire{}.decode([]byte(body), map[string]Question{"relevance": relevanceQuestion()})
			require.NoError(t, err, "the count is metadata and never fails a valid answer")
			assert.Equal(t, tt.want, got.InputTokens)
		})
	}
}

func TestOpenAIDecodeEmptyAnswer(t *testing.T) {
	tests := []struct {
		name string
		body string
		id   string
	}{
		{name: "no answers", body: oaBody(), id: "complexity"},
		{name: "null answers", body: `{"answers":null}`, id: "complexity"},
		{name: "no answers key", body: `{}`, id: "complexity"},
		{name: "a question left out", body: oaBody(oaRoute, oaComplexity), id: "relevance"},
		{name: "choice missing", body: oaBody(`{"name":"route","type":"choice"}`, oaRelevance, oaComplexity), id: "route"},
		{name: "choice null", body: oaBody(`{"name":"route","choice":null}`, oaRelevance, oaComplexity), id: "route"},
		{name: "choice empty", body: oaBody(`{"name":"route","choice":""}`, oaRelevance, oaComplexity), id: "route"},
		{name: "predicate missing", body: oaBody(oaRoute, `{"name":"relevance","type":"predicate"}`, oaComplexity), id: "relevance"},
		{name: "predicate without a probability", body: oaBody(oaRoute, `{"name":"relevance","predicate":{}}`, oaComplexity), id: "relevance"},
		{name: "score missing", body: oaBody(oaRoute, oaRelevance, `{"name":"complexity","type":"score"}`), id: "complexity"},
		{name: "score null", body: oaBody(oaRoute, oaRelevance, `{"name":"complexity","score":null}`), id: "complexity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := openaiWire{}.decode([]byte(tt.body), oaQuestions())
			require.ErrorIs(t, err, ErrEmptyAnswer)
			require.ErrorIs(t, err, ErrMalformed)
			require.NotErrorIs(t, err, ErrInvalidAnswer)
			assert.Contains(t, err.Error(), `"`+tt.id+`"`)
		})
	}
}

func TestOpenAIDecodeRefusal(t *testing.T) {
	const text = "I cannot help with the secret-project request"
	tests := []struct {
		name   string
		answer string
	}{
		{name: "refusal text", answer: `{"name":"route","refusal":"` + text + `"}`},
		{name: "refusal object", answer: `{"name":"route","refusal":{"message":"` + text + `"}}`},
		{name: "refusal type", answer: `{"name":"route","type":"refusal","message":"` + text + `"}`},
		{name: "refusal beside an answer", answer: strings.Replace(oaRoute, `"name":"route",`, `"name":"route","refusal":"`+text+`",`, 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := openaiWire{}.decode([]byte(oaBody(tt.answer, oaRelevance, oaComplexity)), oaQuestions())
			require.ErrorIs(t, err, ErrEmptyAnswer)
			assert.Contains(t, err.Error(), "refused")
			assert.Contains(t, err.Error(), `"route"`)
			assert.NotContains(t, err.Error(), "secret-project", "the refusal text may quote the prompt")
		})
	}
}

func TestOpenAIDecodeInvalidAnswer(t *testing.T) {
	probs := func(entries string) string {
		return `{"name":"route","type":"choice","choice":"a","confidence":0.6,"probabilities":` + entries + `}`
	}
	tests := []struct {
		name string
		body string
		want string // part of the message
	}{
		{name: "not JSON", body: `not json`},
		{name: "truncated", body: `{"answers":[{"name":"route"`},
		{name: "a top-level array", body: `[]`},
		{name: "answers not an array", body: `{"answers":{"route":{}}}`},
		{name: "an answer that is a string", body: `{"answers":["route"]}`},
		{name: "a null answer", body: `{"answers":[null]}`, want: "answer 0 is null"},
		{name: "an unknown name", body: oaBody(oaRoute, oaRelevance, oaComplexity, `{"name":"extra","type":"score","score":1}`),
			want: "answer 3 is for a question that was not sent"},
		{name: "a missing name", body: oaBody(`{"type":"choice","choice":"a"}`), want: "answer 0 is for a question that was not sent"},
		{name: "a duplicate name", body: oaBody(oaRoute, oaRelevance, oaComplexity, oaRoute), want: `"route" is answered twice`},
		{name: "a refusal and an answer under one name", body: oaBody(oaRoute, `{"name":"route","refusal":"no"}`, oaRelevance, oaComplexity),
			want: `"route" is answered twice`},
		{name: "a boolean choice", body: oaBody(`{"name":"route","choice":true,"confidence":0.6}`, oaRelevance, oaComplexity),
			want: "choice is not a string"},
		{name: "a numeric choice", body: oaBody(`{"name":"route","choice":1}`, oaRelevance, oaComplexity), want: "choice is not a string"},
		{name: "an object choice", body: oaBody(`{"name":"route","choice":{"value":"a"}}`, oaRelevance, oaComplexity), want: "choice is not a string"},
		{name: "a choice that was not offered", body: oaBody(strings.Replace(oaRoute, `"choice":"a"`, `"choice":"z"`, 1), oaRelevance, oaComplexity),
			want: "is not an option sent"},
		{name: "a choice with a wrong answer type", body: oaBody(strings.Replace(oaRoute, `"type":"choice"`, `"type":"score"`, 1), oaRelevance, oaComplexity),
			want: `answer type "score" for a choice question`},
		{name: "a predicate answered as a choice", body: oaBody(oaRoute, `{"name":"relevance","type":"choice","choice":"true"}`, oaComplexity),
			want: `answer type "choice" for a noul question`},
		{name: "a choice without confidence", body: oaBody(strings.Replace(oaRoute, `,"confidence":0.6`, "", 1), oaRelevance, oaComplexity),
			want: "confidence missing"},
		{name: "confidence over 1", body: oaBody(strings.Replace(oaRoute, `"confidence":0.6`, `"confidence":1.5`, 1), oaRelevance, oaComplexity),
			want: "confidence missing or outside"},
		{name: "no probabilities", body: oaBody(probs(`[]`), oaRelevance, oaComplexity), want: "0 probabilities for 2 options"},
		{name: "probabilities missing", body: oaBody(`{"name":"route","choice":"a","confidence":0.6}`, oaRelevance, oaComplexity),
			want: "0 probabilities for 2 options"},
		{name: "probabilities not summing to 1", body: oaBody(probs(`[{"value":"a","probability":0.5},{"value":"b@high","probability":0.3}]`),
			oaRelevance, oaComplexity), want: "probabilities sum to"},
		{name: "a probability for an unknown value", body: oaBody(probs(`[{"value":"a","probability":0.5},{"value":"z","probability":0.5}]`),
			oaRelevance, oaComplexity), want: "unknown option"},
		{name: "a probability over 1", body: oaBody(probs(`[{"value":"a","probability":1.5},{"value":"b@high","probability":-0.5}]`),
			oaRelevance, oaComplexity), want: "outside [0, 1]"},
		{name: "a repeated value", body: oaBody(probs(`[{"value":"a","probability":0.5},{"value":"a","probability":0.5}]`),
			oaRelevance, oaComplexity), want: "probability 1 repeats the value of an earlier one"},
		{name: "an entry without a value", body: oaBody(probs(`[{"probability":1}]`), oaRelevance, oaComplexity),
			want: "probability 0 needs a value and a probability"},
		{name: "an entry without a probability", body: oaBody(probs(`[{"value":"a"}]`), oaRelevance, oaComplexity),
			want: "probability 0 needs a value and a probability"},
		{name: "a null entry", body: oaBody(probs(`[null]`), oaRelevance, oaComplexity), want: "probability 0 needs"},
		{name: "probabilities as an object", body: oaBody(probs(`{"a":0.7,"b@high":0.3}`), oaRelevance, oaComplexity)},
		{name: "a predicate probability over 1", body: oaBody(oaRoute, `{"name":"relevance","predicate":{"probability":1.5}}`, oaComplexity),
			want: "noul missing or outside [0, 1]"},
		{name: "a predicate probability below 0", body: oaBody(oaRoute, `{"name":"relevance","predicate":{"probability":-0.1}}`, oaComplexity),
			want: "noul missing or outside [0, 1]"},
		{name: "a predicate that is a number", body: oaBody(oaRoute, `{"name":"relevance","predicate":0.8}`, oaComplexity)},
		{name: "a score over the levels", body: oaBody(oaRoute, oaRelevance, strings.Replace(oaComplexity, `"score":1.4`, `"score":3`, 1)),
			want: "outside the level range"},
		{name: "a negative score", body: oaBody(oaRoute, oaRelevance, strings.Replace(oaComplexity, `"score":1.4`, `"score":-1`, 1)),
			want: "outside the level range"},
		{name: "a score level that does not exist", body: oaBody(oaRoute, oaRelevance, strings.Replace(oaComplexity, `"value":"2"`, `"value":"3"`, 1)),
			want: "unknown option"},
		{name: "a score without probabilities", body: oaBody(oaRoute, oaRelevance, `{"name":"complexity","score":1,"confidence":0.5}`),
			want: "0 probabilities for 3 options"},
		{name: "a score with a string value", body: oaBody(oaRoute, oaRelevance, strings.Replace(oaComplexity, `"score":1.4`, `"score":"1.4"`, 1))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := openaiWire{}.decode([]byte(tt.body), oaQuestions())
			require.ErrorIs(t, err, ErrInvalidAnswer)
			require.ErrorIs(t, err, ErrMalformed)
			require.NotErrorIs(t, err, ErrEmptyAnswer)
			assert.Contains(t, err.Error(), tt.want)
			assert.Equal(t, Response{}, resp, "no partial answers beside an error")
		})
	}
}

// TestOpenAIDecodeDeterministic: the questions are checked in id order, so the same bad body always
// names the same one.
func TestOpenAIDecodeDeterministic(t *testing.T) {
	body := oaBody(`{"name":"route","choice":"z","confidence":0.6}`, `{"name":"relevance","predicate":{"probability":3}}`, oaComplexity)
	for range 20 {
		_, err := openaiWire{}.decode([]byte(body), oaQuestions())
		require.ErrorIs(t, err, ErrInvalidAnswer)
		assert.Contains(t, err.Error(), `"relevance"`, "relevance sorts before route")
	}
}

func TestOpenAIErrorType(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "too long is normalized", body: `{"error":{"message":"too long","type":"invalid_request_error","code":"context_length_exceeded"}}`,
			want: errorTypeTooLong},
		{name: "another code as is", body: `{"error":{"code":"invalid_api_key"}}`, want: "invalid_api_key"},
		{name: "no code", body: `{"error":{"message":"x","type":"server_error"}}`},
		{name: "null code", body: `{"error":{"code":null}}`},
		{name: "numeric code", body: `{"error":{"code":429}}`},
		{name: "error is a string", body: `{"error":"context_length_exceeded"}`},
		{name: "not JSON", body: `context_length_exceeded`},
		{name: "empty", body: ``},
		{name: "TypeSafe's shape is not read", body: `{"detail":{"error_type":"max_tokens_exceeded"}}`},
		{name: "a code at the limit", body: `{"error":{"code":"` + strings.Repeat("x", maxErrorType) + `"}}`, want: strings.Repeat("x", maxErrorType)},
		{name: "a code over the limit", body: `{"error":{"code":"` + strings.Repeat("x", maxErrorType+1) + `"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, openaiWire{}.errorType([]byte(tt.body)))
		})
	}
}

func TestOpenAISetHeaders(t *testing.T) {
	for _, longContext := range []bool{false, true} {
		h := http.Header{}
		openaiWire{}.setHeaders(h, longContext)
		assert.Empty(t, h)
	}
}

// TestOpenAIClientSendsNoTypeSafeHeader checks that LongContext on an OpenAI client does not leak
// TypeSafe's long-context header into the request.
func TestOpenAIClientSendsNoTypeSafeHeader(t *testing.T) {
	var got http.Header
	c := openaiClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusBadRequest)
	})
	c.LongContext = true

	_, err := c.Ask(context.Background(), Request{Model: "m", State: "s", Questions: map[string]Question{"q": {Type: TypeChoice, Instructions: "which?", Criteria: Criteria{{Name: "a", Value: "x"}, {Name: "b", Value: "y"}}}}})

	require.Error(t, err)
	assert.Empty(t, got.Get(longContextHeader))
}

// openaiClient is an OpenAI client against a server that answers every request with h.
func openaiClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	c := newTestClient(t, h)
	c.wire = openaiWire{}
	return c
}

func TestAskOpenAI(t *testing.T) {
	var path, auth string
	var sent openaiRequest
	c := openaiClient(t, func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&sent))
		_, _ = io.WriteString(w, oaBody(oaRoute, oaRelevance, oaComplexity))
	})
	resp, err := c.Ask(t.Context(), oaRequest())
	require.NoError(t, err)
	assert.Equal(t, "Bearer "+testKey, auth)
	assert.Equal(t, "/", path)
	assert.Equal(t, "synthetic-openai", sent.Model)
	assert.Len(t, sent.Questions, 3)
	require.Len(t, resp.Answers, 3)
	assert.Equal(t, "a", resp.Answers["route"].Choice)
	assert.InDelta(t, 0.8, resp.Answers["relevance"].Noul, 1e-9)
	assert.InDelta(t, 1.4, resp.Answers["complexity"].Score, 1e-9)
	require.NotNil(t, resp.InputTokens)
	assert.Equal(t, 42, *resp.InputTokens)
}

func TestAskOpenAIMalformed(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		empty bool
	}{
		{name: "a refusal", body: oaBody(`{"name":"route","refusal":"no"}`, oaRelevance, oaComplexity), empty: true},
		{name: "a missing answer", body: oaBody(oaRoute, oaRelevance), empty: true},
		{name: "an extra name", body: oaBody(oaRoute, oaRelevance, oaComplexity, `{"name":"extra"}`)},
		{name: "a duplicate name", body: oaBody(oaRoute, oaRelevance, oaComplexity, oaRelevance)},
		{name: "not JSON", body: `<html>bad gateway</html>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := openaiClient(t, reply(http.StatusOK, tt.body))
			resp, err := c.Ask(t.Context(), oaRequest())
			require.ErrorIs(t, err, ErrMalformed)
			assert.Equal(t, tt.empty, errors.Is(err, ErrEmptyAnswer))
			assert.Equal(t, !tt.empty, errors.Is(err, ErrInvalidAnswer))
			assert.Equal(t, Response{}, resp)
		})
	}
}

func TestAskOpenAIStatusErrors(t *testing.T) {
	const tooLong = `{"error":{"message":"too many tokens","type":"invalid_request_error","param":"input","code":"context_length_exceeded"}}`
	tests := []struct {
		name      string
		status    int
		body      string
		errorType string
		is        error
	}{
		{name: "too long", status: http.StatusBadRequest, body: tooLong, errorType: errorTypeTooLong, is: ErrTooLong},
		{name: "another 400", status: http.StatusBadRequest, body: `{"error":{"code":"invalid_value"}}`, errorType: "invalid_value"},
		{name: "a 400 without a body", status: http.StatusBadRequest},
		{name: "TypeSafe's too-long shape is not OpenAI's", status: http.StatusBadRequest, body: `{"detail":{"error_type":"max_tokens_exceeded"}}`},
		{name: "too long on another status", status: http.StatusUnprocessableEntity, body: tooLong, errorType: errorTypeTooLong},
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"error":{"code":"invalid_api_key"}}`, errorType: "invalid_api_key", is: ErrUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			c := openaiClient(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})
			_, err := c.Ask(t.Context(), oaRequest())
			var serr *StatusError
			require.ErrorAs(t, err, &serr)
			assert.Equal(t, tt.status, serr.Status)
			assert.Equal(t, tt.errorType, serr.ErrorType)
			for _, sentinel := range []error{ErrTooLong, ErrUnauthorized, ErrOverloaded} {
				assert.Equal(t, tt.is != nil && errors.Is(sentinel, tt.is), errors.Is(err, sentinel), "%v", sentinel)
			}
			assert.Equal(t, int32(1), calls.Load(), "not retried")
		})
	}
}

func TestAskOpenAIRetriesThenSucceeds(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, 529} {
		var calls atomic.Int32
		c := openaiClient(t, func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) == 1 {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":{"code":"rate_limit_exceeded"}}`)
				return
			}
			_, _ = io.WriteString(w, oaBody(oaRoute, oaRelevance, oaComplexity))
		})
		resp, err := c.Ask(t.Context(), oaRequest())
		require.NoError(t, err, "status %d", status)
		assert.Len(t, resp.Answers, 3)
		assert.Equal(t, int32(2), calls.Load())
	}
}

func TestAskOpenAIOverloadedExhausted(t *testing.T) {
	c := openaiClient(t, reply(http.StatusTooManyRequests, `{"error":{"code":"rate_limit_exceeded"}}`))
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := c.Ask(ctx, oaRequest())
	require.ErrorIs(t, err, ErrOverloaded)
	var serr *StatusError
	require.ErrorAs(t, err, &serr)
	assert.Equal(t, "rate_limit_exceeded", serr.ErrorType)
}

// TestAskOpenAIRedactsEchoedState: the state travels as JSON text inside the input string, so an error
// body that echoes it has every state string escaped twice. A prompt with quotes, a newline, < and &
// must not reach the error however the server writes that echo.
func TestAskOpenAIRedactsEchoedState(t *testing.T) {
	req := oaRequest()
	req.State = map[string]any{
		"prompt": "fix the \"secret\" <parser> & more\nthen \"hunter2\" stays out",
		"files":  []string{"if a < b && c > d {\n\treturn \"x\"\n}"},
	}
	noHTML := func(v any) string {
		var b strings.Builder
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		require.NoError(t, enc.Encode(v))
		return strings.TrimSuffix(b.String(), "\n")
	}
	asJSON := func(v any) string {
		data, err := json.Marshal(v)
		require.NoError(t, err)
		return string(data)
	}
	tests := []struct {
		name string
		echo func(input string, raw []byte) string
	}{
		{name: "the request body as sent", echo: func(_ string, raw []byte) string {
			return `{"error":{"message":"bad request","body":` + string(raw) + `}}`
		}},
		{name: "the input quoted with HTML escaping", echo: func(input string, _ []byte) string {
			return `{"error":{"message":"invalid input","param":` + asJSON(input) + `}}`
		}},
		{name: "the input quoted without HTML escaping", echo: func(input string, _ []byte) string {
			return `{"error":{"message":"invalid input","param":` + noHTML(input) + `}}`
		}},
		{name: "the input inside a longer message", echo: func(input string, _ []byte) string {
			return `{"error":{"message":` + noHTML("could not parse "+input+" as a decision input") + `}}`
		}},
		{name: "the input as plain text", echo: func(input string, _ []byte) string {
			return "could not parse " + input
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := openaiClient(t, func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				var body openaiRequest
				assert.NoError(t, json.Unmarshal(raw, &body))
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = io.WriteString(w, tt.echo(body.Input, raw))
			})
			_, err := c.Ask(t.Context(), req)
			requireUnprocessable(t, err)
			for _, leaked := range []string{"secret", "parser", "hunter2", "more", "return", "stays out"} {
				assert.NotContains(t, err.Error(), leaked)
			}
			assert.Contains(t, err.Error(), redacted)
		})
	}
}

// TestAskOpenAIRedactsEchoedStateExactly pins what is left of an echo: only the structure around
// the redacted strings.
func TestAskOpenAIRedactsEchoedStateExactly(t *testing.T) {
	req := oaRequest()
	req.State = map[string]any{"prompt": "fix the \"secret\" <parser> & more\nthen stop"}
	c := openaiClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body openaiRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		var b strings.Builder
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		assert.NoError(t, enc.Encode(map[string]string{"message": "invalid input", "input": body.Input}))
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, b.String())
	})
	_, err := c.Ask(t.Context(), req)
	requireUnprocessable(t, err)
	var serr *StatusError
	require.ErrorAs(t, err, &serr)
	assert.JSONEq(t, `{"input":"{\"prompt\":\"<redacted>\"}","message":"invalid input"}`, serr.Body)
}

func TestStateStringsEscapedTwice(t *testing.T) {
	const prompt = "say \"hi\" <b> & go\n"
	once := jsonEscaped(prompt)
	twice := jsonEscaped(once[noHTMLEscape])
	got := stateStrings(map[string]any{"p": prompt, "plain": "plain", "n": 1, "empty": ""})

	for _, form := range append([]string{prompt, "plain"}, append(once, twice...)...) {
		assert.Contains(t, got, form)
	}
	assert.Contains(t, got, `say \\\"hi\\\" <b> & go\\n`, "the text escapes the quotes once, the body again")
	assert.Contains(t, got, `say \\\"hi\\\" \u003cb\u003e \u0026 go\\n`, "and with HTML escaping")
	seen := map[string]bool{}
	for _, form := range got {
		assert.False(t, seen[form], "%q is listed once", form)
		seen[form] = true
	}
	for i := 1; i < len(got); i++ {
		assert.GreaterOrEqual(t, len(got[i-1]), len(got[i]), "longest first")
	}
	assert.Equal(t, 1, strings.Count(strings.Join(got, "\x00"), "plain"), "a string without escapes has one form")
}

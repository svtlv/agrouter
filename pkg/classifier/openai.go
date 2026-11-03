package classifier

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// openaiInputLimit is the most input tokens OpenAI's Decisions API takes in one request: the model's
// context window less the output allowance reserved from it. A request is one question's limit too.
const openaiInputLimit = 922_000

// openaiLimits is the most tokens the Decisions API takes in one question and in one whole request.
// It has no long-context mode, so the setting is ignored.
func openaiLimits(bool) (perQuestion, perRequest int) {
	return openaiInputLimit, openaiInputLimit
}

// OpenAI's question types, and the type of an answer the model refused to give.
const (
	openaiTypeChoice    = "choice"
	openaiTypePredicate = "predicate"
	openaiTypeScore     = "score"
	openaiTypeRefusal   = "refusal"
)

// openaiCodeTooLong is the error code of a 400 for a request over the context length. It is not
// verified against a live call yet: this constant is the only place to fix.
const openaiCodeTooLong = "context_length_exceeded"

// openaiWire is OpenAI's Decisions API: POST /v1/decisions. The state travels as JSON text in the
// input string (input_json is capped at 256 KiB), and the questions as an array named by id.
type openaiWire struct{}

// openaiRequest is the body of a decision request.
type openaiRequest struct {
	Model     string           `json:"model"`
	Input     string           `json:"input"`
	Questions []openaiQuestion `json:"questions"`
}

// openaiQuestion is one question; Choices belong to a choice and Levels to a score.
type openaiQuestion struct {
	Name         string         `json:"name"`
	Type         string         `json:"type"`
	Instructions string         `json:"instructions,omitempty"`
	Choices      []openaiChoice `json:"choices,omitempty"`
	Levels       []openaiLevel  `json:"levels,omitempty"`
}

type openaiChoice struct {
	Value       string `json:"value"`
	Description string `json:"description"`
}

type openaiLevel struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// encode builds the Decisions body for req. Questions are sorted by id, so a request is reproducible;
// choices keep the order of the criteria and levels the order of the scale.
func (openaiWire) encode(req Request) ([]byte, error) {
	input, err := jsonText(req.State)
	if err != nil {
		return nil, fmt.Errorf("state: %w", err)
	}
	body := openaiRequest{
		Model:     req.Model,
		Input:     input,
		Questions: make([]openaiQuestion, 0, len(req.Questions)),
	}
	for _, id := range slices.Sorted(maps.Keys(req.Questions)) {
		q, qErr := openaiQuestionOf(id, req.Questions[id])
		if qErr != nil {
			return nil, fmt.Errorf("question %q: %w", id, qErr)
		}
		body.Questions = append(body.Questions, q)
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	return data, nil
}

// openaiQuestionOf maps one neutral question to OpenAI's: a Choice stays a choice, a Noul becomes a
// predicate, and a Score gets its levels labeled "0" to "n-1".
func openaiQuestionOf(id string, q Question) (openaiQuestion, error) {
	instructions, err := instructionText(q.Instructions)
	if err != nil {
		return openaiQuestion{}, fmt.Errorf("instructions: %w", err)
	}
	out := openaiQuestion{Name: id, Instructions: instructions}
	switch q.Type {
	case TypeChoice:
		out.Type = openaiTypeChoice
		for _, cr := range q.Criteria {
			description, textErr := text(cr.Value)
			if textErr != nil {
				return openaiQuestion{}, fmt.Errorf("criterion %q: %w", cr.Name, textErr)
			}
			out.Choices = append(out.Choices, openaiChoice{Value: cr.Name, Description: description})
		}
	case TypeNoul:
		out.Type = openaiTypePredicate
		if out.Instructions, err = predicateText(instructions, q.Criteria); err != nil {
			return openaiQuestion{}, err
		}
	case TypeScore:
		out.Type = openaiTypeScore
		for i, level := range q.Levels {
			out.Levels = append(out.Levels, openaiLevel{Label: strconv.Itoa(i), Description: level})
		}
	default:
		return openaiQuestion{}, fmt.Errorf("unsupported question type %q", q.Type)
	}
	return out, nil
}

// predicateText is a Noul's instructions with its two criteria folded in, as "true when: ..." and
// "false when: ..." lines under the question: a predicate has no criteria of its own. A criterion
// named anything else would be dropped silently, so it is an error.
func predicateText(instructions string, criteria Criteria) (string, error) {
	var b strings.Builder
	b.WriteString(instructions)
	for _, cr := range criteria {
		if cr.Name != "true" && cr.Name != "false" {
			return "", fmt.Errorf("noul criterion %q is not true or false", cr.Name)
		}
		description, err := text(cr.Value)
		if err != nil {
			return "", fmt.Errorf("criterion %q: %w", cr.Name, err)
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(cr.Name + " when: " + description)
	}
	return b.String(), nil
}

// instructionText is a question's instructions as text: the string itself, or the compact JSON of an
// object. Missing instructions are empty.
func instructionText(instructions any) (string, error) {
	if instructions == nil {
		return "", nil
	}
	return text(instructions)
}

// text is v as the model reads it: a string as is, anything else as compact JSON.
func text(v any) (string, error) {
	if s, ok := v.(string); ok {
		return s, nil
	}
	return jsonText(v)
}

// jsonText is v as compact JSON text. HTML characters stay as they are: the text is read by a model
// and is escaped once more when it is placed in the request body.
func jsonText(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// setHeaders adds nothing: the Decisions API has no provider-specific request headers.
func (openaiWire) setHeaders(http.Header, bool) {}

// errorType is error.code of an error body, with the too-long code normalized to the neutral
// errorTypeTooLong. It is "" when the body is not JSON of that shape or the code is not a short one.
func (openaiWire) errorType(data []byte) string {
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &body) != nil || len(body.Error.Code) > maxErrorType {
		return ""
	}
	if body.Error.Code == openaiCodeTooLong {
		return errorTypeTooLong
	}
	return body.Error.Code
}

// openaiResponse is the wire form of a 200 response: one answer per question, named by its id.
type openaiResponse struct {
	Answers []*openaiAnswer `json:"answers"` // a nil entry is a null answer
	Usage   json.RawMessage `json:"usage"`   // read permissively by inputTokens
}

// openaiAnswer is one answer. Which fields are set depends on its type: a choice has Choice, a
// predicate has Predicate and a score has Score; a choice and a score have Probabilities and
// Confidence. A refusal replaces all of them. Pointers and raw messages tell a missing value from
// zero, and a value of the wrong kind from a missing one.
type openaiAnswer struct {
	Name          string              `json:"name"`
	Type          string              `json:"type"`
	Refusal       json.RawMessage     `json:"refusal"`
	Choice        json.RawMessage     `json:"choice"`
	Predicate     *openaiPredicate    `json:"predicate"`
	Score         *float64            `json:"score"`
	Probabilities []openaiProbability `json:"probabilities"`
	Confidence    *float64            `json:"confidence"`
}

// openaiPredicate is a predicate's answer: the probability that it is true.
type openaiPredicate struct {
	Probability *float64 `json:"probability"`
}

// openaiProbability is the probability of one choice value or score label.
type openaiProbability struct {
	Value       *string  `json:"value"`
	Probability *float64 `json:"probability"`
}

// decode parses a 200 body and validates one answer per question sent: it matches the answers to the
// questions by name, converts each to the neutral form and holds it to the shared validators. A
// question without an answer, or with a refusal, is ErrEmptyAnswer; anything else wrong is
// ErrInvalidAnswer, including an answer for a question that was not sent or a name used twice.
// Questions are checked in id order, so the same body always gives the same error.
func (openaiWire) decode(data []byte, questions map[string]Question) (Response, error) {
	var resp openaiResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return Response{}, fmt.Errorf("classifier: %w: %w", ErrInvalidAnswer, err)
	}
	// a name is never echoed back unless it is one of ours: it comes from the server
	byName := make(map[string]*openaiAnswer, len(resp.Answers))
	for i, a := range resp.Answers {
		if a == nil {
			return Response{}, fmt.Errorf("classifier: %w: answer %d is null", ErrInvalidAnswer, i)
		}
		switch _, known := questions[a.Name]; {
		case !known:
			return Response{}, fmt.Errorf("classifier: %w: answer %d is for a question that was not sent", ErrInvalidAnswer, i)
		case byName[a.Name] != nil:
			return Response{}, fmt.Errorf("classifier: %w: %q is answered twice", ErrInvalidAnswer, a.Name)
		}
		byName[a.Name] = a
	}
	answers := make(map[string]Answer, len(questions))
	for _, id := range slices.Sorted(maps.Keys(questions)) {
		q := questions[id]
		a := byName[id]
		if a == nil {
			return Response{}, fmt.Errorf("classifier: %w: %q", ErrEmptyAnswer, id)
		}
		if a.refused() {
			return Response{}, fmt.Errorf("classifier: %w: %q: refused", ErrEmptyAnswer, id)
		}
		w, err := a.raw(q)
		if err != nil {
			return Response{}, fmt.Errorf("classifier: %w: %q: %w", ErrInvalidAnswer, id, err)
		}
		if empty(q, w) && (w.Type == "" || w.Type == q.Type) {
			return Response{}, fmt.Errorf("classifier: %w: %q", ErrEmptyAnswer, id)
		}
		if answers[id], err = validate(q, w); err != nil {
			return Response{}, fmt.Errorf("classifier: %w: %q: %w", ErrInvalidAnswer, id, err)
		}
	}
	return Response{Answers: answers, InputTokens: inputTokens(resp.Usage)}, nil
}

// refused reports whether the model declined to answer: an answer of the refusal type, or one that
// carries a refusal. A null or empty refusal is none. The refusal's text is never used, since it may
// quote the prompt.
func (a *openaiAnswer) refused() bool {
	r := string(bytes.TrimSpace(a.Refusal))
	return a.Type == openaiTypeRefusal || r != "" && r != "null" && r != `""`
}

// raw is the answer as the shared validators read it, for the question q: the fields q's type asks
// for, with OpenAI's predicate named noul as in the request, and a score's legend rebuilt from q's
// levels, since OpenAI labels them by position and returns no legend.
func (a *openaiAnswer) raw(q Question) (rawAnswer, error) {
	w := rawAnswer{Type: a.Type}
	if a.Type == openaiTypePredicate {
		w.Type = TypeNoul
	}
	var err error
	switch q.Type {
	case TypeChoice:
		w.Confidence = a.Confidence
		if w.Choice, err = choiceValue(a.Choice); err != nil {
			return rawAnswer{}, err
		}
		if w.Probabilities, err = distribution(a.Probabilities); err != nil {
			return rawAnswer{}, err
		}
	case TypeScore:
		w.Score, w.Confidence = a.Score, a.Confidence
		if w.Probabilities, err = distribution(a.Probabilities); err != nil {
			return rawAnswer{}, err
		}
		w.Legend = make(map[string]string, len(q.Levels))
		for i, level := range q.Levels {
			w.Legend[strconv.Itoa(i)] = level
		}
	case TypeNoul:
		if a.Predicate != nil {
			w.Noul = a.Predicate.Probability
		}
	}
	return w, nil
}

// choiceValue is the chosen value: a JSON string, or "" when it is missing or null. Any other kind,
// such as a boolean, is an error.
func choiceValue(raw json.RawMessage) (string, error) {
	if r := bytes.TrimSpace(raw); len(r) == 0 || string(r) == "null" {
		return "", nil
	}
	var choice string
	if err := json.Unmarshal(raw, &choice); err != nil {
		return "", errors.New("choice is not a string")
	}
	return choice, nil
}

// distribution turns the [{value, probability}] list into the neutral map. An entry missing either
// field, or a value listed twice, is an error: the second would silently replace the first.
func distribution(entries []openaiProbability) (map[string]float64, error) {
	out := make(map[string]float64, len(entries))
	for i, e := range entries {
		if e.Value == nil || e.Probability == nil {
			return nil, fmt.Errorf("probability %d needs a value and a probability", i)
		}
		if _, repeated := out[*e.Value]; repeated {
			return nil, fmt.Errorf("probability %d repeats the value of an earlier one", i)
		}
		out[*e.Value] = *e.Probability
	}
	return out, nil
}

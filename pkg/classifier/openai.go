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

// OpenAI's question types.
const (
	openaiTypeChoice    = "choice"
	openaiTypePredicate = "predicate"
	openaiTypeScore     = "score"
)

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

// errorType is not read yet.
func (openaiWire) errorType([]byte) string { return "" }

// decode is not implemented yet.
func (openaiWire) decode([]byte, map[string]Question) (Response, error) {
	return Response{}, fmt.Errorf("classifier: %w: %w", ErrInvalidAnswer, errors.New("openai response decoding is not implemented"))
}

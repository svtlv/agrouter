package classifier

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
)

// rawAnswer is one answer as the validators read it, whichever provider sent it: a codec parses its
// provider's answer into these fields (TypeSafe's wire names are the json tags), and validate holds
// every provider to one definition of a valid answer. Pointers tell a missing number from zero.
type rawAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Score         *float64           `json:"score"`
	Legend        map[string]string  `json:"legend"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    *float64           `json:"confidence"`
	Noul          *float64           `json:"noul"`
}

// inputTokens is usage.input_tokens, or nil when it is missing, null, not a non-negative integer, or
// the usage is not an object: the count is metadata and never fails a valid answer.
func inputTokens(usage json.RawMessage) *int {
	var u struct {
		InputTokens json.RawMessage `json:"input_tokens"`
	}
	if json.Unmarshal(usage, &u) != nil {
		return nil
	}
	n, err := strconv.ParseInt(string(u.InputTokens), 10, 0)
	if err != nil || n < 0 {
		return nil
	}
	tokens := int(n)
	return &tokens
}

// empty reports whether w lacks the value q asks for: the choice, the score or the noul.
func empty(q Question, w rawAnswer) bool {
	switch q.Type {
	case TypeChoice:
		return w.Choice == ""
	case TypeScore:
		return w.Score == nil
	case TypeNoul:
		return w.Noul == nil
	default:
		return false
	}
}

// probabilitySlack is how far the probabilities may sum from 1.
const probabilitySlack = 0.01

// probabilityRoundoff allows float64 error when summing and comparing boundary values.
const probabilityRoundoff = 1e-12

func validate(q Question, w rawAnswer) (Answer, error) {
	if w.Type != "" && w.Type != q.Type {
		return Answer{}, fmt.Errorf("answer type %q for a %s question", w.Type, q.Type)
	}
	switch q.Type {
	case TypeChoice:
		return validateChoice(q.Criteria.Names(), w)
	case TypeScore:
		return validateScore(q.Levels, w)
	case TypeNoul:
		if w.Noul == nil || !unit(*w.Noul) {
			return Answer{}, errors.New("noul missing or outside [0, 1]")
		}
		return Answer{Type: TypeNoul, Noul: *w.Noul}, nil
	default:
		return Answer{}, fmt.Errorf("unsupported question type %q", q.Type)
	}
}

func validateChoice(options []string, w rawAnswer) (Answer, error) {
	if !slices.Contains(options, w.Choice) {
		return Answer{}, fmt.Errorf("choice %q is not an option sent", w.Choice)
	}
	if err := validateDistribution(options, w); err != nil {
		return Answer{}, err
	}
	return Answer{Type: TypeChoice, Choice: w.Choice, Probabilities: w.Probabilities, Confidence: *w.Confidence}, nil
}

// validateDistribution checks the probabilities and confidence shared by Choice and Score.
func validateDistribution(options []string, w rawAnswer) error {
	known := make(map[string]bool, len(options))
	for _, o := range options {
		known[o] = true
	}
	if w.Confidence == nil || !unit(*w.Confidence) {
		return errors.New("confidence missing or outside [0, 1]")
	}
	if len(w.Probabilities) != len(known) {
		return fmt.Errorf("%d probabilities for %d options", len(w.Probabilities), len(known))
	}
	var sum float64
	for o, p := range w.Probabilities {
		if !known[o] {
			return fmt.Errorf("probability for unknown option %q", o)
		}
		if !unit(p) {
			return fmt.Errorf("probability of %q outside [0, 1]", o)
		}
		sum += p
	}
	if math.Abs(sum-1) > probabilitySlack+probabilityRoundoff {
		return fmt.Errorf("probabilities sum to %g", sum)
	}
	return nil
}

// validateScore checks the reported score, ordered level probabilities and legend.
func validateScore(levels []string, w rawAnswer) (Answer, error) {
	if len(levels) < 2 || len(levels) > 10 {
		return Answer{}, errors.New("score needs 2 to 10 levels")
	}
	if w.Score == nil || math.IsNaN(*w.Score) || math.IsInf(*w.Score, 0) ||
		*w.Score < 0 || *w.Score > float64(len(levels)-1) {
		return Answer{}, errors.New("score missing or outside the level range")
	}
	options := make([]string, len(levels))
	for i := range levels {
		options[i] = strconv.Itoa(i)
	}
	if err := validateDistribution(options, w); err != nil {
		return Answer{}, err
	}
	if len(w.Legend) != len(levels) {
		return Answer{}, errors.New("score legend does not match the levels")
	}
	for i, description := range levels {
		if got, ok := w.Legend[options[i]]; !ok || got != description {
			return Answer{}, fmt.Errorf("score legend does not match level %d", i)
		}
	}
	return Answer{Type: TypeScore, Score: *w.Score, Legend: w.Legend,
		Probabilities: w.Probabilities, Confidence: *w.Confidence}, nil
}

// unit reports whether v is finite and in [0, 1].
func unit(v float64) bool {
	return !math.IsNaN(v) && v >= 0 && v <= 1
}

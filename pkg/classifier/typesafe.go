package classifier

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

// TypeSafe's input limits in tokens, per question and per request, and the header that switches
// them to the long-context mode.
const (
	questionLimit     = 32_000
	requestLimit      = 64_000
	longContextLimit  = 262_000
	longContextHeader = "TypeSafe-Danger-Zone-Long-Context"
	// maxErrorType bounds a parsed error type; a longer one is not a code and is left empty.
	maxErrorType = 64
)

// typesafeLimits is the most tokens TypeSafe takes in one question and in one whole request.
func typesafeLimits(longContext bool) (perQuestion, perRequest int) {
	if longContext {
		return longContextLimit, longContextLimit
	}
	return questionLimit, requestLimit
}

// typesafeWire is TypeSafe's "System One" format: POST /v1/systemone with the request as it is
// declared in types.go. Its neutral types marshal to this body, which is also the shape the router
// sizes questions in.
type typesafeWire struct{}

func (typesafeWire) encode(req Request) ([]byte, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	return data, nil
}

func (typesafeWire) setHeaders(h http.Header, longContext bool) {
	if longContext {
		h.Set(longContextHeader, "true")
	}
}

// errorType is detail.error_type of an error body, or "" when the body is not JSON of that shape or
// the type is not a short code. TypeSafe's too-long code is the neutral errorTypeTooLong already.
func (typesafeWire) errorType(data []byte) string {
	var body struct {
		Detail struct {
			ErrorType string `json:"error_type"`
		} `json:"detail"`
	}
	if json.Unmarshal(data, &body) != nil || len(body.Detail.ErrorType) > maxErrorType {
		return ""
	}
	return body.Detail.ErrorType
}

// response is the wire form of a 200 response; pointers tell a missing number from zero.
type response struct {
	Model   string                `json:"model"`
	Answers map[string]*rawAnswer `json:"answers"` // nil: a null answer
	Usage   json.RawMessage       `json:"usage"`   // read permissively by inputTokens
}

// decode parses a 200 body and validates one answer per question sent.
func (typesafeWire) decode(data []byte, questions map[string]Question) (Response, error) {
	var resp response
	if err := json.Unmarshal(data, &resp); err != nil {
		return Response{}, fmt.Errorf("classifier: %w: %w", ErrInvalidAnswer, err)
	}
	answers := make(map[string]Answer, len(questions))
	for id, q := range questions {
		w, ok := resp.Answers[id]
		if !ok || w == nil || empty(q, *w) && (w.Type == "" || w.Type == q.Type) {
			return Response{}, fmt.Errorf("classifier: %w: %q", ErrEmptyAnswer, id)
		}
		a, err := validate(q, *w)
		if err != nil {
			return Response{}, fmt.Errorf("classifier: %w: %q: %w", ErrInvalidAnswer, id, err)
		}
		answers[id] = a
	}
	return Response{Answers: answers, InputTokens: inputTokens(resp.Usage)}, nil
}

// MarshalJSON encodes Score criteria as an array and Choice/Noul criteria as named objects. This is
// TypeSafe's question format; the router sizes questions by it whatever the provider.
func (q Question) MarshalJSON() ([]byte, error) {
	var criteria any = q.Criteria
	if q.Type == TypeScore {
		criteria = q.Levels
	}
	data, err := json.Marshal(struct {
		Type         string `json:"type"`
		Instructions any    `json:"instructions"`
		Criteria     any    `json:"criteria"`
	}{q.Type, q.Instructions, criteria})
	if err != nil {
		return nil, fmt.Errorf("question: %w", err)
	}
	return data, nil
}

// MarshalJSON encodes the criteria as an object with keys in slice order, so requests are
// reproducible and read in catalog order.
func (c Criteria) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, cr := range c {
		if i > 0 {
			buf.WriteByte(',')
		}
		name, err := json.Marshal(cr.Name)
		if err != nil {
			return nil, fmt.Errorf("criterion name: %w", err)
		}
		value, err := json.Marshal(cr.Value)
		if err != nil {
			return nil, fmt.Errorf("criterion %q: %w", cr.Name, err)
		}
		buf.Write(name)
		buf.WriteByte(':')
		buf.Write(value)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

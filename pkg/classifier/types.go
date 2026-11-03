// Package classifier is a client for a routing classifier: one endpoint that answers typed
// questions about a state. Callers build one provider-neutral Request; a wire codec per provider
// (typesafe.go) turns it into that provider's body and the provider's answers into a Response. The
// retry, the deadline and the redaction are shared.
package classifier

// Question types.
const (
	TypeChoice = "choice"
	TypeScore  = "score"
	TypeNoul   = "noul"
)

// Request is a set of typed questions about a state, as the router builds it for any provider. The
// tags and the Question and Criteria encoders are TypeSafe's format (typesafe.go); other codecs
// build their own body from the fields.
type Request struct {
	Model     string              `json:"model"`
	State     any                 `json:"state"`
	Questions map[string]Question `json:"questions"`
}

// Question is one typed question. Choice and Noul use named Criteria; Score uses ordered Levels.
type Question struct {
	Type         string   `json:"type"`
	Instructions any      `json:"instructions"`
	Criteria     Criteria `json:"criteria"`
	Levels       []string `json:"-"`
}

// Criterion is one named criterion; Value is a string, an object or an array.
type Criterion struct {
	Name  string
	Value any
}

// Criteria is an ordered list of named criteria: the order is the catalog's, so requests are
// reproducible.
type Criteria []Criterion

// Names returns the criterion names in order.
func (c Criteria) Names() []string {
	names := make([]string, len(c))
	for i, cr := range c {
		names[i] = cr.Name
	}
	return names
}

// Response is what Ask returns: the validated answers keyed as in the request, and the input tokens
// the provider counted for it; nil when it did not report a valid count.
type Response struct {
	Answers     map[string]Answer
	InputTokens *int
}

// Answer is a validated answer: Choice or Score with probabilities and confidence, or Noul.
type Answer struct {
	Type          string
	Choice        string
	Score         float64
	Legend        map[string]string
	Probabilities map[string]float64
	Confidence    float64
	Noul          float64
}

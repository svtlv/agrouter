package classifier

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
)

// Provider names, as the classifier_provider setting spells them.
const (
	ProviderTypeSafe = "typesafe"
	ProviderOpenAI   = "openai"
)

// ErrUnknownProvider is a provider name that has no wire codec.
var ErrUnknownProvider = errors.New("unknown classifier provider")

// wire is one provider's format on the wire. The client owns everything shared: the HTTP call, the
// retry, the deadline and the redaction. A codec only turns the neutral Request into the provider's
// body and the provider's answers back into a Response.
type wire interface {
	// encode is the request body for req.
	encode(req Request) ([]byte, error)
	// decode parses a 200 body and validates one answer per question sent, keyed as in questions.
	decode(data []byte, questions map[string]Question) (Response, error)
	// errorType is the provider's code for an error body, normalized to the neutral values the
	// sentinels match (errorTypeTooLong), or "" when the body has none.
	errorType(data []byte) string
	// setHeaders adds the provider's own request headers; longContext is the client's setting.
	setHeaders(h http.Header, longContext bool)
}

// providerSpec is what the package knows about one provider: its codec and its capabilities.
type providerSpec struct {
	wire wire
	// longContext is whether the provider has a long-context mode that Client.LongContext turns on.
	longContext bool
	// limits is the most tokens the provider takes in one question and in one whole request.
	limits func(longContext bool) (perQuestion, perRequest int)
}

// providers is the one table of supported providers: Providers, SupportsLongContext, Limits and New
// all read it, so a provider is supported exactly when it has a codec here.
var providers = map[string]providerSpec{
	ProviderTypeSafe: {wire: typesafeWire{}, longContext: true, limits: typesafeLimits},
}

// Providers returns the supported provider names, sorted.
func Providers() []string {
	return slices.Sorted(maps.Keys(providers))
}

// SupportsLongContext reports whether the provider has a long-context mode. It is false for a
// provider the package does not know.
func SupportsLongContext(provider string) bool {
	return providers[provider].longContext
}

// Limits returns the most tokens the provider takes in one question and in one whole request.
// longContext selects the provider's long-context mode where it has one (SupportsLongContext), which
// Client.LongContext turns on in the request; it is ignored otherwise. The router checks the
// configured request size against these limits. An unknown provider has no limits: both are zero.
func Limits(provider string, longContext bool) (perQuestion, perRequest int) {
	spec, ok := providers[provider]
	if !ok {
		return 0, 0
	}
	return spec.limits(longContext)
}

// codecFor is the codec and capabilities of provider, or ErrUnknownProvider naming the valid ones.
func codecFor(provider string) (providerSpec, error) {
	spec, ok := providers[provider]
	if !ok {
		return providerSpec{}, fmt.Errorf("classifier: %w %q (use %s)", ErrUnknownProvider, provider,
			strings.Join(Providers(), " or "))
	}
	return spec, nil
}

package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/ferro-labs/ai-gateway/internal/apierror"
)

// HeaderRoutingMetadata is the one request header conditional routing may
// read (`key: metadata`). Its value is a small JSON object of scalar values.
// It is the whole of the header surface exposed to predicates: routing never
// reads an arbitrary header, so a caller cannot steer a rule with one the
// operator did not choose to expose.
const HeaderRoutingMetadata = "X-Gateway-Metadata"

const (
	maxRoutingMetadataBytes = 4096
	maxRoutingMetadataKeys  = 32
)

// routingMetadata parses HeaderRoutingMetadata into the map the conditional
// strategy reads. Absent means nil; anything present must be a JSON object
// of at most maxRoutingMetadataKeys string, number or boolean values within
// maxRoutingMetadataBytes — a bound on what a caller can make routing parse.
func routingMetadata(h http.Header) (map[string]string, error) {
	raw := h.Get(HeaderRoutingMetadata)
	if raw == "" {
		return nil, nil
	}
	if len(raw) > maxRoutingMetadataBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", HeaderRoutingMetadata, maxRoutingMetadataBytes)
	}
	var object map[string]any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber() // a large integer must compare as written, not rounded through float64
	if err := dec.Decode(&object); err != nil || object == nil {
		return nil, fmt.Errorf("%s is not a JSON object", HeaderRoutingMetadata)
	}
	if len(object) > maxRoutingMetadataKeys {
		return nil, fmt.Errorf("%s has more than %d entries", HeaderRoutingMetadata, maxRoutingMetadataKeys)
	}
	out := make(map[string]string, len(object))
	for key, value := range object {
		switch v := value.(type) {
		case string:
			out[key] = v
		case json.Number:
			out[key] = v.String()
		case bool:
			out[key] = strconv.FormatBool(v)
		default:
			return nil, fmt.Errorf("%s entry %q must be a string, number or boolean", HeaderRoutingMetadata, key)
		}
	}
	return out, nil
}

// writeRoutingMetadataError reports a malformed metadata header as the
// caller's 400.
func writeRoutingMetadataError(w http.ResponseWriter, err error) {
	apierror.WriteOpenAI(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_request")
}

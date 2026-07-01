package core

import (
	"errors"
	"fmt"
)

// errEmptyEmbeddingInput is returned when an embeddings "input" array is empty.
var errEmptyEmbeddingInput = errors.New("embed: Input must not be an empty array")

// CoerceEmbeddingInput flattens an OpenAI embeddings "input" value into a plain
// []string for providers whose native API accepts only a list of texts. A bare
// string becomes a one-element slice; a []string is returned as-is; a []any is
// coerced element-by-element. An empty array, a nil input, or a non-string
// element is rejected.
func CoerceEmbeddingInput(input any) ([]string, error) {
	switch v := input.(type) {
	case string:
		return []string{v}, nil
	case []string:
		if len(v) == 0 {
			return nil, errEmptyEmbeddingInput
		}
		return v, nil
	case []any:
		if len(v) == 0 {
			return nil, errEmptyEmbeddingInput
		}
		texts := make([]string, 0, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("embed: Input[%d] is %T, want string", i, item)
			}
			texts = append(texts, s)
		}
		return texts, nil
	case nil:
		return nil, fmt.Errorf("embed: Input must not be nil")
	default:
		return nil, fmt.Errorf("embed: unsupported Input type %T; want string or []string", input)
	}
}

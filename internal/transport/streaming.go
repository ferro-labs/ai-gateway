package transport

// IsStreamingRequest returns true if the request body contains
// "stream":true in any whitespace variation.
// Zero allocations — does not parse JSON, uses byte scanning only.
func IsStreamingRequest(body []byte) bool {
	// scan for "stream" then look for true after the colon
	for i := 0; i < len(body)-10; i++ {
		if body[i] != 's' {
			continue
		}
		if i+6 > len(body) || string(body[i:i+6]) != "stream" {
			continue
		}
		// found "stream" — scan forward for colon then true/false
		for j := i + 6; j < len(body) && j < i+30; j++ {
			switch body[j] {
			case ' ', '\t', '\n', '\r', '"', ':':
				continue
			case 't':
				return j+4 <= len(body) && string(body[j:j+4]) == "true"
			case 'f':
				return false
			}
		}
	}
	return false
}

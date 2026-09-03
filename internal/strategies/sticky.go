package strategies

import (
	"hash/fnv"
	"math/rand"
	"strconv"
	"time"
)

// StickyOnUser is the one key sticky hashing supports: the request's `user`
// field, which is what provider prompt caches and a multi-turn A/B session are
// scoped by.
const StickyOnUser = "user"

// Sticky pins a request to the same target for the same key, so a conversation
// keeps its prompt cache and an A/B session keeps its variant. It is a
// stateless hash — the key (and, with a TTL, the current TTL window) decides
// the draw — so it needs no shared state and holds across gateway replicas
// with the same config. A request with no key draws at random as before.
// The hash maps into the weight-proportional draw, so changing weights or the
// target set re-maps a share of keys.
type Sticky struct {
	// On names the request field hashed. Only StickyOnUser is supported.
	On string
	// TTL, when positive, rotates assignments: the key is hashed together
	// with the TTL window it falls in, so a pin lasts at most one window.
	TTL time.Duration
	// now is the clock the window is read from; tests replace it.
	now func() time.Time
}

// enabled reports whether s pins anything at all.
func (s Sticky) enabled() bool { return s.On != "" }

// unit returns the draw for key in [0, 1): a hash when sticky applies to this
// request, a random number otherwise.
func (s Sticky) unit(key string) float64 {
	if !s.enabled() || key == "" {
		return rand.Float64() //nolint:gosec // G404: load-balancing draw, not security-sensitive
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	if s.TTL > 0 {
		now := s.now
		if now == nil {
			now = time.Now
		}
		window := now().UnixNano() / int64(s.TTL)
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(strconv.FormatInt(window, 10)))
	}
	// FNV-1a leaves the high bits nearly untouched by a short suffix — two
	// TTL windows hashed almost the same unit — so the sum is run through a
	// 64-bit finaliser (MurmurHash3's fmix64) before the top bits are taken.
	x := h.Sum64()
	x ^= x >> 33
	x *= 0xff51afd7ed558ccd
	x ^= x >> 33
	x *= 0xc4ceb9fe1a85ec53
	x ^= x >> 33
	const mantissaBits = 53
	return float64(x>>(64-mantissaBits)) / float64(uint64(1)<<mantissaBits)
}

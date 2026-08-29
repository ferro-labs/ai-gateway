package httpserver

import "testing"

type nilCloser struct{ closed *bool }

func (c *nilCloser) Close() error {
	*c.closed = true // dereferences the receiver, like Gateway.Close does
	return nil
}

// A concrete pointer that was never assigned still satisfies the Close
// assertion once boxed into Value; calling it dereferences nil. Callers build
// these lists from struct fields that are nil on every early-exit path, so the
// skip has to live here, where every close routes through.
func TestCloseResourcesSkipsTypedNilPointer(t *testing.T) {
	closed := false
	var never *nilCloser

	err := CloseResources(
		NamedResource{Name: "never built", Value: never},
		NamedResource{Name: "built", Value: &nilCloser{closed: &closed}},
	)

	if err != nil {
		t.Fatalf("CloseResources returned an error: %v", err)
	}
	if !closed {
		t.Fatal("the non-nil resource was not closed")
	}
}

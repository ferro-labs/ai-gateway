package observability

import (
	"context"
	"testing"
)

func TestRequestIdentity_ContextRoundTrip(t *testing.T) {
	want := RequestIdentity{
		User:      "user-42",
		SessionID: "sess-7",
		Metadata:  map[string]string{"team": "search"},
	}
	ctx := ContextWithRequestIdentity(context.Background(), want)

	got := RequestIdentityFromContext(ctx)
	if got.User != want.User || got.SessionID != want.SessionID {
		t.Fatalf("identity = %+v, want %+v", got, want)
	}
	if got.Metadata["team"] != "search" || len(got.Metadata) != 1 {
		t.Fatalf("metadata = %v, want %v", got.Metadata, want.Metadata)
	}
}

func TestRequestIdentity_AbsentIsZero(t *testing.T) {
	got := RequestIdentityFromContext(context.Background())
	if !got.IsZero() {
		t.Fatalf("identity on a bare context = %+v, want zero", got)
	}
	if (RequestIdentity{User: "u"}).IsZero() || (RequestIdentity{SessionID: "s"}).IsZero() ||
		(RequestIdentity{Metadata: map[string]string{"k": "v"}}).IsZero() {
		t.Fatal("IsZero reported true for a populated identity")
	}
}

func TestRequestIdentity_AttributeNames(t *testing.T) {
	if AttrEndUserID != "enduser.id" || AttrSessionID != "session.id" || AttrFerroRequestMetadataPrefix != "ferro.request.metadata." {
		t.Fatalf("attribute names drifted: %q %q %q", AttrEndUserID, AttrSessionID, AttrFerroRequestMetadataPrefix)
	}
}

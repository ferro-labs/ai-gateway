package observability

import (
	"context"
	"fmt"
	"strings"
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

func TestRequestIdentity_MetadataEntryCountIsCapped(t *testing.T) {
	metadata := make(map[string]string, maxMetadataEntries+10)
	for i := 0; i < maxMetadataEntries+10; i++ {
		metadata[fmt.Sprintf("key-%03d", i)] = "v"
	}
	ctx := ContextWithRequestIdentity(context.Background(), RequestIdentity{Metadata: metadata})
	got := RequestIdentityFromContext(ctx).Metadata
	if len(got) != maxMetadataEntries {
		t.Fatalf("stored metadata entries = %d, want %d", len(got), maxMetadataEntries)
	}
}

func TestRequestIdentity_OversizedMetadataEntryIsDropped(t *testing.T) {
	for _, tc := range []struct {
		name    string
		key     string
		value   string
		dropped string
	}{
		{
			name:    "key over the length limit",
			key:     strings.Repeat("k", maxMetadataKeyLen+1),
			value:   "v",
			dropped: strings.Repeat("k", maxMetadataKeyLen+1),
		},
		{
			name:    "value over the length limit",
			key:     "bad",
			value:   strings.Repeat("v", maxMetadataValueLen+1),
			dropped: "bad",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := ContextWithRequestIdentity(context.Background(), RequestIdentity{
				Metadata: map[string]string{tc.key: tc.value, "ok": "v"},
			})
			got := RequestIdentityFromContext(ctx).Metadata
			if _, present := got[tc.dropped]; present {
				t.Error("an oversized metadata entry was stored, want dropped")
			}
			if got["ok"] != "v" {
				t.Error("a valid sibling entry was dropped alongside the oversized one")
			}
		})
	}
}

func TestRequestIdentity_MetadataCapIsDeterministic(t *testing.T) {
	metadata := make(map[string]string, maxMetadataEntries+10)
	for i := 0; i < maxMetadataEntries+10; i++ {
		metadata[fmt.Sprintf("key-%03d", i)] = fmt.Sprintf("v-%03d", i)
	}
	var first map[string]string
	for i := 0; i < 20; i++ {
		ctx := ContextWithRequestIdentity(context.Background(), RequestIdentity{Metadata: metadata})
		got := RequestIdentityFromContext(ctx).Metadata
		if first == nil {
			first = got
			continue
		}
		if len(got) != len(first) {
			t.Fatalf("run %d: metadata size = %d, want %d", i, len(got), len(first))
		}
		for k, v := range first {
			if got[k] != v {
				t.Fatalf("run %d: stored metadata differs from the first run — capping is not deterministic", i)
			}
		}
	}
}

func TestRequestIdentity_AttributeNames(t *testing.T) {
	if AttrEndUserID != "enduser.id" || AttrSessionID != "session.id" || AttrFerroRequestMetadataPrefix != "ferro.request.metadata." {
		t.Fatalf("attribute names drifted: %q %q %q", AttrEndUserID, AttrSessionID, AttrFerroRequestMetadataPrefix)
	}
}

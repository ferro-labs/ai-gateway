package anthropicwire

import "testing"

func TestParseDataURI(t *testing.T) {
	cases := []struct {
		name      string
		uri       string
		wantOK    bool
		wantMedia string
		wantData  string
	}{
		{"base64", "data:image/png;base64,QUJD", true, "image/png", "QUJD"},
		{"base64 after charset param", "data:image/png;charset=utf-8;base64,QUJD", true, "image/png", "QUJD"},
		{"non-base64 data URI", "data:image/png,rawtext", false, "", ""},
		{"remote url", "https://example.com/x.png", false, "", ""},
		{"no comma", "data:image/png;base64", false, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mt, data, ok := ParseDataURI(tc.uri)
			if ok != tc.wantOK || mt != tc.wantMedia || data != tc.wantData {
				t.Fatalf("ParseDataURI(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.uri, mt, data, ok, tc.wantMedia, tc.wantData, tc.wantOK)
			}
		})
	}
}

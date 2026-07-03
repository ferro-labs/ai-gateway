package core

import "testing"

func TestValidateBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		{"https", "https://api.example.com", false},
		{"https with path", "https://api.example.com/v1", false},
		{"http", "http://localhost:8080", false},
		{"empty", "", true},
		{"no scheme", "api.example.com", true},
		{"unsupported scheme", "ftp://api.example.com", true},
		{"scheme without host", "https://", true},
		{"unparseable", "http://[::1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBaseURL("prov", tc.rawURL)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateBaseURL(%q) error = %v, wantErr = %v", tc.rawURL, err, tc.wantErr)
			}
		})
	}
}

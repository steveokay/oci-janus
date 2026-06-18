package bearer

import "testing"

func TestExtract(t *testing.T) {
	cases := []struct {
		name      string
		header    string
		wantToken string
		wantOK    bool
	}{
		{"canonical Bearer", "Bearer abc.def.ghi", "abc.def.ghi", true},
		{"lowercase bearer", "bearer abc.def.ghi", "abc.def.ghi", true},
		{"uppercase BEARER", "BEARER abc.def.ghi", "abc.def.ghi", true},
		{"mixed-case BeArEr", "BeArEr abc.def.ghi", "abc.def.ghi", true},
		{"tab separator", "Bearer\tabc.def.ghi", "abc.def.ghi", true},
		{"multiple spaces after scheme", "Bearer   abc", "abc", true},
		{"empty header", "", "", false},
		{"scheme only", "Bearer", "", false},
		{"scheme with trailing space, no token", "Bearer ", "", false},
		{"Basic scheme rejected", "Basic dXNlcjpwYXNz", "", false},
		{"Digest scheme rejected", "Digest realm=x", "", false},
		{"BearerExt confusable rejected", "BearerExt abc", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Extract(c.header)
			if got != c.wantToken || ok != c.wantOK {
				t.Errorf("Extract(%q) = (%q, %v), want (%q, %v)", c.header, got, ok, c.wantToken, c.wantOK)
			}
		})
	}
}

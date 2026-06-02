package companion

import "testing"

func TestParseLocation(t *testing.T) {
	ok := []struct {
		in   string
		want Location
	}{
		{"7/0/2", Location{7, 0, 2}},
		{"1/3/2", Location{1, 3, 2}},
		{" 7 / 0 / 2 ", Location{7, 0, 2}},
		{"12/10/9", Location{12, 10, 9}},
	}
	for _, tc := range ok {
		got, err := ParseLocation(tc.in)
		if err != nil {
			t.Errorf("ParseLocation(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseLocation(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
		if got.String() != tc.want.String() {
			t.Errorf("String() = %q, want %q", got.String(), tc.want.String())
		}
	}

	bad := []string{"", "7/0", "7/0/2/1", "a/0/2", "7//2", "-1/0/2", "0/0/0", "7/0/2.5"}
	for _, in := range bad {
		if _, err := ParseLocation(in); err == nil {
			t.Errorf("ParseLocation(%q) = nil error, want error", in)
		}
	}
}

func TestLocationString(t *testing.T) {
	if got := (Location{7, 0, 2}).String(); got != "7/0/2" {
		t.Errorf("String() = %q, want 7/0/2", got)
	}
}

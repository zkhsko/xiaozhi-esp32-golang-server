package ws

import (
	"errors"
	"testing"
)

func TestParseVersion(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  Version
	}{{"1", Version1}, {" 1\t", Version1}, {"2", Version2}, {"3", Version3}} {
		version, err := ParseVersion(tc.value)
		if err != nil || version != tc.want {
			t.Fatalf("ParseVersion(%q) = %v, %v", tc.value, version, err)
		}
	}
	for _, value := range []string{"", "0", "-1", "01", "+1", "4", "1,1", "65537", "99999999999999999999"} {
		if _, err := ParseVersion(value); !errors.Is(err, ErrUnsupportedVersion) {
			t.Errorf("ParseVersion(%q) = %v", value, err)
		}
	}
}

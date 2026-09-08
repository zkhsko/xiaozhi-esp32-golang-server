package ws

import (
	"errors"
	"testing"
)

func TestParseVersion(t *testing.T) {
	for _, value := range []string{"1", " 1\t"} {
		version, err := ParseVersion(value)
		if err != nil || version != Version1 {
			t.Fatalf("ParseVersion(%q) = %v, %v", value, version, err)
		}
	}
	for _, value := range []string{"", "0", "-1", "01", "+1", "2", "3", "4", "1,1", "65537", "99999999999999999999"} {
		if _, err := ParseVersion(value); !errors.Is(err, ErrUnsupportedVersion) {
			t.Errorf("ParseVersion(%q) = %v", value, err)
		}
	}
}

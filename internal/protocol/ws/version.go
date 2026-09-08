package ws

import (
	"errors"
	"strconv"
	"strings"
)

type Version int

const Version1 Version = 1

var ErrUnsupportedVersion = errors.New("unsupported websocket protocol version")

func ParseVersion(value string) (Version, error) {
	value = strings.TrimSpace(value)
	n, err := strconv.Atoi(value)
	if err != nil || strconv.Itoa(n) != value {
		return 0, ErrUnsupportedVersion
	}
	v := Version(n)
	if err := v.Validate(); err != nil {
		return 0, err
	}
	return v, nil
}

func (v Version) Validate() error {
	if v != Version1 {
		return ErrUnsupportedVersion
	}
	return nil
}

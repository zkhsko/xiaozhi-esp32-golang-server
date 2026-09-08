package ws

import (
	"errors"
	"strconv"
	"strings"
)

type Version int

const (
	Version1 Version = 1
	Version2 Version = 2
)

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
	if v != Version1 && v != Version2 {
		return ErrUnsupportedVersion
	}
	return nil
}

func (v Version) headerSize() int {
	if v == Version2 {
		return 16
	}
	return 0
}

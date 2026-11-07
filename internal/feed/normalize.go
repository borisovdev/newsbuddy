// Package feed provides pure helpers for feed URL normalization and item
// key derivation, used to detect duplicate sources and duplicate items.
package feed

import (
	"errors"
	"net/url"
	"strings"
)

// NormalizeURL canonicalizes a feed URL so that equivalent URLs compare
// equal.5 for the exact steps and non-goals.
func NormalizeURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)

	u, err := url.Parse(trimmed)
	if err != nil {
		return "", err
	}
	if u.Host == "" || u.Opaque != "" {
		return "", errors.New("feed URL must have a host")
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("only http and https feed URLs are supported")
	}
	u.Scheme = scheme
	u.Host = strings.ToLower(u.Host)

	u.Fragment = ""
	u.RawFragment = ""

	u.Path = strings.TrimRight(u.Path, "/")

	// RawQuery is left untouched: preserved byte-for-byte, unsorted.

	u.User = nil

	return u.String(), nil
}

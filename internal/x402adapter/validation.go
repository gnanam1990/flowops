package x402adapter

import "regexp"

func mustCompileAddressPattern() *regexp.Regexp {
	return regexp.MustCompile(`^0x[0-9a-f]{40}$`)
}

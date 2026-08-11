package main

import "testing"

func TestValidateLoopbackListener(t *testing.T) {
	t.Parallel()
	for _, address := range []string{"127.0.0.1:8080", "[::1]:8080", "localhost:8080"} {
		if err := validateLoopbackListener(address); err != nil {
			t.Errorf("validateLoopbackListener(%q) error = %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8080", "8.8.8.8:8080", ":8080", "127.0.0.1"} {
		if err := validateLoopbackListener(address); err == nil {
			t.Errorf("validateLoopbackListener(%q) unexpectedly succeeded", address)
		}
	}
}

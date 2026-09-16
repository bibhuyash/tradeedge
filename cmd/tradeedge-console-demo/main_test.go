package main

import "testing"

func TestOnlyLoopbackAllowed(t *testing.T) {
	for _, address := range []string{"0.0.0.0:8090", ":8090", "192.168.1.2:8090", "localhost:8090", "[::]:8090", "invalid"} {
		if loopbackAddress(address) {
			t.Fatalf("allowed %s", address)
		}
	}
	for _, address := range []string{"127.0.0.1:8090", "[::1]:8090"} {
		if !loopbackAddress(address) {
			t.Fatalf("rejected %s", address)
		}
	}
}

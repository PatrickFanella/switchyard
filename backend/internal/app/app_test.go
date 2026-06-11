package app

import "testing"

func TestGreeting(t *testing.T) {
	if got := Greeting("SUBCULT"); got != "Hello, SUBCULT!" {
		t.Fatalf("unexpected greeting: %s", got)
	}
}

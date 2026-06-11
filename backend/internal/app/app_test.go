package app

import "testing"

func TestGreeting(t *testing.T) {
	if got := Greeting("Switchyard"); got != "Hello, Switchyard!" {
		t.Fatalf("unexpected greeting: %s", got)
	}
}

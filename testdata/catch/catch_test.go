package catch

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(2, 3); got != 5 {
		t.Errorf("Add(2, 3) = %d", got)
	}
}

func TestNotTrue(t *testing.T) {
	if Not(true) {
		t.Error("Not(true) is true")
	}
}

func TestNotFalse(t *testing.T) {
	if !Not(false) {
		t.Error("Not(false) is false")
	}
}

func TestJoin(t *testing.T) {
	if got := Join("a", "b"); got != "ab" {
		t.Errorf("Join = %q", got)
	}
}

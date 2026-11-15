package cases

import "testing"

func TestSize(t *testing.T) {
	if got := Size(9); got != "small" {
		t.Errorf("Size(9) = %q", got)
	}
	if got := Size(10); got != "big" {
		t.Errorf("Size(10) = %q", got)
	}
}

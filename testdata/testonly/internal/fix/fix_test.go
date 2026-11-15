package fix

import "testing"

func TestPositive(t *testing.T) {
	if !Positive(fixture(0)) || Positive(fixture(-1)) {
		t.Error("wrong")
	}
}

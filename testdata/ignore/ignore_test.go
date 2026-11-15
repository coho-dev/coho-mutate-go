package ignore

import "testing"

func TestCount(t *testing.T) {
	if got := Count(3); got != 3 {
		t.Errorf("Count(3) = %d", got)
	}
}

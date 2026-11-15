package parallel

import (
	"testing"
	"time"
)

// TestP1/one panics on Get's bugs after TestP2/a has started, so the last
// test to start is not the one that panicked.
func TestP1(t *testing.T) {
	t.Parallel()
	t.Run("one", func(t *testing.T) {
		time.Sleep(150 * time.Millisecond)
		if got := Get([]int{1}, 1); got != -1 {
			t.Errorf("Get past the end = %d", got)
		}
	})
	t.Run("two", func(t *testing.T) {
		if got := Get(nil, 0); got != -1 {
			t.Errorf("Get on nothing = %d", got)
		}
	})
}

func TestP2(t *testing.T) {
	t.Parallel()
	time.Sleep(50 * time.Millisecond)
	t.Run("a", func(t *testing.T) {
		time.Sleep(300 * time.Millisecond)
		if got := Get([]int{1, 2}, 0); got != 1 {
			t.Errorf("Get(0) = %d", got)
		}
	})
}

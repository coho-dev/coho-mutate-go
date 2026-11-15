package crash

import "testing"

func TestAtEnd(t *testing.T) {
	if got := At([]int{1}, 1); got != -1 {
		t.Errorf("At past the end = %d", got)
	}
}

func TestAtLater(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if got := At(nil, 0); got != -1 {
			t.Errorf("At on nothing = %d", got)
		}
	})
}

func TestAtTable(t *testing.T) {
	for _, c := range []struct {
		name string
		xs   []int
		i    int
	}{{"one", []int{1}, 1}, {"two", []int{1, 2}, 2}} {
		t.Run(c.name, func(t *testing.T) {
			if got := At(c.xs, c.i); got != -1 {
				t.Errorf("At past the end = %d", got)
			}
		})
	}
}

func TestAtGroup(t *testing.T) {
	t.Run("group", func(t *testing.T) {
		t.Run("first", func(t *testing.T) {
			if got := At([]int{1}, 1); got != -1 {
				t.Errorf("At past the end = %d", got)
			}
		})
		t.Run("second", func(t *testing.T) {
			if got := At(nil, 0); got != -1 {
				t.Errorf("At on nothing = %d", got)
			}
		})
	})
}

func TestMax(t *testing.T) {
	if got := Max(1, 2); got != 2 {
		t.Errorf("Max(1, 2) = %d", got)
	}
}

func TestCount(t *testing.T) {
	if got := Count(3); got != 3 {
		t.Errorf("Count(3) = %d", got)
	}
}

func TestCountAgain(t *testing.T) {
	if got := Count(5); got != 5 {
		t.Errorf("Count(5) = %d", got)
	}
}

func TestCountThird(t *testing.T) {
	if got := Count(2); got != 2 {
		t.Errorf("Count(2) = %d", got)
	}
}

func TestCountFourth(t *testing.T) {
	if got := Count(1); got != 1 {
		t.Errorf("Count(1) = %d", got)
	}
}

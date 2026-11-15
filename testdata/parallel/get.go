package parallel

// Get is xs[i], or -1 past the end. Both its bugs panic in TestP1/one and
// TestP1/two. Its bug ">=" also fails TestP2/a, which is still running when
// TestP1/one panics.
func Get(xs []int, i int) int {
	if i < len(xs) {
		return xs[i]
	}
	return -1
}

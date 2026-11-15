// Command positive is the one caller of fix.Positive outside a test.
package main

import (
	"fmt"

	"example.com/testonly/internal/fix"
)

func main() { fmt.Println(fix.Positive(1)) }

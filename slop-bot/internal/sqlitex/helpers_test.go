package sqlitex_test

import (
	"fmt"

	"github.com/uptrace/bun"
)

type bunTx = bun.Tx

func nameFor(i, n int) string { return fmt.Sprintf("w%d-%d", i, n) }

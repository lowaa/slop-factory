package rpcdriver

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readAll reads everything through readLine, with a reader buffer smaller
// than the lines so the ErrBufferFull path is taken too.
func readAll(t *testing.T, in string, max int) (got []string, tooLong int) {
	t.Helper()
	r := bufio.NewReaderSize(strings.NewReader(in), 16)
	for {
		line, err := readLine(r, max)
		if errors.Is(err, errLineTooLong) {
			tooLong++
			continue
		}
		if len(line) > 0 {
			got = append(got, string(line))
		}
		if err != nil {
			require.ErrorIs(t, err, io.EOF)
			return got, tooLong
		}
	}
}

func TestReadLineDropsAnOversizedLineAndCarriesOn(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 100)
	got, tooLong := readAll(t, "a\n"+long+"\n"+strings.Repeat("y", 40)+"\nlast", 40)
	assert.Equal(t, []string{"a", strings.Repeat("y", 40), "last"}, got, "a line of exactly max fits; the one after the long line still arrives")
	assert.Equal(t, 1, tooLong)
}

func TestReadLineSplitsOnLFOnly(t *testing.T) {
	t.Parallel()
	got, _ := readAll(t, "{\"t\":\"a b\"}\n{}\n", 1<<10)
	assert.Equal(t, []string{"{\"t\":\"a b\"}", "{}"}, got)
}

func TestReadLineAnOversizedLastLine(t *testing.T) {
	t.Parallel()
	got, tooLong := readAll(t, "a\n"+strings.Repeat("z", 100), 40)
	assert.Equal(t, []string{"a"}, got)
	assert.Equal(t, 1, tooLong)
}

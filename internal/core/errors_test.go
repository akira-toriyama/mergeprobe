package core

import (
	"errors"
	"fmt"
	"testing"
)

func TestExitCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil is ok", nil, 0},
		{"not-found maps to 1", NotFoundf("x", "missing"), 1},
		{"validation maps to 2", Validationf("x", "bad arg"), 2},
		{"internal maps to 3", Internalf("x", "boom"), 3},
		{"wrapped typed error still resolves", fmt.Errorf("ctx: %w", Validationf("x", "bad")), 2},
		{"unclassified error is internal, never usage", errors.New("plain"), 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ExitCode(c.err); got != c.want {
				t.Errorf("ExitCode(%v) = %d, want %d", c.err, got, c.want)
			}
		})
	}
}

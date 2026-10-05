package caldav

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsNotFoundError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{ErrNotFound, true},
		{fmt.Errorf("%w: failed to delete event: %w", ErrConnectionFailed, errors.New("404 Not Found")), true},
		{fmt.Errorf("%w: failed to delete event: %w", ErrConnectionFailed, errors.New("500 Internal Server Error")), false},
		{errors.New("410 Gone"), false},
	}
	for _, tc := range cases {
		if got := IsNotFoundError(tc.err); got != tc.want {
			t.Errorf("IsNotFoundError(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

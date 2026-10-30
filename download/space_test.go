package download

import (
	"errors"
	"testing"
)

func TestSpacePreflightAccountsForAllocatedTemporaryFile(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		total, allocated, free int64
		reject                 bool
	}{
		{"new file fits", 1000, 0, 1000, false},
		{"new file too large", 1000, 0, 999, true},
		{"allocated prefix reused", 1000, 900, 100, false},
		{"allocated prefix insufficient", 1000, 900, 99, true},
		{"fully preallocated", 1000, 1024, 0, false},
		{"unknown total", -1, 0, 0, false},
		{"unknown allocation", 1000, -1, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSpaceRequirement(tc.total, tc.allocated, tc.free)
			var spaceErr *insufficientSpaceError
			if errors.As(err, &spaceErr) != tc.reject {
				t.Fatalf("space decision: %v", err)
			}
		})
	}
}

func TestStorageErrorDescriptionPreservesUnderlyingError(t *testing.T) {
	err := errors.New("unrelated write error")
	if got := describeStorageError(err); got != err {
		t.Fatalf("unrelated error changed: %v", got)
	}
}

package limits_test

import (
	"testing"

	"codeweaver/internal/limits"
)

// TestMaxFileBytes_Default confirms the default limit is 10 MiB when the env var is unset.
func TestMaxFileBytes_Default(t *testing.T) {
	t.Setenv("CODEWEAVER_MAX_FILE_BYTES", "")

	v, err := limits.MaxFileBytes()
	if err != nil {
		t.Fatalf("MaxFileBytes (default): unexpected error: %v", err)
	}
	want := int64(10 * 1024 * 1024)
	if v != want {
		t.Errorf("MaxFileBytes (default): want %d, got %d", want, v)
	}
}

// TestMaxFileBytes_Zero confirms that CODEWEAVER_MAX_FILE_BYTES=0 disables the limit.
func TestMaxFileBytes_Zero(t *testing.T) {
	t.Setenv("CODEWEAVER_MAX_FILE_BYTES", "0")

	v, err := limits.MaxFileBytes()
	if err != nil {
		t.Fatalf("MaxFileBytes (0): unexpected error: %v", err)
	}
	if v != 0 {
		t.Errorf("MaxFileBytes (0): want 0 (no limit), got %d", v)
	}
}

// TestMaxFileBytes_Explicit confirms an explicit byte count is returned correctly.
func TestMaxFileBytes_Explicit(t *testing.T) {
	t.Setenv("CODEWEAVER_MAX_FILE_BYTES", "1048576") // 1 MiB

	v, err := limits.MaxFileBytes()
	if err != nil {
		t.Fatalf("MaxFileBytes (1MiB): unexpected error: %v", err)
	}
	if v != 1048576 {
		t.Errorf("MaxFileBytes (1MiB): want 1048576, got %d", v)
	}
}

// TestMaxFileBytes_Negative confirms that a negative value returns an error.
// The caller should exit with code 2 (usage error) when MaxFileBytes returns an error.
func TestMaxFileBytes_Negative(t *testing.T) {
	t.Setenv("CODEWEAVER_MAX_FILE_BYTES", "-1")

	_, err := limits.MaxFileBytes()
	if err == nil {
		t.Error("MaxFileBytes (-1): want error, got nil")
	}
}

// TestMaxFileBytes_Invalid confirms that a non-integer value returns an error.
func TestMaxFileBytes_Invalid(t *testing.T) {
	t.Setenv("CODEWEAVER_MAX_FILE_BYTES", "invalid")

	_, err := limits.MaxFileBytes()
	if err == nil {
		t.Error("MaxFileBytes (invalid): want error, got nil")
	}
}

// TestMaxFileBytes_LargePositive confirms that very large values are accepted.
func TestMaxFileBytes_LargePositive(t *testing.T) {
	t.Setenv("CODEWEAVER_MAX_FILE_BYTES", "1073741824") // 1 GiB

	v, err := limits.MaxFileBytes()
	if err != nil {
		t.Fatalf("MaxFileBytes (1GiB): unexpected error: %v", err)
	}
	if v != 1073741824 {
		t.Errorf("MaxFileBytes (1GiB): want 1073741824, got %d", v)
	}
}

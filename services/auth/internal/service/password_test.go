// Package service_test tests the auth service business logic without any
// database, Redis, or network dependencies.
package service

import (
	"testing"
)

// TestValidatePassword_tabledriven exercises every ValidatePassword rule branch.
func TestValidatePassword_tabledriven(t *testing.T) {
	tests := []struct {
		name     string
		password string
		wantErr  bool
	}{
		{
			name:     "valid password meets all requirements",
			password: "Str0ng!Password",
			wantErr:  false,
		},
		{
			name:     "exactly 12 chars all classes",
			password: "Abc1!defghij",
			wantErr:  false,
		},
		{
			name:     "too short — 11 chars",
			password: "Abc1!defghi",
			wantErr:  true,
		},
		{
			name:     "missing uppercase",
			password: "abc1!defghijk",
			wantErr:  true,
		},
		{
			name:     "missing lowercase",
			password: "ABC1!DEFGHIJK",
			wantErr:  true,
		},
		{
			name:     "missing digit",
			password: "Abcd!efghijkl",
			wantErr:  true,
		},
		{
			name:     "missing symbol",
			password: "Abcd1efghijkl",
			wantErr:  true,
		},
		{
			name:     "empty string",
			password: "",
			wantErr:  true,
		},
		{
			name:     "unicode symbol counts as symbol",
			password: "Abc1©defghijk",
			wantErr:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePassword(tc.password)
			if tc.wantErr && err == nil {
				t.Errorf("ValidatePassword(%q): expected error, got nil", tc.password)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidatePassword(%q): unexpected error: %v", tc.password, err)
			}
		})
	}
}

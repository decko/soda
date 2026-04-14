package pipeline

import (
	"testing"
)

func TestParsePRRef(t *testing.T) {
	tests := []struct {
		name       string
		prURL      string
		wantOwner  string
		wantRepo   string
		wantNumber string
		wantErr    bool
	}{
		{
			name:       "standard_github_url",
			prURL:      "https://github.com/decko/soda/pull/49",
			wantOwner:  "decko",
			wantRepo:   "soda",
			wantNumber: "49",
		},
		{
			name:       "trailing_slash",
			prURL:      "https://github.com/decko/soda/pull/49/",
			wantOwner:  "decko",
			wantRepo:   "soda",
			wantNumber: "49",
		},
		{
			name:       "different_owner_repo",
			prURL:      "https://github.com/facebook/react/pull/1234",
			wantOwner:  "facebook",
			wantRepo:   "react",
			wantNumber: "1234",
		},
		{
			name:    "invalid_url_no_pull",
			prURL:   "https://github.com/decko/soda/issues/49",
			wantErr: true,
		},
		{
			name:    "too_short",
			prURL:   "https://github.com",
			wantErr: true,
		},
		{
			name:    "empty_url",
			prURL:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, number, err := parsePRRef(tt.prURL)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if owner != tt.wantOwner {
				t.Errorf("owner = %q, want %q", owner, tt.wantOwner)
			}
			if repo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tt.wantRepo)
			}
			if number != tt.wantNumber {
				t.Errorf("number = %q, want %q", number, tt.wantNumber)
			}
		})
	}
}

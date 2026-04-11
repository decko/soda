package ticket

import (
	"testing"
)

func TestExtractCriteria(t *testing.T) {
	tests := []struct {
		name        string
		description string
		want        []string
	}{
		{
			name: "wiki markup h3 heading with bullets",
			description: `Some intro text.

h3. Acceptance Criteria
* Login page loads in under 2 seconds
* Error message shown for invalid credentials
* Session token stored securely

h3. Notes
Some other text.`,
			want: []string{
				"Login page loads in under 2 seconds",
				"Error message shown for invalid credentials",
				"Session token stored securely",
			},
		},
		{
			name: "markdown heading with dashes",
			description: `## Description
Fix the login flow.

## Acceptance Criteria
- Users can log in with email
- Password reset sends email within 1 minute
- Two-factor auth works with TOTP

## Implementation Notes
Use existing auth library.`,
			want: []string{
				"Users can log in with email",
				"Password reset sends email within 1 minute",
				"Two-factor auth works with TOTP",
			},
		},
		{
			name: "plain text heading with colon",
			description: `Description of the ticket.

Acceptance Criteria:
- API returns 200 for valid input
- API returns 400 for missing fields`,
			want: []string{
				"API returns 200 for valid input",
				"API returns 400 for missing fields",
			},
		},
		{
			name: "bold heading",
			description: `Do the thing.

**Acceptance Criteria**
- Feature A works
- Feature B works`,
			want: []string{
				"Feature A works",
				"Feature B works",
			},
		},
		{
			name: "case insensitive",
			description: `h3. ACCEPTANCE CRITERIA
* Item one
* Item two`,
			want: []string{
				"Item one",
				"Item two",
			},
		},
		{
			name: "no acceptance criteria section",
			description: `Just a plain description with no AC section.
This ticket does something useful.`,
			want: nil,
		},
		{
			name:        "empty description",
			description: "",
			want:        nil,
		},
		{
			name: "numbered list items",
			description: `## Acceptance Criteria
1. First criterion
2. Second criterion
3. Third criterion`,
			want: []string{
				"First criterion",
				"Second criterion",
				"Third criterion",
			},
		},
		{
			name: "checkbox items",
			description: `## Acceptance Criteria
- [ ] Unchecked item
- [x] Checked item
- [ ] Another unchecked`,
			want: []string{
				"Unchecked item",
				"Checked item",
				"Another unchecked",
			},
		},
		{
			name: "stops at next heading",
			description: `## Acceptance Criteria
- First AC

## Technical Details
- This is not an AC`,
			want: []string{
				"First AC",
			},
		},
		{
			name: "wiki h2 heading",
			description: `h2. Acceptance Criteria
* Single criterion`,
			want: []string{
				"Single criterion",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractCriteria(tt.description)
			if len(got) != len(tt.want) {
				t.Fatalf("ExtractCriteria() returned %d items, want %d\ngot:  %v\nwant: %v",
					len(got), len(tt.want), got, tt.want)
			}
			for idx := range got {
				if got[idx] != tt.want[idx] {
					t.Errorf("item[%d] = %q, want %q", idx, got[idx], tt.want[idx])
				}
			}
		})
	}
}

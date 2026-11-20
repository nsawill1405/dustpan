package tidy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRulesConfig(t *testing.T) {
	for _, tt := range []struct {
		name, content, jpeg, pdf string
		invalid                  bool
	}{
		{"omitted rules", "age_days = 0", "Images", "Documents", false},
		{"custom rules", "age_days = 0\n[[rules]]\nextensions = [\".JPEG\"]\nfolder = \"My Photos/Other\"", "My Photos/Other", "", false},
		{"no rules", "age_days = 0\nrules = []", "", "", false},
		{"outside", "[[rules]]\nextensions = [\"jpeg\"]\nfolder = \"../away\"", "", "", true},
		{"hidden", "[[rules]]\nextensions = [\"jpeg\"]\nfolder = \".hidden\"", "", "", true},
		{"duplicate", "[[rules]]\nextensions = [\"jpeg\", \".JPEG\"]\nfolder = \"Photos\"", "", "", true},
		{"invalid quiet period", "quiet_for = \"0s\"", "", "", true},
		{"typo", "quitet_for = \"1m\"", "", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "rules.toml")
			if err := os.WriteFile(filename, []byte(tt.content), 0600); err != nil {
				t.Fatal(err)
			}
			r, err := LoadRules(filename)
			if tt.invalid {
				if err == nil {
					t.Fatal("expected invalid rules")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.byExt["jpeg"] != tt.jpeg || r.byExt["pdf"] != tt.pdf {
				t.Fatalf("unexpected rules: %v", r.byExt)
			}
		})
	}
}

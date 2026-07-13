package frontmatter

import (
	"errors"
	"testing"
)

func TestSplit(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantYAML string
		wantBody string
		wantErr  error
	}{
		{
			name:     "basic",
			input:    "---\nkey: value\n---\nbody text\n",
			wantYAML: "key: value\n",
			wantBody: "body text\n",
		},
		{
			name:     "dashes in double-quoted value",
			input:    "---\ndescription: \"a --- b\"\n---\nbody\n",
			wantYAML: "description: \"a --- b\"\n",
			wantBody: "body\n",
		},
		{
			name:     "dashes in block scalar",
			input:    "---\ndescription: |-\n  line one\n  ---\n  line two\n---\nbody\n",
			wantYAML: "description: |-\n  line one\n  ---\n  line two\n",
			wantBody: "body\n",
		},
		{
			name:     "body containing delimiter line",
			input:    "---\nkey: value\n---\nbefore\n---\nafter\n",
			wantYAML: "key: value\n",
			wantBody: "before\n---\nafter\n",
		},
		{
			name:     "closing delimiter with trailing CR",
			input:    "---\r\nkey: value\r\n---\r\nbody\r\n",
			wantYAML: "key: value\r\n",
			wantBody: "body\r\n",
		},
		{
			name:     "closing delimiter with trailing spaces",
			input:    "---\nkey: value\n---   \nbody\n",
			wantYAML: "key: value\n",
			wantBody: "body\n",
		},
		{
			name:     "four dashes must not close",
			input:    "---\nkey: value\n----\nmore: yaml\n---\nbody\n",
			wantYAML: "key: value\n----\nmore: yaml\n",
			wantBody: "body\n",
		},
		{
			name:     "dash dash dash word must not close",
			input:    "---\nkey: value\n--- foo\nmore: yaml\n---\nbody\n",
			wantYAML: "key: value\n--- foo\nmore: yaml\n",
			wantBody: "body\n",
		},
		{
			name:     "empty body",
			input:    "---\nkey: value\n---\n",
			wantYAML: "key: value\n",
			wantBody: "",
		},
		{
			name:     "no trailing newline after closing delimiter",
			input:    "---\nkey: value\n---",
			wantYAML: "key: value\n",
			wantBody: "",
		},
		{
			name:     "empty frontmatter",
			input:    "---\n---\nbody\n",
			wantYAML: "",
			wantBody: "body\n",
		},
		{
			name:    "missing open",
			input:   "key: value\n---\n",
			wantErr: ErrMissing,
		},
		{
			name:    "open must be alone on line",
			input:   "---key: value\n---\n",
			wantErr: ErrMissing,
		},
		{
			name:    "unterminated",
			input:   "---\nkey: value\nno close\n",
			wantErr: ErrUnterminated,
		},
		{
			name:    "lone open delimiter",
			input:   "---\n",
			wantErr: ErrUnterminated,
		},
		{
			name:    "empty input",
			input:   "",
			wantErr: ErrMissing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yamlBlock, body, err := Split([]byte(tt.input))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Split err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Split: %v", err)
			}
			if string(yamlBlock) != tt.wantYAML {
				t.Errorf("yaml = %q, want %q", yamlBlock, tt.wantYAML)
			}
			if body != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}
		})
	}
}

func TestSplitOnlyDelimiters(t *testing.T) {
	// Input that is exactly "---" (no newline) opens but never closes.
	if _, _, err := Split([]byte("---")); !errors.Is(err, ErrUnterminated) {
		t.Fatalf("Split(---) err = %v, want ErrUnterminated", err)
	}
	// "---\n---" closes at EOF with empty yaml and empty body.
	yamlBlock, body, err := Split([]byte("---\n---"))
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(yamlBlock) != 0 || body != "" {
		t.Fatalf("got yaml=%q body=%q, want empty", yamlBlock, body)
	}
}

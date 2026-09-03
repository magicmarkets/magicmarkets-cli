package cli

import "testing"

func TestParseCurrentScore(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    *[2]int
		wantErr bool
	}{
		{name: "empty means unset", in: "", want: nil},
		{name: "simple", in: "1-2", want: &[2]int{1, 2}},
		{name: "zero-zero", in: "0-0", want: &[2]int{0, 0}},
		{name: "double digit", in: "10-2", want: &[2]int{10, 2}},
		{name: "spaces trimmed", in: " 1 - 2 ", want: &[2]int{1, 2}},
		{name: "missing separator", in: "12", wantErr: true},
		{name: "non-numeric home", in: "a-2", wantErr: true},
		{name: "non-numeric away", in: "1-b", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCurrentScore(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseCurrentScore(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("parseCurrentScore(%q) = %v, want %v", tt.in, got, tt.want)
			}
			if got != nil && *got != *tt.want {
				t.Errorf("parseCurrentScore(%q) = %v, want %v", tt.in, *got, *tt.want)
			}
		})
	}
}

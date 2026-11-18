package charset

import (
	"testing"
)

func TestResolveKnownCharsets(t *testing.T) {
	cases := []struct {
		input    string
		wantIANA string
		wantUTF8 bool
	}{
		{"", "US-ASCII", true},
		{"ASCII", "US-ASCII", true},
		{"UNICODE UTF-8", "UTF-8", true},
		{"UTF-8", "UTF-8", true},
		{"8859/1", "ISO-8859-1", false},
		{"ISO-8859-1", "ISO-8859-1", false},
		{"8859/15", "ISO-8859-15", false},
		{"SHIFT_JIS", "Shift_JIS", false},
		{"JIS", "ISO-2022-JP", false},
		{"ISO IR87", "ISO-2022-JP", false},
		{"Windows-1252", "windows-1252", false},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			id, err := Resolve(tc.input)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tc.input, err)
			}
			if id.IANAName != tc.wantIANA {
				t.Errorf("IANAName: got %q want %q", id.IANAName, tc.wantIANA)
			}
			if id.IsUTF8 != tc.wantUTF8 {
				t.Errorf("IsUTF8: got %v want %v", id.IsUTF8, tc.wantUTF8)
			}
		})
	}
}

func TestResolveUnknown(t *testing.T) {
	_, err := Resolve("EBCDIC")
	if err != ErrUnsupported {
		t.Errorf("expected ErrUnsupported, got %v", err)
	}
}

func TestResolveCaseInsensitive(t *testing.T) {
	id, err := Resolve("utf-8")
	if err != nil {
		t.Fatal(err)
	}
	if !id.IsUTF8 {
		t.Error("utf-8 lowercase should be UTF-8 compatible")
	}
	id2, err := Resolve("8859/1")
	if err != nil {
		t.Fatal(err)
	}
	if id2.IsUTF8 {
		t.Error("8859/1 should not be UTF-8 compatible")
	}
}

func TestIsUTF8Compatible(t *testing.T) {
	if !IsUTF8Compatible("UTF-8") {
		t.Error("UTF-8 should be compatible")
	}
	if !IsUTF8Compatible("") {
		t.Error("empty (ASCII) should be compatible")
	}
	if IsUTF8Compatible("8859/1") {
		t.Error("8859/1 should not be compatible")
	}
	if IsUTF8Compatible("SHIFT_JIS") {
		t.Error("Shift_JIS should not be compatible")
	}
	if IsUTF8Compatible("EBCDIC") {
		t.Error("unknown charset should not be compatible")
	}
}

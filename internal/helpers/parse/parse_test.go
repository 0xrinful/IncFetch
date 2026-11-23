package parse

import (
	"testing"
	"time"
)

func TestParseDateAt(t *testing.T) {
	now := time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		in   string
		want time.Time
	}{
		{
			name: "iso date",
			in:   "2025-03-14",
			want: time.Date(2025, 3, 14, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "arabic full date",
			in:   "يناير 05, 2025",
			want: time.Date(2025, 1, 5, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "arabic comma and day first",
			in:   "12 أكتوبر، 2025",
			want: time.Date(2025, 10, 12, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "english month",
			in:   "May 7, 2025",
			want: time.Date(2025, 5, 7, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "today",
			in:   "Today",
			want: time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "yesterday arabic",
			in:   "يوم واحد مضى",
			want: time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "arabic dual hours",
			in:   "منذ ساعتين",
			want: now.Add(-2 * time.Hour),
		},
		{
			name: "arabic dual weeks",
			in:   "منذ أسبوعين",
			want: now.AddDate(0, 0, -14),
		},
		{
			name: "arabic plural months shuhur",
			in:   "منذ 3 شهور",
			want: now.AddDate(0, -3, 0),
		},
		{
			name: "arabic indic digits",
			in:   "منذ ٥ دقائق",
			want: now.Add(-5 * time.Minute),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDateAt(tc.in, now)
			if err != nil {
				t.Fatalf("parseDateAt(%q) unexpected error: %v", tc.in, err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("parseDateAt(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseChapterNumber(t *testing.T) {
	got, err := ParseChapterNumber("الفصل ١٢.٥")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 12.5 {
		t.Fatalf("got %v, want 12.5", got)
	}
}

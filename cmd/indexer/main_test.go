package main

import (
	"testing"
	"time"
)

func TestParseAnalyticsWindowAcceptsBothFlagForms(t *testing.T) {
	want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for _, args := range [][]string{
		{"--from", "2026-01-01T00:00:00Z"},
		{"--from=2026-01-01T00:00:00Z"},
	} {
		from, to, err := parseAnalyticsWindow(args)
		if err != nil {
			t.Fatalf("%v: unexpected error: %v", args, err)
		}
		if !from.Equal(want) {
			t.Errorf("%v: from = %s, want %s", args, from, want)
		}
		if !to.IsZero() {
			t.Errorf("%v: to = %s, want unbounded", args, to)
		}
	}
}

func TestParseAnalyticsWindowDefaultsToUnbounded(t *testing.T) {
	from, to, err := parseAnalyticsWindow(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !from.IsZero() || !to.IsZero() {
		t.Errorf("got (%s, %s), want both unbounded", from, to)
	}
}

// A silently ignored flag would turn a scoped repair into a refresh of all
// history, which on a populated database cannot be undone.
func TestParseAnalyticsWindowRejectsAnythingUnrecognised(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"typo", []string{"--form", "2026-01-01T00:00:00Z"}},
		{"missing value", []string{"--to", "2026-01-01T00:00:00Z", "--from"}},
		{"missing value, inline form", []string{"--from="}},
		{"bare positional", []string{"2026-01-01T00:00:00Z"}},
		{"not a timestamp", []string{"--from", "yesterday"}},
		{"date without time", []string{"--from", "2026-01-01"}},
		{"reversed window", []string{"--from", "2026-02-01T00:00:00Z", "--to", "2026-01-01T00:00:00Z"}},
		{"equal bounds", []string{"--from", "2026-01-01T00:00:00Z", "--to", "2026-01-01T00:00:00Z"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := parseAnalyticsWindow(tt.args); err == nil {
				t.Errorf("args %v were accepted, want an error", tt.args)
			}
		})
	}
}

func TestParseAnalyticsWindowNormalisesToUTC(t *testing.T) {
	from, _, err := parseAnalyticsWindow([]string{"--from", "2026-01-01T00:00:00-03:00"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	if !from.Equal(want) || from.Location() != time.UTC {
		t.Errorf("from = %s (%v), want %s in UTC", from, from.Location(), want)
	}
}

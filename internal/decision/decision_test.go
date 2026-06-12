package decision

import (
	"errors"
	"testing"

	"github.com/behnam-rk/dezhban/internal/monitor"
)

func TestEvaluate(t *testing.T) {
	d := New([]string{"IR", "ru"}) // mixed case → normalized

	tests := []struct {
		name    string
		country string
		err     error
		want    Verdict
	}{
		{"in blocklist", "IR", nil, Block},
		{"in blocklist lowercased input", "ir", nil, Block},
		{"second entry, case-folded config", "RU", nil, Block},
		{"out of blocklist", "US", nil, Allow},
		{"empty country", "", nil, Allow},
		{"lookup error → Allow (Phase 3)", "IR", errors.New("all providers failed"), Allow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := monitor.Result{
				Reading: monitor.Reading{CountryCode: tt.country},
				Err:     tt.err,
			}
			if got := d.Evaluate(r); got != tt.want {
				t.Fatalf("Evaluate(country=%q, err=%v) = %v, want %v", tt.country, tt.err, got, tt.want)
			}
		})
	}
}

func TestNewEmptyBlocklist(t *testing.T) {
	d := New(nil)
	if got := d.Evaluate(monitor.Result{Reading: monitor.Reading{CountryCode: "IR"}}); got != Allow {
		t.Fatalf("empty blocklist must Allow everything, got %v", got)
	}
}

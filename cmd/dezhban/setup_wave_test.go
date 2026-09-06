package main

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/behnam-rk/dezhban/internal/config"
	"github.com/behnam-rk/dezhban/internal/setup"
)

// ids of the questions a wave puts on one form.
func waveIDs(qs []setup.Question) []string {
	var out []string
	for _, q := range qs {
		out = append(out, q.ID)
	}
	return out
}

// drive runs the wave loop for one group the way cmdSetup does, without a
// terminal: each wave is recorded, then answered by `answer` before the next.
func drive(qs []setup.Question, group int, a *setup.Answers, answer func(id string)) [][]string {
	var waves [][]string
	asked := map[string]bool{}
	for {
		wave := nextWave(qs, group, asked, a)
		if len(wave) == 0 {
			return waves
		}
		waves = append(waves, waveIDs(wave))
		for _, q := range wave {
			asked[q.ID] = true
			answer(q.ID)
		}
	}
}

// A huh form binds every field before any is answered, so a question gated on
// another question on the SAME form would be decided by that question's seeded
// default. Step 2 must therefore arrive as two waves — and crucially it must do
// so whichever way autoMode is seeded, because a pinned config seeds it to
// false and so satisfies the manual fields' gate before the user touches it.
func TestStepTwoArrivesInWaves(t *testing.T) {
	for _, tc := range []struct {
		name   string
		pinned []string
		want   [][]string
	}{
		{
			name: "fresh config, automatic seeded on",
			want: [][]string{{"autoMode"}},
		},
		{
			name:   "pinned config, automatic seeded off",
			pinned: []string{"utun9"},
			want:   [][]string{{"autoMode"}, {"tunnels", "profileFiles", "endpoints"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.VPN.TunnelInterfaces = tc.pinned
			qs := setup.Questions(setup.Options{Config: &cfg, GOOS: "darwin"})

			// The user answers nothing: every question keeps its seeded value.
			got := drive(qs, 2, setup.NewAnswers(qs), func(string) {})
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("waves = %v, want %v", got, tc.want)
			}
		})
	}
}

// Off macOS there is no live discovery, so the endpoint question is ungated and
// rides on the FIRST prompt beside the tickbox — a Linux user who leaves
// automatic detection on must still be asked for a server, or the config cannot
// enforce. That makes the wave shape genuinely platform-dependent, which is why
// it is pinned rather than left to the darwin cases above.
func TestOffMacOSTheEndpointQuestionRidesTheFirstWave(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			cfg := config.Default()
			qs := setup.Questions(setup.Options{Config: &cfg, GOOS: goos})
			a := setup.NewAnswers(qs)

			// Automatic detection left on, the recommended answer.
			got := drive(qs, 2, a, func(string) {})
			want := [][]string{{"autoMode", "endpoints"}}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("waves = %v, want %v", got, want)
			}
		})
	}
}

// Ticking automatic detection retracts the manual half of step 2. The three
// gated questions must then never be put on a form at all — the wave loop is
// what makes that true, and it is the case the old mark-inside-the-pass loop got
// wrong by shipping all four on one form.
func TestTickingAutomaticRetractsTheManualFields(t *testing.T) {
	cfg := config.Default()
	cfg.VPN.TunnelInterfaces = []string{"utun9"} // seeds autoMode false
	cfg.VPN.Endpoints = []string{"203.0.113.7"}
	qs := setup.Questions(setup.Options{Config: &cfg, GOOS: "darwin"})
	a := setup.NewAnswers(qs)

	seen := map[string]bool{}
	drive(qs, 2, a, func(id string) {
		seen[id] = true
		if id == "autoMode" {
			a.Set("autoMode", "true") // the user ticks it after all
		}
	})

	if !seen["autoMode"] {
		t.Fatal("autoMode was never asked")
	}
	for _, id := range []string{"tunnels", "profileFiles", "endpoints"} {
		if seen[id] {
			t.Errorf("%s was put on a form despite automatic detection being on", id)
		}
	}

	// And nothing it would have written may reach the config.
	after := cfg
	in := a.Input(strconv.Itoa(cfg.Hysteresis), nil)
	in.MacOS, in.ConfigExisted = true, true
	setup.Apply(&after, in)
	config.Normalize(&after)
	if got := after.VPN.Endpoints; !reflect.DeepEqual(got, []string{"203.0.113.7"}) {
		t.Errorf("endpoints = %v, want the configured one untouched", got)
	}
}

// Every question whose gate ends up satisfied must have been put on some form.
func TestEveryGatedQuestionIsReachable(t *testing.T) {
	cfg := config.Default()
	cfg.VPN.TunnelInterfaces = []string{"utun9"}
	qs := setup.Questions(setup.Options{Config: &cfg, GOOS: "darwin"})
	a := setup.NewAnswers(qs)

	seen := map[string]bool{}
	drive(qs, 2, a, func(id string) {
		seen[id] = true
		if id == "autoMode" {
			a.Set("autoMode", "false")
		}
	})
	for _, q := range qs {
		if q.Group == 2 && a.ShouldAsk(q) && !seen[q.ID] {
			t.Errorf("%s has a satisfied gate but was never asked", q.ID)
		}
	}
}

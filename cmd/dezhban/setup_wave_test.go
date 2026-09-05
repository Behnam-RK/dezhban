package main

import (
	"reflect"
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

// The wave loop must never strand a question. Whatever the user answers, every
// question whose gate ends up satisfied has to have been put on some form.
func TestEveryGatedQuestionIsReachable(t *testing.T) {
	cfg := config.Default()
	cfg.VPN.TunnelInterfaces = []string{"utun9"} // seeds autoMode false
	qs := setup.Questions(setup.Options{Config: &cfg, GOOS: "darwin"})
	a := setup.NewAnswers(qs)

	// The user unticks nothing but re-affirms manual mode when asked.
	seen := map[string]bool{}
	for _, w := range drive(qs, 2, a, func(id string) {
		seen[id] = true
		if id == "autoMode" {
			a.Set("autoMode", "false")
		}
	}) {
		_ = w
	}
	for _, q := range qs {
		if q.Group != 2 {
			continue
		}
		if a.ShouldAsk(q) && !seen[q.ID] {
			t.Errorf("%s has a satisfied gate but was never asked", q.ID)
		}
	}
}

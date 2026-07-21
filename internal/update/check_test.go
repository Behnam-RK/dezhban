package update

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v0.5.0","html_url":"https://example/releases/tag/v0.5.0"}`))
	}))
	defer srv.Close()

	restore := apiLatestURL
	apiLatestURL = srv.URL
	defer func() { apiLatestURL = restore }()

	cases := []struct {
		name      string
		current   string
		wantAvail bool
		wantCur   string
	}{
		{"older tagged release", "v0.4.0", true, "0.4.0"},
		{"same as latest", "v0.5.0", false, "0.5.0"},
		{"newer than latest (shouldn't happen, but must not crash)", "v0.6.0", false, "0.6.0"},
		{"git-describe dev build", "v0.4.0-3-gabc123-dirty", false, ""},
		{"toolchain devel marker", "(devel)", false, ""},
		{"rc build behind latest final", "v0.4.0-rc.1", true, "0.4.0"},
		{"rc build matching latest final's line", "v0.5.0-rc.1", false, "0.5.0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := Check(c.current, srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			if res.Available != c.wantAvail {
				t.Errorf("Available = %v, want %v", res.Available, c.wantAvail)
			}
			if res.Current != c.wantCur {
				t.Errorf("Current = %q, want %q", res.Current, c.wantCur)
			}
			if res.Latest != "0.5.0" {
				t.Errorf("Latest = %q, want 0.5.0", res.Latest)
			}
		})
	}
}

func TestNormalizeVersion(t *testing.T) {
	cases := map[string]string{
		"v0.3.0":       "0.3.0",
		"0.3.0":        "0.3.0",
		"v0.3.0-rc.1":  "0.3.0", // real rc release build — comparable by base core
		"v0.3.0-rc.10": "0.3.0",
		// git-describe dev-build tails are NOT comparable, on purpose (see the
		// doc comment on normalizeVersion) — this is the exact case that was a
		// real bug here: the first version of this function stripped the tail
		// and returned "0.3.0", silently treating an arbitrary dev build as
		// equivalent to a clean release.
		"v0.3.0-5-gabc123":       "",
		"v0.3.0-5-gabc123-dirty": "",
		"(devel)":                "",
		"":                       "",
		"v0.3":                   "",
	}
	for in, want := range cases {
		if got := normalizeVersion(in); got != want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSemverLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.1.0", "0.2.0", true},
		{"0.2.0", "0.1.0", false},
		{"0.1.0", "0.1.0", false},
		{"1.0.0", "0.9.9", false},
		{"0.9.9", "1.0.0", true},
		{"0.1.9", "0.1.10", true},
	}
	for _, c := range cases {
		if got := semverLess(c.a, c.b); got != c.want {
			t.Errorf("semverLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

package filter

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestAdGuardAndHostsRules(t *testing.T) {
	f := New()
	stats, err := f.Load(strings.NewReader(`# comment
! another comment
||ads.example^ # block subdomains too
@@||safe.ads.example^
0.0.0.0 tracker.example second.example # hosts format
127.0.0.1 loop.example
||bad.example^$important
||*.wild.example^
`))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Rules != 5 || stats.Unsupported != 2 {
		t.Errorf("stats = %+v, want 5 rules and 2 unsupported", stats)
	}
	for domain, want := range map[string]bool{
		"ads.example.": true, "x.ads.example.": true,
		"safe.ads.example.": false, "a.safe.ads.example.": false,
		"notads.example.": false, "EXAMPLE.ADS.EXAMPLE.": true,
		"tracker.example.": true, "second.example.": true,
		"loop.example.": true, "bad.example.": false,
		"x.wild.example.": false, "other.example.": false,
	} {
		if got := f.Blocked(domain); got != want {
			t.Errorf("Blocked(%q) = %v, want %v", domain, got, want)
		}
	}
}

func TestFilterLongLineAndEmptyLabels(t *testing.T) {
	f := New()
	_, err := f.Load(strings.NewReader("||example.org^\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"example.org.evil.", "foo..example.org.", "", "."} {
		if f.Blocked(name) {
			t.Errorf("malformed or unrelated name was blocked: %q", name)
		}
	}
}

func TestValidationMatchesLoadedRuleCounts(t *testing.T) {
	rules := "! comment\n||ads.example^\n@@||safe.ads.example^\n0.0.0.0 localhost tracker.example another.example\n||unsupported.example^$important\n"
	want, err := New().Load(strings.NewReader(rules))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Validate(strings.NewReader(rules), true)
	if err != nil || got != want {
		t.Errorf("Validate = %+v, %v; Load = %+v", got, err, want)
	}
	if _, err := Validate(strings.NewReader("<html>error</html>"), true); err == nil {
		t.Fatal("zero supported rules must not replace live subscription")
	}
}

func TestLoadFilesAndLocalhost(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ads.txt"), []byte("127.0.0.1 localhost localhost.localdomain ads.example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f, stats, err := LoadFiles(dir, []string{"ads.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Rules != 1 || !f.Blocked("ads.example.") || f.Blocked("localhost.") {
		t.Errorf("unexpected hosts parsing: %+v", stats)
	}
}

func BenchmarkBlocked50k(b *testing.B) {
	var lines strings.Builder
	for i := range 50000 {
		lines.WriteString("||ad")
		lines.WriteString(strconv.Itoa(i))
		lines.WriteString(".example^\n")
	}
	f := New()
	if _, err := f.Load(strings.NewReader(lines.String())); err != nil {
		b.Fatal(err)
	}
	for _, tc := range []struct {
		name, domain string
		want         bool
	}{
		{"hit", "www.ad42420.example.", true},
		{"miss", "www.normal.example.", false},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if f.Blocked(tc.domain) != tc.want {
					b.Fatal("unexpected match")
				}
			}
		})
	}
	m := &Manager{}
	m.active.Store(f)
	b.Run("manager_hit", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if !m.Blocked("www.ad42420.example.") {
				b.Fatal("unexpected miss")
			}
		}
	})
}

func BenchmarkFilterLoad50k(b *testing.B) {
	var lines strings.Builder
	for i := range 50000 {
		lines.WriteString("||ad")
		lines.WriteString(strconv.Itoa(i))
		lines.WriteString(".example^\n")
	}
	rules := lines.String()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		f := New()
		if _, err := f.Load(strings.NewReader(rules)); err != nil {
			b.Fatal(err)
		}
	}
}

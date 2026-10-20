package locale

import (
	"runtime"
	"testing"
	"time"

	"github.com/goodsign/monday"
)

func TestNormalize(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want monday.Locale
		ok   bool
	}{
		{"de_DE", monday.LocaleDeDE, true},
		{"de-DE", monday.LocaleDeDE, true},
		{"de_DE.UTF-8", monday.LocaleDeDE, true},
		{"de_DE@euro", monday.LocaleDeDE, true},
		{"de", monday.LocaleDeDE, true},
		{"en", monday.LocaleEnUS, true},
		{"en_US", monday.LocaleEnUS, true},
		{"en-GB", monday.LocaleEnGB, true},
		{"fr", monday.LocaleFrFR, true},
		{" pt-BR ", monday.LocalePtBR, true},
		{"zh_CN", monday.LocaleZhCN, true},
		// "C" and "POSIX" count as unset.
		{"C", "", false},
		{"POSIX", "", false},
		{"", "", false},
		{"  ", "", false},
		// Unsupported locales.
		{"xx_YY", "", false},
		{"de_AT", "", false}, // no Austrian German in monday
		{"Klingon", "", false},
	} {
		got, ok := Normalize(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("Normalize(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestFromEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows reads the user locale, not LC_ALL/LC_TIME/LANG (see TestWindowsUserLocale)")
	}
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	l, ok := FromEnvironment(env(map[string]string{"LC_ALL": "de_DE.UTF-8", "LANG": "fr_FR"}))
	if !ok || l != monday.LocaleDeDE {
		t.Errorf("LC_ALL wins: got %q, %v; want de_DE, true", l, ok)
	}
	l, ok = FromEnvironment(env(map[string]string{"LC_TIME": "fr_FR", "LANG": "de_DE"}))
	if !ok || l != monday.LocaleFrFR {
		t.Errorf("LC_TIME beats LANG: got %q, %v; want fr_FR, true", l, ok)
	}
	// C and POSIX count as unset; the next variable applies.
	l, ok = FromEnvironment(env(map[string]string{"LC_ALL": "C", "LANG": "en_GB"}))
	if !ok || l != monday.LocaleEnGB {
		t.Errorf("LC_ALL=C counts as unset: got %q, %v; want en_GB, true", l, ok)
	}
	if _, ok = FromEnvironment(env(map[string]string{"LANG": "xx_YY"})); ok {
		t.Error("unsupported environment locale: want ok == false")
	}
	if _, ok = FromEnvironment(env(map[string]string{"LC_ALL": "xx_YY", "LANG": "de_DE"})); ok {
		t.Error("unsupported highest-priority locale must fall back, not select LANG")
	}
	if _, ok = FromEnvironment(env(map[string]string{})); ok {
		t.Error("unset environment: want ok == false")
	}
}

func TestFormat(t *testing.T) {
	loc := time.FixedZone("CEST", 2*3600)
	ts := time.Date(2026, 10, 18, 17, 22, 10, 0, loc) // a Sunday
	for _, tc := range []struct {
		locale monday.Locale
		want   string
	}{
		{monday.LocaleDeDE, "So., 18. Okt. 2026 17:22:10 CEST"},
		{monday.LocaleEnUS, "Sun, Oct 18, 2026 17:22:10 CEST"},
		{monday.LocaleEnGB, "Sun, 18 Oct 2026 17:22:10 CEST"},
		{monday.LocaleFrFR, "dim, 18 oct 2026 17:22:10 CEST"},
	} {
		if got := Format(ts, tc.locale); got != tc.want {
			t.Errorf("Format(%s) = %q, want %q", tc.locale, got, tc.want)
		}
	}
	// An unknown locale falls back to en_US.
	if got := Format(ts, "xx_YY"); got != Format(ts, monday.LocaleEnUS) {
		t.Errorf("Format(xx_YY) = %q, want the en_US format %q", got, Format(ts, monday.LocaleEnUS))
	}
}

func TestSupported(t *testing.T) {
	if !Supported("de_DE") || !Supported("en") {
		t.Error("de_DE and en must be supported")
	}
	if Supported("xx_YY") || Supported("") {
		t.Error("xx_YY and the empty string must be unsupported")
	}
}

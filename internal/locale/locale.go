// Package locale resolves and validates the locale used for the dates in
// mailfire run's job reports (task 33). Only the dates are localized —
// month and weekday names and the date order — through
// github.com/goodsign/monday; all labels and texts stay English.
//
// Accepted spellings: "de_DE", "de-DE", "de_DE.UTF-8", "de_DE@euro", and
// a bare language ("de"), which selects that language's main locale. "C"
// and "POSIX" count as unset. The resolution order is --locale, then
// [run].locale / MAILFIRE_RUN_LOCALE, then the environment (LC_ALL,
// LC_TIME, LANG; on Windows the user's locale), then "en_US".
package locale

import (
	"runtime"
	"strings"
	"time"

	"github.com/goodsign/monday"
)

// Fallback is the locale used when nothing else selects one.
const Fallback = monday.LocaleEnUS

// supported is the set of locales monday can format.
var supported = func() map[monday.Locale]bool {
	m := make(map[monday.Locale]bool)
	for _, l := range monday.ListLocales() {
		m[l] = true
	}
	return m
}()

// Supported reports whether s names a locale monday can format.
func Supported(s string) bool {
	_, ok := Normalize(s)
	return ok
}

// Normalize maps an accepted spelling to a supported monday locale. ok is
// false when s is empty, "C", "POSIX", or unsupported.
func Normalize(s string) (monday.Locale, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "C" || s == "POSIX" {
		return "", false
	}
	// Drop encoding ("de_DE.UTF-8") and modifier ("de_DE@euro") suffixes.
	if i := strings.IndexAny(s, ".@"); i >= 0 {
		s = s[:i]
	}
	s = strings.ReplaceAll(s, "-", "_")
	lang, region, hasRegion := strings.Cut(s, "_")
	if lang == "" {
		return "", false
	}
	lang = strings.ToLower(lang)
	if hasRegion && region != "" {
		// Strip any further territory/variant components ("zh_Hans_CN").
		region, _, _ = strings.Cut(strings.ToUpper(region), "_")
		if l := monday.Locale(lang + "_" + region); supported[l] {
			return l, true
		}
		// A region that is not supported makes the locale unsupported:
		// falling back to the language's main locale would silently use
		// another country's conventions.
		return "", false
	}
	return languageDefault(lang)
}

// languageDefault returns the main locale of a bare language: the
// locale whose region repeats the language ("de_DE" for "de") when there
// is one, else the language's first supported locale.
func languageDefault(lang string) (monday.Locale, bool) {
	if l := monday.Locale(lang + "_" + strings.ToUpper(lang)); supported[l] {
		return l, true
	}
	for _, l := range monday.ListLocales() {
		if strings.HasPrefix(string(l), lang+"_") {
			return l, true
		}
	}
	return "", false
}

// FromEnvironment reads the process locale: LC_ALL, LC_TIME, LANG (first
// nonempty; "C" and "POSIX" count as unset), on Windows the user's
// locale. An unset or unsupported environment locale yields ok == false;
// the caller silently falls back to Fallback.
func FromEnvironment(getenv func(string) string) (monday.Locale, bool) {
	if runtime.GOOS == "windows" {
		return Normalize(windowsUserLocale())
	}
	for _, v := range []string{"LC_ALL", "LC_TIME", "LANG"} {
		s := strings.TrimSpace(getenv(v))
		if s != "" && s != "C" && s != "POSIX" {
			return Normalize(s)
		}
	}
	return "", false
}

// dateLayouts overrides the default date layout where the locale's
// convention differs. The layouts carry English month and weekday names,
// which monday.Format translates; the time zone abbreviation is never
// translated. The time is 24-hour with seconds.
var dateLayouts = map[monday.Locale]string{
	monday.LocaleEnUS: "Mon, Jan 2, 2006 15:04:05 MST",
	monday.LocaleDeDE: "Mon., 2. Jan. 2006 15:04:05 MST",
	monday.LocaleJaJP: "2006 Jan 2 (Mon) 15:04:05 MST",
	monday.LocaleKoKR: "2006 Jan 2 (Mon) 15:04:05 MST",
	monday.LocaleZhCN: "2006 Jan 2 Mon 15:04:05 MST",
	monday.LocaleZhHK: "2006 Jan 2 Mon 15:04:05 MST",
	monday.LocaleZhTW: "2006 Jan 2 Mon 15:04:05 MST",
}

// defaultDateLayout is the day-month-year order common to most locales.
const defaultDateLayout = "Mon, 2 Jan 2006 15:04:05 MST"

// Format renders t with the locale's month and weekday names and date
// order, a 24-hour time with seconds, and the time-zone abbreviation,
// e.g. "So., 18. Okt. 2026 17:22:10 CEST" (de_DE) or
// "Sun, Oct 18, 2026 17:22:10 CEST" (en_US). An empty or unsupported
// locale formats as Fallback.
func Format(t time.Time, l monday.Locale) string {
	if !supported[l] {
		l = Fallback
	}
	layout, ok := dateLayouts[l]
	if !ok {
		layout = defaultDateLayout
	}
	return monday.Format(t, layout, l)
}

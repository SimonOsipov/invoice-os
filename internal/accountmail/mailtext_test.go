package accountmail

import (
	"maps"
	"strings"
	"testing"
	"unicode/utf8"
)

const mark = "​"

func TestUnlinked_BreaksEachLinkStart(t *testing.T) {
	cases := map[string]string{
		"evil.com":             "evil." + mark + "com",
		"www.evil.com":         "www." + mark + "evil." + mark + "com",
		"WWW.EVIL.COM":         "WWW." + mark + "EVIL." + mark + "COM",
		"https://evil.com/pay": "https" + mark + "://evil." + mark + "com/pay",
		"HTTP://x":             "HTTP" + mark + "://x",
		"http://x":             "http" + mark + "://x",
		"a@b.co":               "a@" + mark + "b." + mark + "co",
		"pay@evil.com":         "pay@" + mark + "evil." + mark + "com",
		"mailto:pay@evil.com":  "mailto:pay@" + mark + "evil." + mark + "com",
		"bücher.de":            "bücher." + mark + "de",
		"shop.中国":              "shop." + mark + "中国",
		"St.Mary":              "St." + mark + "Mary",
		"ÉVIL.COM":             "ÉVIL." + mark + "COM",
		"ｅｖｉｌ.ｃｏｍ":             "ｅｖｉｌ." + mark + "ｃｏｍ",
		"a.b.co":               "a.b." + mark + "co",
		"shop1.com":            "shop1." + mark + "com",
		"(evil.com)":           "(evil." + mark + "com)",
		"http:///x":            "http" + mark + ":///x",
		"evil\u00ad.com":       "evil\u00ad." + mark + "com",
		"evil\u200d.com":       "evil\u200d." + mark + "com",
		"evil.\u00adcom":       "evil." + mark + "\u00adcom",
		"cafe\u0301.com":       "cafe\u0301." + mark + "com",
		"pay\u00ad@evil.com":   "pay\u00ad@" + mark + "evil." + mark + "com",
		"evil⁠.com":            "evil⁠." + mark + "com",
		"evil\uFEFF.com":       "evil\uFEFF." + mark + "com",
		"evil᠎.com":            "evil᠎." + mark + "com",
		"evil­‍⁠.com":          "evil­‍⁠." + mark + "com",
		"evil.­‍⁠com":          "evil." + mark + "­‍⁠com",
		"evil.c⁠om":            "evil." + mark + "c⁠om",
		"­evil.com":            "­evil." + mark + "com",
		"evil.com­":            "evil." + mark + "com­",
		"evil.ćom":            "evil." + mark + "ćom",
		"pay⁠@evil.com":        "pay⁠@" + mark + "evil." + mark + "com",
		"pay@⁠evil.com":        "pay@" + mark + "⁠evil." + mark + "com",
		"pay­­@­­evil.com":     "pay­­@" + mark + "­­evil." + mark + "com",
	}
	for in, want := range cases {
		if got := Unlinked(in); got != want {
			t.Errorf("Unlinked(%q) = %q, want %q", in, got, want)
		}
	}
}

var unlinkedNoMark = []string{
	"Adaeze Okafor", "Okafor & Sons Ltd", "Okafor & Sons Ltd.", "Dr. Okafor", "J.R. Okafor", "J\u00ad.R. Okafor", "U.S.A. Ltd", "\u00ad.com", "evil.\u00ad", "@\u00adevil", "J.\u00adR. Okafor", "a@\u00ad b", "Acme @\u00adHome", "evil\u00ad.\u00ad",
	"e.g. Okafor", "Acme @ Home", "Acme @Home", "visit .com", "7.5% VAT", "INV-2026.01", "a@ b", "",
}

func TestUnlinked_LeavesTextWithNoLinkStart(t *testing.T) {
	for _, in := range unlinkedNoMark {
		if got := Unlinked(in); got != in {
			t.Errorf("Unlinked(%q) = %q, want it unchanged", in, got)
		}
	}
}

func TestUnlinked_IsIdempotentAndReversible(t *testing.T) {
	ins := append([]string{"evil.com", "www.evil.com", "https://evil.com/pay", "a@b.co", "mailto:pay@evil.com", "bücher.de", "shop.中国", "St.Mary"}, unlinkedNoMark...)
	for _, in := range ins {
		once := Unlinked(in)
		if twice := Unlinked(once); twice != once {
			t.Errorf("Unlinked not idempotent on %q: %q then %q", in, once, twice)
		}
		if strings.ReplaceAll(once, mark, "") != strings.ReplaceAll(in, mark, "") {
			t.Errorf("Unlinked(%q) = %q changes the visible text", in, once)
		}
	}
}

func TestUnlinked_InputMarksAreNormalised(t *testing.T) {
	cases := map[string]string{
		"evil." + mark + "com":    "evil." + mark + "com",
		"ev" + mark + "il.com":    "evil." + mark + "com",
		"J." + mark + "R. Okafor": "J.R. Okafor",
	}
	for in, want := range cases {
		if got := Unlinked(in); got != want {
			t.Errorf("Unlinked(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMailName_CutsToSixtyRunes(t *testing.T) {
	cases := []struct{ in, want string }{
		{strings.Repeat("a", 61), strings.Repeat("a", 59) + "…"},
		{strings.Repeat("a", 60), strings.Repeat("a", 60)},
		{strings.Repeat("é", 200), strings.Repeat("é", 59) + "…"},
		{strings.Repeat("😀", 61), strings.Repeat("😀", 59) + "…"},
		{"", ""},
	}
	for _, c := range cases {
		got := MailName(c.in)
		if got != c.want || !utf8.ValidString(got) {
			t.Errorf("MailName(%d runes) = %q, want %q", utf8.RuneCountInString(c.in), got, c.want)
		}
	}
}

func TestMailName_CountsOnlyVisibleRunes(t *testing.T) {
	got := MailName(strings.Repeat("a", 50) + ".com" + strings.Repeat("b", 10))
	plain := []rune(strings.ReplaceAll(got, mark, ""))
	if len(plain) != NameChars || plain[len(plain)-1] != '…' {
		t.Errorf("got %d visible runes ending %q, want %d ending …", len(plain), string(plain[len(plain)-1]), NameChars)
	}
	if !strings.Contains(got, "a."+mark+"com") {
		t.Errorf("%q lacks the mark after the dot", got)
	}
}

func TestMailName_InputMarksDoNotCountTowardTheCut(t *testing.T) {
	want := strings.Repeat("a", 50) + strings.Repeat("b", 10)
	if got := MailName(strings.Repeat("a", 50) + strings.Repeat(mark, 20) + strings.Repeat("b", 10)); got != want {
		t.Errorf("MailName = %q, want %q", got, want)
	}
}

func TestMailName_CutEdges(t *testing.T) {
	a58, a51 := strings.Repeat("a", 58), strings.Repeat("a", 51)
	cases := []struct{ in, want string }{
		{"https://evil.com/pay", "https" + mark + "://evil." + mark + "com/pay"},
		{a58 + ".com", a58 + ".…"},
		{a51 + " https://evil.com", a51 + " https:/…"},
	}
	for _, c := range cases {
		if got := MailName(c.in); got != c.want {
			t.Errorf("MailName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMailNames_NeutralisesBothNames(t *testing.T) {
	got := MailNames("pay@evil.com", "Ada https://evil.com")
	want := map[string]string{"workspace_name": "pay@" + mark + "evil." + mark + "com", "display_name": "Ada https" + mark + "://evil." + mark + "com"}
	if !maps.Equal(got, want) {
		t.Errorf("MailNames = %q, want %q", got, want)
	}
}

func TestMailNames_EmptyNameStaysEmpty(t *testing.T) {
	got := MailNames("Obi Partners", "")
	if want := map[string]string{"workspace_name": "Obi Partners", "display_name": ""}; !maps.Equal(got, want) {
		t.Errorf("MailNames = %q, want %q", got, want)
	}
}

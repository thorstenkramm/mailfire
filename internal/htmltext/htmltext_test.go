package htmltext

import (
	"strings"
	"testing"
)

func TestText(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{"empty", "", ""},
		{"plain text", "hello world", "hello world\n"},
		{"markup only", "<p></p>", ""},
		{"simple fragment", "<p>Hello <b>world</b></p>", "Hello world\n"},
		{"entities decoded", `<p>Fish &amp; Chips &mdash; 5&nbsp;&euro; &lt;3</p>`, "Fish & Chips — 5 € <3\n"},
		{"blocks separated", "<p>one</p><p>two</p>", "one\n\ntwo\n"},
		{"br breaks line", "a<br>b<br/>c", "a\nb\nc\n"},
		{"headings and list", "<h1>Title</h1><ul><li>a</li><li>b</li></ul>", "Title\n\na\nb\n"},
		{"comments removed", "a<!-- hidden -->b", "ab\n"},
		{"unterminated comment", "a<!-- hidden", "a\n"},
		{"conditional comment", "<!--[if mso]>outlook<![endif]-->shown", "shown\n"},
		{"doctype removed", "<!DOCTYPE html>\n<p>x</p>", "x\n"},
		{"processing instruction", "<?xml version=\"1.0\"?>\n<p>x</p>", "x\n"},
		{"script dropped", `<p>a</p><script>alert("x")</script><p>b</p>`, "a\n\nb\n"},
		{"style dropped", "<style>p{color:red}</style><p>x</p>", "x\n"},
		{"title dropped", "<head><title>Subj</title></head><p>body</p>", "body\n"},
		{"unterminated script", "<p>a</p><script>never closed", "a\n"},
		{"stray less-than kept", "1 < 2 &lt; 3", "1 < 2 < 3\n"},
		{"uppercase tags", "<P>Hi</P><BR><B>bold</B>", "Hi\n\nbold\n"},
		{"quoted gt in attribute", `<p title="a>b">x</p>`, "x\n"},
		{"quoted gt in closing", `<p>x</p class="a>b"><p>y</p>`, "x\n\ny\n"},
		{"unterminated tag", "<p>a</p><b class=", "a\n"},
		{"closing tag without opener", "text</p>", "text\n"},
		{"whitespace collapsed", "<p>  lots   of\n\t  space  </p>", "lots of space\n"},
		{"bom ignored", "\ufeff<p>x</p>", "x\n"},
		{"crlf normalized", "<p>a</p>\r\n<p>b</p>", "a\n\nb\n"},
		{"blank runs collapse", "<p>a</p>\n\n\n<p>b</p>", "a\n\nb\n"},
		// Adversarial input: none of these may swallow following text.
		{"digit after less-than is text", "I <3 you. More <b>here</b>", "I <3 you. More here\n"},
		{"apostrophe in unquoted attribute", "<a title=it's>x</a> after", "x after\n"},
		{"gt in quoted href", `<a href="x>y">link</a> after`, "link after\n"},
		{"nested quotes in attribute", `<a title='say "hi>"'>q</a> <a b="c'>d">e</a>`, "q e\n"},
		{"double dash in comment", "a<!-- x -- y -->b", "ab\n"},
		{"abrupt empty comment", "a<!-->b", "ab\n"},
		{"script end tag in string", `<script>var x = "</script>";</script>after`, `";after` + "\n"},
		{"less-than in script", "<script>if (a<b) x()</script>after", "after\n"},
		{"lookalike end tag in script", "<script>a</scriptx>b</script>c", "c\n"},
		{"lookalike tags kept", "<scripts>keep</scripts> <titlex>this</titlex>", "keep this\n"},
		{"invalid utf-8 in script", "<script>\xff\xff\xff\xff</script>after", "after\n"},
		{"multibyte across tags", "<p>héllo</p><b>wörld</b>€", "héllo\n\nwörld€\n"},
		{"svg title dropped", "<p>a</p><svg><title>Icon</title><path/></svg><p>b</p>", "a\n\nb\n"},
		{"cdata is bogus comment", "<![CDATA[x<y]]>after", "after\n"},
		{"fragment title dropped", "<title>t</title><p>x</p>", "x\n"},
		{"unterminated title", "<p>x</p><title>t", "x\n"},
		{"noscript dropped", "<head><noscript>ns</noscript></head><body>b</body>", "b\n"},
		{"unterminated quoted attribute", `<p>a</p><a href="x>text`, "a\n"},
		{"nbsp collapsed", "<p>a&nbsp;&nbsp;b</p>", "a b\n"},
		{"double-escaped entity", "<p>&amp;lt;</p>", "&lt;\n"},
		{"inline spacing kept", "<b>a</b> <i>b</i>", "a b\n"},
		{"pre keeps layout", "<p>log:</p><pre>\nline 1\n  indented\n\n\nend  </pre><p>x</p>",
			"log:\n\nline 1\n  indented\n\n\nend\n\nx\n"},
		{"pre entities decoded", "<pre>a &lt; b</pre>", "a < b\n"},
		{"pre leading spaces", "<pre>  x</pre>", "  x\n"},
		{
			"full document",
			"<!DOCTYPE html>\n<html>\n<head>\n<title>S &amp; S</title>\n" +
				"<style>body{margin:0}</style>\n</head>\n<body>\n" +
				"<h1 style=\"color:#b23b12;\">Hi there</h1>\n" +
				"<p>Fish &amp; Chips</p>\n</body>\n</html>\n",
			"Hi there\n\nFish & Chips\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Text(tc.src); got != tc.want {
				t.Errorf("Text(%q) = %q, want %q", tc.src, got, tc.want)
			}
		})
	}
}

// FuzzText checks the invariants every plain-text part relies on: no
// panic, and the result is empty or ends with exactly one newline.
func FuzzText(f *testing.F) {
	for _, s := range []string{
		"<p>a</p>", "<script>x</script>y", "<a title=it's>x", "<pre>\n a\n</pre>",
		"<!-- x --", "<![CDATA[", "\xff<b>\xfe</b>", "a&nbsp;&amp", "</",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		got := Text(src)
		if got == "" {
			return
		}
		if !strings.HasSuffix(got, "\n") || strings.HasSuffix(got, "\n\n") {
			t.Errorf("Text(%q) = %q: want exactly one trailing newline", src, got)
		}
	})
}

// TestTextLarge guards against quadratic behavior on big documents.
func TestTextLarge(t *testing.T) {
	src := strings.Repeat("<p>x <b>y</b></p><script>z</script>", 50000)
	got := Text(src)
	if want := 50000 * len("x y\n\n"); len(got) != want-1 {
		t.Errorf("len(Text) = %d, want %d", len(got), want-1)
	}
}

package htmltext

import "testing"

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
		{"crlf normalized", "<p>a</p>\r\n<p>b</p>", "a\n\nb\n"},
		{"blank runs collapse", "<p>a</p>\n\n\n<p>b</p>", "a\n\nb\n"},
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

package markdown

import (
	"strings"
	"testing"
)

func TestIsLocalImage(t *testing.T) {
	for _, tc := range []struct {
		dest  string
		local bool
	}{
		{"img.png", true},
		{"./assets/logo.png", true},
		{"../up/one.png", true},
		{"/abs/path/logo.png", true},
		{"~/Pictures/logo.png", true},
		{"space in name.png", true},
		{"", true}, // renders as an empty src; opening it fails with a clear error
		{"https://example.com/logo.png", false},
		{"http://example.com/logo.png", false},
		{"HTTP://example.com/logo.png", false},
		{"//cdn.example.com/logo.png", false},
		{"cid:img-abc@mailfire", false}, // already rewritten
		{"data:image/png;base64,iVBORw0K", false},
		{"img:2.png", false},         // looks like a scheme; documented in IsLocalImage
		{"C:\\img\\logo.png", false}, // Windows path, task 18
	} {
		if got := IsLocalImage(tc.dest); got != tc.local {
			t.Errorf("IsLocalImage(%q) = %v, want %v", tc.dest, got, tc.local)
		}
	}
}

func TestLocalImages(t *testing.T) {
	src := `# Doc

![one](a.png) and ![two](https://example.com/b.png)

![three](sub/c.jpg "a title")

![one again](a.png)
![protocol relative](//cdn.example.com/d.gif)
`
	want := []string{"a.png", "sub/c.jpg"}
	got := LocalImages(src)
	if len(got) != len(want) {
		t.Fatalf("LocalImages = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("LocalImages[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLocalImagesNone(t *testing.T) {
	if got := LocalImages("no images here\n\n![r](https://example.com/x.png)\n"); got != nil {
		t.Errorf("LocalImages = %q, want nil", got)
	}
}

func TestHTMLWithImages(t *testing.T) {
	src := `![diagram](assets/diagram.png)

![remote](https://example.com/remote.png "kept title")
`
	replaced := []string{}
	html := HTMLWithImages(src, func(dest string) string {
		replaced = append(replaced, dest)
		return "cid:img-0123abcd@mailfire"
	})
	if len(replaced) != 1 || replaced[0] != "assets/diagram.png" {
		t.Errorf("replace called with %q, want only [assets/diagram.png]", replaced)
	}
	// The cid: reference survives the renderer byte-identically (goldmark
	// keeps ':' and '@' unescaped and does not filter the cid: scheme),
	// and the alt text is preserved.
	if want := `<img src="cid:img-0123abcd@mailfire" alt="diagram">`; !strings.Contains(html, want) {
		t.Errorf("html missing %q:\n%s", want, html)
	}
	if want := `<img src="https://example.com/remote.png" alt="remote" title="kept title">`; !strings.Contains(html, want) {
		t.Errorf("html missing untouched remote image %q:\n%s", want, html)
	}
}

package markdown

import (
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// GitHub Flavored Markdown alerts: a top-level blockquote whose first
// line is exactly [!NOTE], [!TIP], [!IMPORTANT], [!WARNING] or
// [!CAUTION] (case-insensitive) becomes an Alert node. Like on GitHub,
// alerts are recognized only at the top level, never nested in lists or
// other blockquotes, and the marker must be alone on its line.
//
// The HTML is a one-cell presentation table (class "alert alert-<kind>",
// cell "alert-body", title paragraph "alert-title"): Outlook ignores
// borders and backgrounds on <div>, but honors them on <td>. The look is
// left to the template's CSS, which the inliner turns into style
// attributes.

// KindAlert is the node kind of Alert.
var KindAlert = ast.NewNodeKind("Alert")

// Alert is a GFM alert block. Its children are the blockquote's blocks
// with the marker line removed.
type Alert struct {
	ast.BaseBlock
	// AlertKind is the lower-case alert type, e.g. "note".
	AlertKind string
}

// Kind implements ast.Node.
func (n *Alert) Kind() ast.NodeKind { return KindAlert }

// Dump implements ast.Node.
func (n *Alert) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"AlertKind": n.AlertKind}, nil)
}

// Title is the label shown above the alert content, e.g. "Note".
func (n *Alert) Title() string {
	return strings.ToUpper(n.AlertKind[:1]) + n.AlertKind[1:]
}

var reAlertMarker = regexp.MustCompile(`(?i)^\[!(note|tip|important|warning|caution)\]$`)

// alerts is the goldmark extension registering the transformer and the
// HTML renderer.
type alerts struct{}

func (alerts) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(parser.WithASTTransformers(util.Prioritized(alertTransformer{}, 999)))
	m.Renderer().AddOptions(renderer.WithNodeRenderers(util.Prioritized(alertRenderer{}, 999)))
}

type alertTransformer struct{}

// Transform replaces top-level alert blockquotes by Alert nodes.
func (alertTransformer) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	src := reader.Source()
	for c := doc.FirstChild(); c != nil; {
		next := c.NextSibling()
		if bq, ok := c.(*ast.Blockquote); ok {
			if kind := stripMarker(bq, src); kind != "" {
				alert := &Alert{AlertKind: kind}
				for b := bq.FirstChild(); b != nil; {
					nb := b.NextSibling()
					alert.AppendChild(alert, b)
					b = nb
				}
				doc.ReplaceChild(doc, bq, alert)
			}
		}
		c = next
	}
}

// stripMarker checks whether the first line of bq is an alert marker. If
// so, it removes the marker from the first paragraph (the paragraph
// itself when nothing else is left) and returns the lower-case kind;
// otherwise it returns "" and leaves bq untouched.
func stripMarker(bq *ast.Blockquote, src []byte) string {
	para, ok := bq.FirstChild().(*ast.Paragraph)
	if !ok || para.Lines().Len() == 0 {
		return ""
	}
	first := para.Lines().At(0)
	m := reAlertMarker.FindStringSubmatch(strings.TrimSpace(string(first.Value(src))))
	if m == nil {
		return ""
	}
	// The marker line holds only plain text nodes ("[", "!NOTE", "]");
	// drop every inline node that ends within it.
	for c := para.FirstChild(); c != nil; {
		next := c.NextSibling()
		if t, ok := c.(*ast.Text); ok && t.Segment.Stop <= first.Stop {
			para.RemoveChild(para, c)
		}
		c = next
	}
	lines := text.NewSegments()
	for i := 1; i < para.Lines().Len(); i++ {
		lines.Append(para.Lines().At(i))
	}
	para.SetLines(lines)
	if !para.HasChildren() {
		bq.RemoveChild(bq, para)
	}
	return strings.ToLower(m[1])
}

type alertRenderer struct{}

func (alertRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(KindAlert, renderAlert)
}

func renderAlert(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n, ok := node.(*Alert)
	if !ok {
		return ast.WalkContinue, nil // Registered for KindAlert only; unreachable.
	}
	markup := "</td></tr></table>\n"
	if entering {
		markup = `<table role="presentation" class="alert alert-` + n.AlertKind +
			`" width="100%" cellpadding="0" cellspacing="0" border="0"><tr><td class="alert-body">` + "\n" +
			`<p class="alert-title">` + n.Title() + "</p>\n"
	}
	if _, err := w.WriteString(markup); err != nil {
		return ast.WalkStop, err
	}
	return ast.WalkContinue, nil
}

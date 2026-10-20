package render

import (
	"bytes"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"text/template/parse"

	"github.com/thorstenkramm/mailfire/templates"
)

// File renders a user-supplied, self-contained Go text/template.
func File(path string, d Data) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("template file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("template file %s: not a regular file", path)
	}
	source, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("template file: %w", err)
	}
	t, err := template.New(path).Funcs(template.FuncMap{"html": html.EscapeString}).Option("missingkey=error").Parse(string(source))
	if err != nil {
		return "", fmt.Errorf("template file: %w", err)
	}
	if d.Lang == "" {
		d.Lang = "en"
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, d); err != nil {
		return "", fmt.Errorf("template file: %w", err)
	}
	return buf.String(), nil
}

// ReportTemplate is the built-in report content template of
// "mailfire run" (task 33): Markdown, rendered with ReportData, then
// wrapped in a layout template. Export writes it as job-report.md
// alongside the layout templates.
const ReportTemplate = "job-report.md"

// exportFiles lists every file Export writes: the four layout templates
// and the run report template.
func exportFiles() []string {
	files := make([]string, 0, len(Names)+1)
	for _, name := range Names {
		files = append(files, name+".html")
	}
	return append(files, ReportTemplate)
}

// Export writes each built-in as a self-contained editable template.
// Exclusive creation protects previously customized files.
func Export(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	for _, file := range exportFiles() {
		path := filepath.Join(dir, file)
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			if err != nil {
				return err
			}
			return fmt.Errorf("template already exists: %s", path)
		}
	}
	for _, name := range Names {
		lk := looks[name]
		source, err := exportNodes(tmpl.Lookup(lk.layout+".html").Root, view{S: lk})
		if err != nil {
			return err
		}
		if err := writeExport(filepath.Join(dir, name+".html"), source); err != nil {
			return err
		}
	}
	report, err := templates.FS.ReadFile(ReportTemplate)
	if err != nil {
		return err
	}
	return writeExport(filepath.Join(dir, ReportTemplate), string(report))
}

// writeExport writes one exported template, refusing to replace an
// existing file.
func writeExport(path, source string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(source)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// Resolve layout/style nodes, preserving message fields and optional blocks.
// Working on parsed nodes preserves the built-ins' whitespace trimming.
func exportNodes(list *parse.ListNode, d view) (string, error) {
	var out strings.Builder
	for _, node := range list.Nodes {
		switch n := node.(type) {
		case *parse.TextNode:
			out.Write(n.Text)
		case *parse.TemplateNode:
			s, err := exportNodes(tmpl.Lookup(n.Name).Root, d)
			if err != nil {
				return "", err
			}
			out.WriteString(s)
		case *parse.WithNode:
			s, err := exportNodes(n.List, d)
			if err != nil {
				return "", err
			}
			out.WriteString("{{with " + n.Pipe.String() + "}}" + s + "{{end}}")
		case *parse.ActionNode:
			if !strings.Contains(n.String(), ".S.") {
				out.WriteString(n.String())
				continue
			}
			s, err := executeExportNode(node, d)
			if err != nil {
				return "", err
			}
			out.WriteString(s)
		default:
			s, err := executeExportNode(node, d)
			if err != nil {
				return "", err
			}
			out.WriteString(s)
		}
	}
	return out.String(), nil
}

func executeExportNode(node parse.Node, d view) (string, error) {
	root := &parse.ListNode{NodeType: parse.NodeList, Nodes: []parse.Node{node}}
	t, err := template.New("export").AddParseTree("export", &parse.Tree{Name: "export", Root: root})
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	err = t.Execute(&buf, d)
	return buf.String(), err
}

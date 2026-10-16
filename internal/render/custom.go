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

// Export writes each built-in as a self-contained editable template.
// Exclusive creation protects previously customized files.
func Export(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	for _, name := range Names {
		path := filepath.Join(dir, name+".html")
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
		f, err := os.OpenFile(filepath.Join(dir, name+".html"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return err
		}
		_, err = f.WriteString(source)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
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

## {{.Emoji}} {{.Job}} {{.StatusWord}} on {{.Host}}

- **Command:** `{{.JobFull}}`
- **Working directory:** `{{.WorkDir}}`
- **Started:** {{.Start}}
- **Finished:** {{.End}}
- **Duration:** {{.Duration}}
- **Exit code:** {{.ExitCode}}{{if .ExitDetail}} — {{.ExitDetail}}{{end}}
{{- if .StdoutTail}}

**Last {{.Tail}} lines of stdout** ({{.StdoutBytes}} bytes{{if .StdoutOmitted}}, {{.StdoutOmitted}} bytes omitted by the log cap{{end}}):

````text
{{.StdoutTail}}
````
{{- end}}
{{- if .StderrTail}}

**Last {{.Tail}} lines of stderr** ({{.StderrBytes}} bytes{{if .StderrOmitted}}, {{.StderrOmitted}} bytes omitted by the log cap{{end}}):

````text
{{.StderrTail}}
````
{{- end}}
{{- if .Logs}}

**Full logs attached:** {{.Logs}}
{{- end}}

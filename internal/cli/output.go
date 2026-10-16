package cli

import (
	"fmt"
	"io"
	"log"
)

// Output failures are diagnostics, never delivery failures: returning a send
// error after acceptance could cause callers to deliver the same mail twice.
func printOutput(w io.Writer, args ...any) {
	if _, err := fmt.Fprint(w, args...); err != nil {
		log.Printf("mailfire: writing command output: %v", err)
	}
}

func printfOutput(w io.Writer, format string, args ...any) {
	printOutput(w, fmt.Sprintf(format, args...))
}

func printlnOutput(w io.Writer, args ...any) {
	printOutput(w, fmt.Sprintln(args...))
}

func closeReported(c io.Closer, stderr io.Writer) {
	if err := c.Close(); err != nil {
		printfOutput(stderr, "mailfire: closing resource: %v\n", err)
	}
}

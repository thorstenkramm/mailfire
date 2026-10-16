// Package mailbox validates public Internet mailbox syntax without DNS lookups.
package mailbox

import (
	"fmt"
	"net/mail"
	"strings"

	"golang.org/x/net/idna"
)

// Parse accepts net/mail's mailbox syntax, including quoted local parts and
// Unicode, but requires a dotted DNS domain with valid labels.
func Parse(s string) (*mail.Address, error) {
	a, err := mail.ParseAddress(s)
	if err != nil {
		return nil, err
	}
	// ParseAddress unquotes the local part, which may itself contain @.
	domain := a.Address[strings.LastIndex(a.Address, "@")+1:]
	ascii, err := idna.Lookup.ToASCII(domain)
	if err != nil || len(ascii) > 253 || !strings.Contains(ascii, ".") {
		return nil, fmt.Errorf("mailbox domain must be a dotted DNS name")
	}
	for _, label := range strings.Split(ascii, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return nil, fmt.Errorf("invalid mailbox domain label")
		}
		for _, c := range label {
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-':
			default:
				return nil, fmt.Errorf("invalid mailbox domain label")
			}
		}
	}
	return a, nil
}

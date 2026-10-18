package message

import (
	"errors"
	"fmt"
	"testing"
)

type testErr struct {
	kind Kind
	code int
}

func (e *testErr) Error() string  { return "test error" }
func (e *testErr) SendKind() Kind { return e.kind }
func (e *testErr) ReplyCode() int { return e.code }
func (e *testErr) Detail() string { return "detail" }

func TestClassificationThroughWrapping(t *testing.T) {
	for _, k := range []Kind{KindAuth, KindPermanent, KindDeferrable} {
		err := fmt.Errorf("wrapped: %w", &testErr{kind: k, code: 550})
		if IsAuth(err) != (k == KindAuth) || IsPermanent(err) != (k == KindPermanent) || IsDeferrable(err) != (k == KindDeferrable) {
			t.Errorf("%v: helpers disagree", k)
		}
		if serr, ok := As(err); !ok || serr.ReplyCode() != 550 || serr.Detail() != "detail" {
			t.Errorf("%v: As = %v, %v", k, serr, ok)
		}
	}
	plain := errors.New("plain")
	if IsAuth(plain) || IsPermanent(plain) || IsDeferrable(plain) {
		t.Error("an unclassified error must match no kind")
	}
	if serr, ok := As(plain); ok || serr != nil {
		t.Errorf("As matched an unclassified error: %v", serr)
	}
}

func TestKindString(t *testing.T) {
	for k, want := range map[Kind]string{KindAuth: "auth", KindPermanent: "permanent", KindDeferrable: "deferrable", KindUnknown: "unknown"} {
		if k.String() != want {
			t.Errorf("%d.String() = %q, want %q", k, k.String(), want)
		}
	}
}

func TestAPIError(t *testing.T) {
	e := &APIError{Provider: "gmail", Op: "send message", StatusCode: 429, Message: "slow down", Kind: KindDeferrable}
	if e.Error() != "gmail: send message: status 429: slow down" || !IsDeferrable(e) || e.ReplyCode() != 429 || e.Detail() != "slow down" {
		t.Errorf("APIError = %q", e.Error())
	}
	if got := (&APIError{Provider: "gmail", Op: "read credentials", Message: "not logged in", Kind: KindAuth}).Error(); got != "gmail: read credentials: not logged in" {
		t.Errorf("transport form = %q", got)
	}
}

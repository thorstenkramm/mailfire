package deferral

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The default dedup state directory lives inside the default defer_dir;
// the store must neither list it nor evict it as an incomplete write.
func TestReservedDirIgnored(t *testing.T) {
	s := newStore(t)
	state := filepath.Join(s.Dir, ReservedDir)
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, strings.Repeat("a", 64)+".json"), make([]byte, 5000), 0o600); err != nil {
		t.Fatal(err)
	}
	age(t, state)
	s.MaxSize = 1
	if _, err := s.Save([]string{"a@example.com"}, "s", []byte("b"), testContentType); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := s.Save([]string{"a@example.com"}, "s2", []byte("b"), testContentType); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, strings.Repeat("a", 64)+".json")); err != nil {
		t.Fatalf("dedup state evicted by max_size cleanup: %v", err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range list {
		if m.ID == ReservedDir {
			t.Error("List includes the dedup state directory")
		}
	}
	if _, err := s.MessageDir(ReservedDir); err == nil {
		t.Error("MessageDir accepts the reserved directory")
	}
	if _, err := s.Delete([]string{ReservedDir}); err == nil {
		t.Error("Delete accepts the reserved directory")
	}
	if _, err := os.Stat(state); err != nil {
		t.Errorf("dedup state directory removed: %v", err)
	}
}

// SaveMessage records the dedup scope; failed retries keep it.
func TestSaveMessageDedupFields(t *testing.T) {
	s := newStore(t)
	meta, err := s.SaveMessage(Metadata{
		To: []string{"a@example.com"}, Subject: "s", ContentType: testContentType,
		LastError: "boom", DedupScope: strings.Repeat("f", 64), DedupCooldown: 30 * time.Minute,
	}, []byte("body"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordFailure(meta.ID, time.Now(), "again", false); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get(meta.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.DedupScope != meta.DedupScope || got.DedupCooldown != 30*time.Minute || got.Size != 4 || got.LastError != "again" {
		t.Errorf("stored = %+v", got)
	}
}

// Messages without dedup fields (older versions, sends without a key)
// read with an empty scope, and the fields stay absent from meta.json.
func TestDedupFieldsOptional(t *testing.T) {
	s := newStore(t)
	meta, err := s.Save([]string{"a@example.com"}, "s", []byte("b"), testContentType)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, meta.ID, MetaFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "dedup") {
		t.Errorf("meta.json without a key mentions dedup:\n%s", data)
	}
	var m Metadata
	if err := json.Unmarshal(data, &m); err != nil || m.DedupScope != "" || m.DedupCooldown != 0 {
		t.Errorf("decoded = %+v, %v", m, err)
	}
}

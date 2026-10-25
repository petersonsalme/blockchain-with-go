package p2p

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSaveLoadAddrs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peers.json")
	want := []string{"http://a:1", "http://b:2"}
	if err := SaveAddrs(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAddrs(path)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("load = %v, %v", got, err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", st.Mode(), err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftovers: %v", entries)
	}
	// overwrite keeps the mode and replaces content
	if err := SaveAddrs(path, nil); err != nil {
		t.Fatal(err)
	}
	got, err = LoadAddrs(path)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("load = %#v, %v", got, err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "[]\n" && string(raw) != "[]" {
		t.Fatalf("raw = %q", raw)
	}
}

func TestLoadAddrsMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	got, err := LoadAddrs(filepath.Join(dir, "none.json"))
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("load = %#v, %v", got, err)
	}
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte("{not json"), 0o600)
	if _, err := LoadAddrs(bad); err == nil {
		t.Fatal("want error")
	}
	if _, err := LoadAddrs(dir); err == nil {
		t.Fatal("want error reading a directory")
	}
}

func TestSaveAddrsFailureLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := SaveAddrs(filepath.Join(dir, "missing", "peers.json"), []string{"x"}); err == nil {
		t.Fatal("want error")
	}
	// rename onto a directory fails: the temporary file must be removed
	target := filepath.Join(dir, "peers.json")
	_ = os.Mkdir(target, 0o700)
	_ = os.WriteFile(filepath.Join(target, "keep"), nil, 0o600)
	if err := SaveAddrs(target, []string{"x"}); err == nil {
		t.Fatal("want error")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftovers: %v", entries)
	}
}

type stepFile struct {
	fail   string
	closed int
	calls  []string
}

func (f *stepFile) step(name string) error {
	f.calls = append(f.calls, name)
	if f.fail == name {
		return errors.New(name)
	}
	return nil
}
func (f *stepFile) Write(p []byte) (int, error) { return len(p), f.step("write") }
func (f *stepFile) Chmod(os.FileMode) error     { return f.step("chmod") }
func (f *stepFile) Sync() error                 { return f.step("sync") }
func (f *stepFile) Close() error                { f.closed++; return f.step("close") }

func TestWriteAndCloseSteps(t *testing.T) {
	for _, fail := range []string{"write", "chmod", "sync", "close"} {
		f := &stepFile{fail: fail}
		err := writeAndClose(f, []byte("x"))
		if err == nil || err.Error() != fail {
			t.Errorf("fail=%s: err = %v", fail, err)
		}
		if f.closed != 1 {
			t.Errorf("fail=%s: closed %d times", fail, f.closed)
		}
	}
	f := &stepFile{}
	if err := writeAndClose(f, []byte("x")); err != nil || !reflect.DeepEqual(f.calls, []string{"write", "chmod", "sync", "close"}) {
		t.Fatalf("calls = %v, %v", f.calls, err)
	}
}

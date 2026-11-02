package credential

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestVaultIdentityAndIntegrity(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	vault, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	clear(key) // New must not retain the caller's mutable slice.
	sealed, err := vault.Seal("ab", "c", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := vault.Seal("ab", "c", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(sealed, second) {
		t.Fatal("nonce reused")
	}
	plain, err := vault.Open("ab", "c", sealed)
	if err != nil || string(plain) != "secret" {
		t.Fatalf("roundtrip: %v", err)
	}
	for _, identity := range [][2]string{{"a", "bc"}, {"ab", "d"}, {"other", "c"}} {
		if _, err := vault.Open(identity[0], identity[1], sealed); !errors.Is(err, ErrEnvelope) {
			t.Fatal("identity accepted", err)
		}
	}
	for i := range sealed {
		corrupt := bytes.Clone(sealed)
		corrupt[i] ^= 1
		if _, err := vault.Open("ab", "c", corrupt); !errors.Is(err, ErrEnvelope) {
			t.Fatalf("tamper accepted at %d", i)
		}
	}
	for _, size := range []int{0, 1, 31, 33} {
		if _, err := New(make([]byte, size)); !errors.Is(err, ErrKey) {
			t.Fatal("key accepted")
		}
	}
	if _, err := vault.Seal("", "c", nil); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	if _, err := vault.Open("ab", "c", []byte("bad")); !errors.Is(err, ErrEnvelope) {
		t.Fatal(err)
	}
	if _, err := vault.Seal("ab", "c", make([]byte, MaxArchiveBytes+1)); !errors.Is(err, ErrEnvelope) {
		t.Fatal(err)
	}
}

func TestWorkspaceRoundTrip(t *testing.T) {
	source := t.TempDir()
	for name, value := range map[string]string{"home/.dws/profiles.json": "profiles", "keychain/dws-cli/dek": "key", "home/plain": "data"} {
		filename := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(value), 0644); err != nil {
			t.Fatal(err)
		}
	}
	vault, _ := New(make([]byte, 32))
	sealed, err := vault.SealDirectory("u", "c", source)
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := vault.OpenDirectory("u", "c", sealed, target); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(target, "home/.dws/profiles.json")); err != nil || string(data) != "profiles" {
		t.Fatal(err)
	}
	if err := filepath.Walk(target, func(name string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		want := os.FileMode(0600)
		if info.IsDir() {
			want = 0700
		}
		if info.Mode().Perm() != want {
			t.Errorf("permissions %o want %o", info.Mode().Perm(), want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := vault.OpenDirectory("u", "c", sealed, target); !errors.Is(err, ErrWorkspace) {
		t.Fatal("nonempty target accepted", err)
	}
}

func archiveFixture(t *testing.T, headers ...*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	for _, header := range headers {
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := writer.Write(make([]byte, header.Size)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestRejectUnsafeArchiveBeforeWriting(t *testing.T) {
	cases := []*tar.Header{
		{Name: "../outside", Typeflag: tar.TypeReg},
		{Name: "/outside", Typeflag: tar.TypeReg},
		{Name: "C:/outside", Typeflag: tar.TypeReg},
		{Name: "a\\outside", Typeflag: tar.TypeReg},
		{Name: "a/../../outside", Typeflag: tar.TypeReg},
		{Name: "a", Typeflag: tar.TypeSymlink, Linkname: "../outside"},
		{Name: "a", Typeflag: tar.TypeLink, Linkname: "existing"},
		{Name: "a", Typeflag: tar.TypeFifo},
	}
	for i, header := range cases {
		t.Run(header.Name, func(t *testing.T) {
			target := t.TempDir()
			data := archiveFixture(t, &tar.Header{Name: "good", Typeflag: tar.TypeReg, Size: 1}, header)
			if err := Unpack(data, target); !errors.Is(err, ErrArchive) {
				t.Fatalf("case %d accepted: %v", i, err)
			}
			children, err := os.ReadDir(target)
			if err != nil || len(children) != 0 {
				t.Fatal("partial archive extracted")
			}
		})
	}
	for _, headers := range [][]*tar.Header{
		{{Name: "same", Typeflag: tar.TypeReg}, {Name: "same", Typeflag: tar.TypeReg}},
		{{Name: "parent", Typeflag: tar.TypeReg}, {Name: "parent/child", Typeflag: tar.TypeReg}},
		{{Name: "parent/child", Typeflag: tar.TypeReg}, {Name: "parent", Typeflag: tar.TypeReg}},
	} {
		if err := Unpack(archiveFixture(t, headers...), t.TempDir()); !errors.Is(err, ErrArchive) {
			t.Fatal("conflict accepted", err)
		}
	}
}

func TestArchiveLimits(t *testing.T) {
	data := archiveFixture(t, &tar.Header{Name: "large", Typeflag: tar.TypeReg, Size: MaxBytes + 1})
	if err := Unpack(data, t.TempDir()); !errors.Is(err, ErrArchive) {
		t.Fatal("oversized contents accepted", err)
	}
	// Combined size is bounded even when individual files fit.
	data = archiveFixture(t, &tar.Header{Name: "a", Typeflag: tar.TypeReg, Size: MaxBytes}, &tar.Header{Name: "b", Typeflag: tar.TypeReg, Size: 1})
	if err := Unpack(data, t.TempDir()); !errors.Is(err, ErrArchive) {
		t.Fatal("combined size accepted", err)
	}
	headers := make([]*tar.Header, MaxEntries+1)
	for i := range headers {
		headers[i] = &tar.Header{Name: filepath.ToSlash(filepath.Join("entries", string(rune(0x1000+i)))), Typeflag: tar.TypeReg}
	}
	if err := Unpack(archiveFixture(t, headers...), t.TempDir()); !errors.Is(err, ErrArchive) {
		t.Fatal("entry limit accepted", err)
	}
	// Implicit parent directories count toward the filesystem entry budget.
	implicit := make([]*tar.Header, MaxEntries/2+1)
	for i := range implicit {
		implicit[i] = &tar.Header{Name: "parents/" + string(rune(0x2000+i)) + "/child", Typeflag: tar.TypeReg}
	}
	if err := Unpack(archiveFixture(t, implicit...), t.TempDir()); !errors.Is(err, ErrArchive) {
		t.Fatal("implicit entry limit accepted", err)
	}
	if err := Unpack(make([]byte, MaxArchiveBytes+1), t.TempDir()); !errors.Is(err, ErrArchive) {
		t.Fatal("archive bound accepted", err)
	}
	valid := archiveFixture(t, &tar.Header{Name: "a", Typeflag: tar.TypeReg, Size: 100})
	if err := Unpack(valid[:520], t.TempDir()); !errors.Is(err, ErrArchive) {
		t.Fatal("truncation accepted", err)
	}
}

func TestPackRejectsLinksAndOversize(t *testing.T) {
	source := t.TempDir()
	file := filepath.Join(source, "file")
	if err := os.WriteFile(file, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(source, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Pack(source); !errors.Is(err, ErrArchive) {
		t.Fatal("symlink accepted", err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Pack(source); !errors.Is(err, ErrArchive) {
		t.Fatal("hardlink accepted", err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	handle, err := os.OpenFile(file, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Truncate(MaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	handle.Close()
	if _, err := Pack(source); !errors.Is(err, ErrArchive) {
		t.Fatal("oversized source accepted", err)
	}
}

func TestUnpackDestinationBoundary(t *testing.T) {
	archive := archiveFixture(t, &tar.Header{Name: "safe/file", Typeflag: tar.TypeReg, Size: 1})
	parent := t.TempDir()
	actual := t.TempDir()
	link := filepath.Join(parent, "alias")
	if err := os.Symlink(actual, link); err != nil {
		t.Fatal(err)
	}
	if err := Unpack(archive, link); !errors.Is(err, ErrWorkspace) {
		t.Fatal("symlink target accepted", err)
	}
	if err := Unpack(archive, filepath.Join(parent, "missing")); !errors.Is(err, ErrWorkspace) {
		t.Fatal("missing target accepted", err)
	}
	if err := os.WriteFile(filepath.Join(actual, "existing"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Unpack(archive, actual); !errors.Is(err, ErrWorkspace) {
		t.Fatal("nonempty target accepted", err)
	}
}

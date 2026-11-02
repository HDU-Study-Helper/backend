package credential

import (
	"archive/tar"
	"bytes"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
)

// safeName also rejects Windows paths when running on Unix.
func safeName(name string) bool {
	return name != "" && name != "." && len(name) <= 4096 && !strings.ContainsAny(name, "\\:\x00") && !strings.HasPrefix(name, "/") && path.Clean(name) == name && name != ".." && !strings.HasPrefix(name, "../")
}

func singleLink(info fs.FileInfo) bool {
	// FileInfo.Sys differs across platforms. Fail closed when link counts cannot
	// be checked rather than importing an offline credential through a hardlink.
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return false
	}
	count := value.FieldByName("Nlink")
	return count.IsValid() && count.CanUint() && count.Uint() == 1
}

// Pack returns an uncompressed tar in memory. The caller must exclusively own
// the source directory until Pack returns; it must not be modified concurrently.
// No file metadata, ownership, or original access permissions are archived.
func Pack(root string) ([]byte, error) {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrWorkspace
	}
	tree, err := os.OpenRoot(root)
	if err != nil {
		return nil, ErrWorkspace
	}
	defer tree.Close()
	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	var total int64
	entries := 0
	err = fs.WalkDir(tree.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return ErrWorkspace
		}
		if name == "." {
			return nil
		}
		entries++
		if entries > MaxEntries || !safeName(name) {
			return ErrArchive
		}
		info, err := tree.Lstat(name)
		if err != nil {
			return ErrWorkspace
		}
		header := &tar.Header{Name: name, Mode: 0700, Typeflag: tar.TypeDir}
		switch {
		case info.IsDir():
		case info.Mode().IsRegular():
			if !singleLink(info) || info.Size() < 0 || info.Size() > MaxBytes-total {
				return ErrArchive
			}
			total += info.Size()
			header.Mode = 0600
			header.Typeflag = tar.TypeReg
			header.Size = info.Size()
		default:
			return ErrArchive
		}
		if err := writer.WriteHeader(header); err != nil {
			return ErrArchive
		}
		if info.Mode().IsRegular() {
			file, err := tree.Open(name)
			if err != nil {
				return ErrWorkspace
			}
			actual, statErr := file.Stat()
			if statErr != nil || !actual.Mode().IsRegular() || !singleLink(actual) || !os.SameFile(info, actual) || actual.Size() != info.Size() {
				file.Close()
				return ErrArchive
			}
			_, copyErr := io.CopyN(writer, file, info.Size())
			var extra [1]byte
			n, readErr := file.Read(extra[:])
			closeErr := file.Close()
			if copyErr != nil || n != 0 || readErr != io.EOF || closeErr != nil {
				return ErrWorkspace
			}
		}
		if buf.Len() > MaxArchiveBytes {
			return ErrArchive
		}
		return nil
	})
	if err != nil {
		clear(buf.Bytes())
		return nil, err
	}
	if writer.Close() != nil || buf.Len() > MaxArchiveBytes {
		clear(buf.Bytes())
		return nil, ErrArchive
	}
	return buf.Bytes(), nil
}

type archiveEntry struct {
	name      string
	directory bool
	data      []byte
}

func validateArchive(data []byte) ([]archiveEntry, error) {
	if len(data) > MaxArchiveBytes {
		return nil, ErrArchive
	}
	reader := tar.NewReader(bytes.NewReader(data))
	entries := make([]archiveEntry, 0)
	seen := map[string]bool{}
	materialized := map[string]bool{}
	valid := false
	defer func() {
		if !valid {
			for _, entry := range entries {
				clear(entry.data)
			}
		}
	}()
	var total int64
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, ErrArchive
		}
		name := header.Name
		if header.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
		}
		_, duplicate := seen[name]
		if len(entries) >= MaxEntries || !safeName(name) || duplicate || header.Linkname != "" || header.Size < 0 || header.Size > MaxBytes-total {
			return nil, ErrArchive
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir {
			return nil, ErrArchive
		}
		if header.Typeflag == tar.TypeDir && header.Size != 0 {
			return nil, ErrArchive
		}
		// A file cannot be a parent directory, in either archive order.
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if directory, ok := seen[parent]; ok && !directory {
				return nil, ErrArchive
			}
		}
		if header.Typeflag == tar.TypeReg {
			for existing := range seen {
				if strings.HasPrefix(existing, name+"/") {
					return nil, ErrArchive
				}
			}
		}
		materialized[name] = true
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			materialized[parent] = true
		}
		if len(materialized) > MaxEntries {
			return nil, ErrArchive
		}
		entry := archiveEntry{name: name, directory: header.Typeflag == tar.TypeDir}
		if !entry.directory {
			total += header.Size
			entry.data = make([]byte, int(header.Size))
			if _, err := io.ReadFull(reader, entry.data); err != nil {
				clear(entry.data)
				return nil, ErrArchive
			}
		}
		seen[name] = entry.directory
		entries = append(entries, entry)
	}
	valid = true
	return entries, nil
}

// Unpack accepts only an existing empty real directory, normally created by
// os.MkdirTemp on a private tmpfs. Callers must prevent concurrent access to it.
// Archive validation precedes extraction. Failure removes all created entries.
func Unpack(data []byte, destination string) error {
	entries, err := validateArchive(data)
	if err != nil {
		return err
	}
	defer func() {
		for _, entry := range entries {
			clear(entry.data)
		}
	}()
	info, err := os.Lstat(destination)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrWorkspace
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return ErrWorkspace
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return ErrWorkspace
	}
	children, readErr := dir.Readdirnames(1)
	if readErr != io.EOF || len(children) != 0 {
		dir.Close()
		return ErrWorkspace
	}
	if err := dir.Chmod(0700); err != nil {
		dir.Close()
		return ErrWorkspace
	}
	dir.Close()
	created := []string{}
	success := false
	defer func() {
		if !success {
			for i := len(created) - 1; i >= 0; i-- {
				_ = root.Remove(created[i])
			}
		}
	}()
	var mkdir func(string) error
	mkdir = func(name string) error {
		if name == "." {
			return nil
		}
		if info, err := root.Lstat(name); err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return ErrWorkspace
			}
			return nil
		} else if !os.IsNotExist(err) {
			return ErrWorkspace
		}
		if err := mkdir(path.Dir(name)); err != nil {
			return err
		}
		if err := root.Mkdir(name, 0700); err != nil {
			return ErrWorkspace
		}
		created = append(created, name)
		return nil
	}
	for _, entry := range entries {
		if entry.directory {
			if err := mkdir(entry.name); err != nil {
				return err
			}
			continue
		}
		if err := mkdir(path.Dir(entry.name)); err != nil {
			return err
		}
		file, err := root.OpenFile(filepath.FromSlash(entry.name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return ErrWorkspace
		}
		created = append(created, entry.name)
		_, writeErr := file.Write(entry.data)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			return ErrWorkspace
		}
	}
	success = true
	return nil
}

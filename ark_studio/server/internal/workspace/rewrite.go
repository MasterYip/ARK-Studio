package workspace

import (
	"bytes"
	"os"

	"arkstudio/go/internal/project"
)

// TextFile is one project text file read for Search › Replace.
type TextFile struct {
	Target, Rel string
	Text        string // without a UTF-8 BOM
	BOM         bool
	Hash        string // hash of the bytes on disk (the editor's base_hash)
}

// ReadText reads a project file as UTF-8 text with the Editor's rules: inside
// the project, no symlinks or ignored paths, at most MaxFileBytes, no binary.
func ReadText(root, path string) (*TextFile, error) {
	target, rel, err := resolve(project.Resolve(root), path, true)
	if err != nil {
		return nil, err
	}
	if isDir(target) {
		return nil, werr(400, "path is a folder, not a file")
	}
	fi, err := os.Stat(target)
	if err != nil {
		return nil, werr(400, "cannot read file: %s", osMessage(err))
	}
	if fi.Size() > MaxFileBytes {
		return nil, werr(413, "file is too large to edit (%d bytes; limit %d)", fi.Size(), MaxFileBytes)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		return nil, werr(400, "cannot read file: %s", osMessage(err))
	}
	if looksBinary(raw) {
		return nil, werr(415, "binary file; the editor only opens UTF-8 text")
	}
	text, ok := decodeUTF8Sig(raw)
	if !ok {
		return nil, werr(415, "file is not UTF-8 text")
	}
	return &TextFile{Target: target, Rel: rel, Text: text, BOM: bytes.HasPrefix(raw, []byte("\xef\xbb\xbf")), Hash: hashBytes(raw)}, nil
}

// WriteText replaces f's content when the file still has f.Hash on disk
// (409 otherwise) and returns the new hash.  A BOM the file had is kept.
func WriteText(f *TextFile, text string) (string, error) {
	if readOnly(f.Target) {
		return "", werr(403, "the file is read-only")
	}
	current, err := os.ReadFile(f.Target)
	if err != nil {
		return "", werr(400, "cannot read file: %s", osMessage(err))
	}
	if !compareDigest(f.Hash, hashBytes(current)) {
		return "", werr(409, "the file changed on disk since the preview; search again")
	}
	data := []byte(text)
	if f.BOM {
		data = append([]byte("\xef\xbb\xbf"), data...)
	}
	if len(data) > MaxFileBytes {
		return "", werr(413, "file is too large to save (%d bytes; limit %d)", len(data), MaxFileBytes)
	}
	if err := atomicWrite(f.Target, data); err != nil {
		return "", err
	}
	return hashBytes(data), nil
}

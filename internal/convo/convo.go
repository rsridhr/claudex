package convo

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"unicode/utf16"

	"github.com/tanq16/claudex/internal/model"
)

// RawHistoryEntry preserves the original JSONL line for lossless write-back.
type RawHistoryEntry struct {
	Parsed model.HistoryEntry
	Raw    []byte
}

// projectPathLimit is Claude Code's cap on an encoded project directory name.
// Longer names are truncated and given a hash suffix so they stay unique.
const projectPathLimit = 200

// EncodeProjectPath reproduces Claude Code's project directory name for a working
// directory. The reference implementation is:
//
//	function KP(q){ let K = q.replace(/[^a-zA-Z0-9]/g,"-")
//	                if (K.length <= 200) return K
//	                return `${K.slice(0,200)}-${Math.abs(hash(q)).toString(36)}` }
//	function hash(e){ let t=0; for(let r=0;r<e.length;r++) t=(t<<5)-t+e.charCodeAt(r)|0; return t }
//
// Two details matter. The regex carries no /u flag, so it substitutes per UTF-16
// code unit: an astral-plane rune is two units and becomes two hyphens. And the
// hash is taken over the ORIGINAL path, not the encoded one.
func EncodeProjectPath(absPath string) string {
	units := utf16.Encode([]rune(absPath))
	encoded := make([]byte, len(units))
	for i, u := range units {
		switch {
		case u >= 'a' && u <= 'z', u >= 'A' && u <= 'Z', u >= '0' && u <= '9':
			encoded[i] = byte(u)
		default:
			encoded[i] = '-'
		}
	}
	if len(encoded) <= projectPathLimit {
		return string(encoded)
	}
	return string(encoded[:projectPathLimit]) + "-" + strconv.FormatInt(absPathHash(units), 36)
}

// absPathHash is Java-style string hashing over UTF-16 code units with int32
// wraparound, returned as a non-negative value. int64 is used for the result so
// that negating math.MinInt32 does not wrap back to itself, matching Math.abs.
func absPathHash(units []uint16) int64 {
	var h int32
	for _, u := range units {
		h = h*31 + int32(u)
	}
	if h < 0 {
		return -int64(h)
	}
	return int64(h)
}

func ProjectDir(configDir, projectPath string) string {
	return filepath.Join(configDir, "projects", EncodeProjectPath(projectPath))
}

func ReadRawHistory(configDir string) ([]RawHistoryEntry, error) {
	path := filepath.Join(configDir, "history.jsonl")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []RawHistoryEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		raw := make([]byte, len(scanner.Bytes()))
		copy(raw, scanner.Bytes())

		var entry model.HistoryEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			continue
		}
		entries = append(entries, RawHistoryEntry{Parsed: entry, Raw: raw})
	}

	return entries, scanner.Err()
}

// WriteRawHistory atomically overwrites history.jsonl.
func WriteRawHistory(configDir string, entries []RawHistoryEntry) error {
	path := filepath.Join(configDir, "history.jsonl")
	tmp := path + ".tmp"

	f, err := os.Create(tmp)
	if err != nil {
		return err
	}

	w := bufio.NewWriter(f)
	for _, e := range entries {
		w.Write(e.Raw)
		w.WriteByte('\n')
	}

	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}

	return os.Rename(tmp, path)
}

func AppendRawHistory(configDir string, entries []RawHistoryEntry) error {
	path := filepath.Join(configDir, "history.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	for _, e := range entries {
		w.Write(e.Raw)
		w.WriteByte('\n')
	}
	return w.Flush()
}

func FilterBySession(entries []RawHistoryEntry, sessionID string) (matching, rest []RawHistoryEntry) {
	for _, e := range entries {
		if e.Parsed.SessionID == sessionID {
			matching = append(matching, e)
		} else {
			rest = append(rest, e)
		}
	}
	return
}

func MoveSession(sessionID, srcProjectDir, dstProjectDir string) error {
	if err := os.MkdirAll(dstProjectDir, 0700); err != nil {
		return fmt.Errorf("creating target directory: %w", err)
	}

	jsonlName := sessionID + ".jsonl"
	srcJSONL := filepath.Join(srcProjectDir, jsonlName)
	dstJSONL := filepath.Join(dstProjectDir, jsonlName)

	if _, err := os.Stat(dstJSONL); err == nil {
		return fmt.Errorf("session %s already exists in target directory", sessionID)
	}

	if _, err := os.Stat(srcJSONL); err == nil {
		if err := moveFileOrDir(srcJSONL, dstJSONL); err != nil {
			return fmt.Errorf("moving session JSONL: %w", err)
		}
	}

	srcSubDir := filepath.Join(srcProjectDir, sessionID)
	dstSubDir := filepath.Join(dstProjectDir, sessionID)
	if info, err := os.Stat(srcSubDir); err == nil && info.IsDir() {
		if err := moveFileOrDir(srcSubDir, dstSubDir); err != nil {
			return fmt.Errorf("moving sub-agent directory: %w", err)
		}
	}

	return nil
}

// moveFileOrDir tries os.Rename first, falls back to copy+remove for cross-filesystem.
func moveFileOrDir(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}

	if linkErr, ok := err.(*os.LinkError); ok && errors.Is(linkErr.Err, syscall.EXDEV) {
		return copyAndRemove(src, dst)
	}
	return err
}

func copyAndRemove(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}

	if info.IsDir() {
		return copyDirAndRemove(src, dst)
	}

	return copyFileAndRemove(src, dst, info.Mode())
}

func copyFileAndRemove(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	in.Close()
	return os.Remove(src)
}

func copyDirAndRemove(src, dst string) error {
	if err := os.MkdirAll(dst, 0700); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())

		info, err := e.Info()
		if err != nil {
			return err
		}

		if e.IsDir() {
			if err := copyDirAndRemove(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFileAndRemove(srcPath, dstPath, info.Mode()); err != nil {
				return err
			}
		}
	}

	return os.Remove(src)
}

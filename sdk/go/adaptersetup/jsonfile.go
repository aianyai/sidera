package adaptersetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Errors of EditJSONFile, wrapped with the path and the words for a person.
var (
	// ErrInvalidJSON: the file is not strict JSON with an object at its root, so
	// the file was not touched. A client that reads strictly ignores such a file as
	// a whole, which is why it is refused and not repaired.
	ErrInvalidJSON = errors.New("the file is not a valid JSON object")
	// ErrChangedDuringEdit: the file changed between the read and the rename, so
	// the edit was dropped and the file is as the other writer left it.
	ErrChangedDuringEdit = errors.New("the file changed while it was being edited")
	// ErrUnexpectedType: a key on the way to the one an edit needs holds a value of
	// another type (an array where an object was expected), and was not overwritten.
	ErrUnexpectedType = errors.New("a value in the file has another type than expected")
)

// maxLinks bounds the symbolic links followed from one path.
const maxLinks = 32

// EditJSONFile edits the strict-JSON file at path with edit, as conservatively as
// an edit of a person's client configuration has to be:
//
//   - A symbolic link is followed, relative or dangling ones included, and the
//     file it leads to is written, so a settings file that is a link into a
//     dotfiles directory stays a link.
//   - The file is parsed strictly: valid JSON (no comment, no trailing comma, no
//     byte-order mark), valid UTF-8 with no unpaired surrogate escape (they would be
//     rewritten as U+FFFD), one object at the root, no repeated key. Anything else is
//     refused with ErrInvalidJSON before edit runs, and the file is not touched.
//   - Members keep their order and numbers their spelling; the indentation and line
//     ending of the file are kept.
//   - The file keeps its mode. A file that does not exist is made, mode 0600 and
//     with its directory if the path was not a link.
//   - The content is read again just before the rename and compared with what was
//     edited; if it differs the edit is dropped (ErrChangedDuringEdit). The window
//     between that check and the rename remains, and is the smallest this can make
//     it.
//
// The file is replaced by a rename, so a hard link to it is not kept (the link keeps
// the old content) and neither is the file's owner when this program runs as another
// user; only the mode is.
//
// edit changes root and reports whether it did; when it reports no change nothing
// is written. The result says whether the file was written.
func EditJSONFile(path string, edit func(root *Object) (changed bool, err error)) (written bool, err error) {
	f, err := loadJSONFile(path)
	if err != nil {
		return false, err
	}
	changed, err := edit(f.root)
	if err != nil || !changed {
		return false, err
	}
	data := encodeObject(f.root, f.original)

	dir := filepath.Dir(f.target)
	if !f.existed {
		if f.viaLink {
			if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
				return false, fmt.Errorf("%s is a link to %s, whose directory does not exist", path, f.target)
			}
		} else if err := os.MkdirAll(dir, 0o700); err != nil {
			return false, err
		}
	}
	unchanged := func() error {
		now, _, nowExisted, err := readTarget(f.target)
		if err != nil {
			return err
		}
		if nowExisted != f.existed || !bytes.Equal(now, f.original) {
			return fmt.Errorf("%w: %s; nothing was written", ErrChangedDuringEdit, f.target)
		}
		return nil
	}
	if err := replaceFile(f.target, data, f.mode, unchanged); err != nil {
		return false, err
	}
	return true, nil
}

// ReadJSONFile reads the file at path the way EditJSONFile does: links followed, the
// content parsed strictly (ErrInvalidJSON otherwise). A file that is not there is an
// empty object and exists is false. Nothing is written.
func ReadJSONFile(path string) (root *Object, exists bool, err error) {
	f, err := loadJSONFile(path)
	if err != nil {
		return nil, false, err
	}
	return f.root, f.existed, nil
}

// jsonFile is a file as EditJSONFile and ReadJSONFile find it.
type jsonFile struct {
	target   string // the file the path leads to
	viaLink  bool
	original []byte
	mode     os.FileMode
	existed  bool
	root     *Object
}

func loadJSONFile(path string) (*jsonFile, error) {
	target, viaLink, err := followLinks(path)
	if err != nil {
		return nil, err
	}
	original, mode, existed, err := readTarget(target)
	if err != nil {
		return nil, err
	}
	f := &jsonFile{target: target, viaLink: viaLink, original: original, mode: mode, existed: existed, root: NewObject()}
	if existed {
		if f.root, err = parseObject(original); err != nil {
			return nil, fmt.Errorf("%w: %s: %v; nothing was changed, fix or remove the file and run the command again", ErrInvalidJSON, target, err)
		}
	}
	return f, nil
}

// followLinks returns the file that path leads to: path itself when it is not a
// link, else the target of the link, followed to its end. A link to something that
// is not there leads to where it would be.
func followLinks(path string) (target string, viaLink bool, err error) {
	target = path
	for range maxLinks {
		info, err := os.Lstat(target)
		if errors.Is(err, os.ErrNotExist) {
			return target, viaLink, nil
		}
		if err != nil {
			return "", false, err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return target, viaLink, nil
		}
		link, err := os.Readlink(target)
		if err != nil {
			return "", false, err
		}
		if !filepath.IsAbs(link) {
			// The kernel resolves a relative link from the directory the link is really
			// in. The directory of the path it was reached by may itself be reached
			// through a link, and then joining the two would lead somewhere else.
			dir, err := filepath.EvalSymlinks(filepath.Dir(target))
			if err != nil {
				return "", false, err
			}
			link = filepath.Join(dir, link)
		}
		target, viaLink = link, true
	}
	return "", false, fmt.Errorf("%s leads through more than %d links", path, maxLinks)
}

// readTarget reads the file, and the mode a rewrite gives it: the file's own, or
// 0600 for one that does not exist.
func readTarget(target string) (data []byte, mode os.FileMode, existed bool, err error) {
	info, err := os.Stat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0o600, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, false, fmt.Errorf("%s is not a regular file", target)
	}
	data, err = os.ReadFile(target)
	if err != nil {
		return nil, 0, false, err
	}
	return data, info.Mode().Perm(), true, nil
}

// Object is a JSON object that keeps the order of its members. The values in it
// are *Object, []any, string, json.Number (a number as it was spelled), bool and nil.
type Object struct {
	keys []string
	vals map[string]any
}

// NewObject returns an empty object.
func NewObject() *Object { return &Object{vals: map[string]any{}} }

// Get returns the value of the key.
func (o *Object) Get(key string) (any, bool) {
	v, ok := o.vals[key]
	return v, ok
}

// Set sets the key, keeping its place if it is there and adding it last if not.
func (o *Object) Set(key string, value any) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = value
}

// Keys returns the keys in order.
func (o *Object) Keys() []string { return append([]string(nil), o.keys...) }

// ObjectAt returns the object at the key path below o, making it, and the objects
// above it, when they are not there. A key on the way that holds anything else is an
// ErrUnexpectedType and nothing is changed.
func (o *Object) ObjectAt(path ...string) (*Object, error) {
	cur := o
	for i, key := range path {
		v, ok := cur.vals[key]
		if !ok {
			child := NewObject()
			cur.Set(key, child)
			cur = child
			continue
		}
		child, isObject := v.(*Object)
		if !isObject {
			return nil, unexpected(path[:i+1], v, "an object")
		}
		cur = child
	}
	return cur, nil
}

// HasString says whether the array at the key path below o holds the string. A path
// that is not there holds nothing; a key on the way that holds the wrong type is an
// ErrUnexpectedType.
func (o *Object) HasString(path []string, s string) (bool, error) {
	_, _, arr, _, err := o.stringArray(path, false)
	return indexOfString(arr, s) >= 0, err
}

// AddString adds the string to the array at the key path below o unless it is in it,
// making the array and the objects above it when they are not there, and says whether
// it added it. A key on the way that holds the wrong type is an ErrUnexpectedType and
// nothing is changed.
func (o *Object) AddString(path []string, s string) (added bool, err error) {
	parent, key, arr, found, err := o.stringArray(path, true)
	if err != nil {
		return false, err
	}
	if found && indexOfString(arr, s) >= 0 {
		return false, nil
	}
	parent.Set(key, append(arr, s))
	return true, nil
}

// RemoveString removes every occurrence of the string from the array at the key path
// below o and says whether it removed one. The array stays, empty if it was the last;
// a path that is not there is not made.
func (o *Object) RemoveString(path []string, s string) (removed bool, err error) {
	parent, key, arr, found, err := o.stringArray(path, false)
	if err != nil || !found {
		return false, err
	}
	kept := make([]any, 0, len(arr))
	for _, e := range arr {
		if e != s {
			kept = append(kept, e)
		}
	}
	if len(kept) == len(arr) {
		return false, nil
	}
	parent.Set(key, kept)
	return true, nil
}

// stringArray finds the array at the path: the objects on the way, then the array
// under the last key. With create, objects that are not there are made on the way (the
// array itself is not: found says whether it exists). Nothing is made after a key
// that holds the wrong type is found, since a key that is missing has nothing below it.
func (o *Object) stringArray(path []string, create bool) (parent *Object, key string, arr []any, found bool, err error) {
	if len(path) == 0 {
		return nil, "", nil, false, errors.New("adaptersetup: an empty key path")
	}
	parent = o
	for i, k := range path[:len(path)-1] {
		v, ok := parent.vals[k]
		if !ok {
			if !create {
				return nil, "", nil, false, nil
			}
			child := NewObject()
			parent.Set(k, child)
			parent = child
			continue
		}
		child, isObject := v.(*Object)
		if !isObject {
			return nil, "", nil, false, unexpected(path[:i+1], v, "an object")
		}
		parent = child
	}
	key = path[len(path)-1]
	v, ok := parent.vals[key]
	if !ok {
		return parent, key, nil, false, nil
	}
	arr, isArray := v.([]any)
	if !isArray {
		return nil, "", nil, false, unexpected(path, v, "an array")
	}
	return parent, key, arr, true, nil
}

func indexOfString(arr []any, s string) int {
	for i, e := range arr {
		if e == s {
			return i
		}
	}
	return -1
}

func unexpected(path []string, v any, want string) error {
	return fmt.Errorf("%w: %s is %s, not %s", ErrUnexpectedType, strings.Join(path, "."), describe(v), want)
}

func describe(v any) string {
	switch v.(type) {
	case *Object:
		return "an object"
	case []any:
		return "an array"
	case string:
		return "a string"
	case json.Number:
		return "a number"
	case bool:
		return "a boolean"
	}
	return "null"
}

// parseObject parses data as one JSON object under the rules of EditJSONFile.
func parseObject(data []byte) (*Object, error) {
	if !json.Valid(data) {
		var probe any
		err := json.Unmarshal(data, &probe)
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) {
			return nil, fmt.Errorf("invalid JSON at byte %d: %v (comments, trailing commas and a byte-order mark are not JSON)", syntax.Offset, syntax)
		}
		return nil, fmt.Errorf("invalid JSON: %v", err)
	}
	if !utf8.Valid(data) {
		return nil, errors.New("the file is not valid UTF-8, and a rewrite would replace the bytes that are not with U+FFFD")
	}
	if at := unpairedSurrogate(data); at >= 0 {
		return nil, fmt.Errorf("the \\u escape at byte %d is half of a surrogate pair without the other half, and a rewrite would replace it with U+FFFD", at)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	obj, ok := v.(*Object)
	if !ok {
		return nil, errors.New("the root is not an object")
	}
	return obj, nil
}

// unpairedSurrogate returns the offset of the first \uD800 to \uDFFF escape in data
// that is not one half of a pair, or -1. data is valid JSON, so every escape is
// complete.
func unpairedSurrogate(data []byte) int {
	inString := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case !inString:
			inString = c == '"'
		case c == '"':
			inString = false
		case c == '\\' && data[i+1] != 'u':
			i++
		case c == '\\':
			r := hex4(data[i+2 : i+6])
			switch {
			case r >= 0xDC00 && r <= 0xDFFF:
				return i
			case r >= 0xD800 && r <= 0xDBFF:
				if i+12 > len(data) || data[i+6] != '\\' || data[i+7] != 'u' {
					return i
				}
				if low := hex4(data[i+8 : i+12]); low < 0xDC00 || low > 0xDFFF {
					return i
				}
				i += 11
			default:
				i += 5
			}
		}
	}
	return -1
}

func hex4(b []byte) uint64 {
	n, _ := strconv.ParseUint(string(b), 16, 32)
	return n
}

func parseValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	d, isDelim := tok.(json.Delim)
	if !isDelim {
		return tok, nil // string, json.Number, bool or nil
	}
	if d == '[' {
		arr := []any{}
		for dec.More() {
			v, err := parseValue(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		_, err := dec.Token() // ]
		return arr, err
	}
	obj := NewObject()
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key := keyTok.(string)
		if _, dup := obj.vals[key]; dup {
			return nil, fmt.Errorf("the key %q is repeated, and readers disagree on which value wins", key)
		}
		v, err := parseValue(dec)
		if err != nil {
			return nil, err
		}
		obj.Set(key, v)
	}
	_, err = dec.Token() // }
	return obj, err
}

// encodeObject writes root in the style of the file it was read from: the same
// indentation unit (compact if the file had its members on one line), line ending and
// final newline.
func encodeObject(root *Object, original []byte) []byte {
	eol := "\n"
	if bytes.Contains(original, []byte("\r\n")) {
		eol = "\r\n"
	}
	var b bytes.Buffer
	writeValue(&b, root, indentOf(original), eol, 0)
	if len(original) == 0 || bytes.HasSuffix(original, []byte("\n")) {
		b.WriteString(eol)
	}
	return b.Bytes()
}

// indentOf is the indentation unit of the file: the whitespace in front of the first
// member of the root object that starts a line, two spaces when that cannot be told, or
// "" (compact) when the object has members and none of its lines breaks. A file with no
// member to learn it from, an empty object or none at all, is laid out with two spaces.
func indentOf(data []byte) string {
	unit, depth, inString := "", 0, false
	members, multiline := false, false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			if c == '\\' {
				i++
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
			members = members || depth == 1
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case '\n':
			if depth < 1 { // after the object: the final newline
				continue
			}
			multiline = true
			if depth == 1 && unit == "" {
				unit = memberIndent(data[i+1:])
			}
		}
	}
	switch {
	case members && !multiline:
		return ""
	case unit == "":
		return "  "
	}
	return unit
}

// memberIndent is the whitespace at the start of the line in front of a key, or "".
func memberIndent(line []byte) string {
	n := 0
	for n < len(line) && (line[n] == ' ' || line[n] == '\t') {
		n++
	}
	if n < len(line) && line[n] == '"' {
		return string(line[:n])
	}
	return ""
}

func writeValue(b *bytes.Buffer, v any, unit, eol string, depth int) {
	newline := func(depth int) {
		if unit != "" {
			b.WriteString(eol)
			b.WriteString(strings.Repeat(unit, depth))
		}
	}
	switch v := v.(type) {
	case *Object:
		if len(v.keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteByte('{')
		for i, k := range v.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			newline(depth + 1)
			writeString(b, k)
			b.WriteByte(':')
			if unit != "" {
				b.WriteByte(' ')
			}
			writeValue(b, v.vals[k], unit, eol, depth+1)
		}
		newline(depth)
		b.WriteByte('}')
	case []any:
		if len(v) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i, e := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			newline(depth + 1)
			writeValue(b, e, unit, eol, depth+1)
		}
		newline(depth)
		b.WriteByte(']')
	case string:
		writeString(b, v)
	case json.Number:
		b.WriteString(v.String())
	case bool:
		fmt.Fprint(b, v)
	case nil:
		b.WriteString("null")
	default:
		panic(fmt.Sprintf("adaptersetup: a JSON value cannot be a %T", v))
	}
}

func writeString(b *bytes.Buffer, s string) {
	enc := json.NewEncoder(&nopNewline{b})
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
}

// nopNewline drops the newline json.Encoder ends a value with.
type nopNewline struct{ w *bytes.Buffer }

func (n *nopNewline) Write(p []byte) (int, error) {
	n.w.Write(bytes.TrimSuffix(p, []byte("\n")))
	return len(p), nil
}

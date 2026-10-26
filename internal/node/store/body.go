package store

import (
	"fmt"
	"path"
	"strings"

	"github.com/aianyai/sidera/internal/envelope"
)

// bodySuffix ends the name of every file of bodies/.
const bodySuffix = ".body"

// BodyFileName is the name of the body file of the record file named recordName:
// the name without its extension, then ".body". It is the one place a body is
// named. A body belongs to exactly one record, and the name says which, so a
// transaction that changes the record also settles the body without asking who
// else refers to it, and a name of out/ carries its sender into bodies/ the same
// way. It panics for a name that is not a record file's: every caller takes the
// name from a record.
func BodyFileName(recordName string) string {
	stem, ok := strings.CutSuffix(recordName, recordSuffix)
	if !ok || stem == "" {
		panic(fmt.Sprintf("store: %q is not the name of a record file", recordName))
	}
	return stem + bodySuffix
}

// isBodyName reports whether name has the shape of a file of bodies/: the name of
// a record of in/, ctl/ or out/ without its extension, then ".body".
func isBodyName(name string) bool {
	stem, ok := strings.CutSuffix(name, bodySuffix)
	if !ok {
		return false
	}
	if isHashName(stem) {
		return true
	}
	_, _, ok = splitOutName(stem + recordSuffix)
	return ok
}

// bodyOwner is a record that may refer to a body: the name of its file and the
// reference it holds, nil when it needs none.
type bodyOwner struct {
	name string
	ref  *BodyRef
}

func outOwners(outs ...OutRecord) []bodyOwner {
	var owners []bodyOwner
	for _, o := range outs {
		owners = append(owners, bodyOwner{o.Name(), o.Body})
	}
	return owners
}

func inOwners(ins []InRecord) []bodyOwner {
	var owners []bodyOwner
	for _, in := range ins {
		owners = append(owners, bodyOwner{in.Name(), in.Body})
	}
	return owners
}

func ctlOwners(ctls []CtlRecord) []bodyOwner {
	var owners []bodyOwner
	for _, c := range ctls {
		owners = append(owners, bodyOwner{c.Name(), c.Body})
	}
	return owners
}

// bodyFiles are the files of the bodies content is for: one for each owner that
// refers to a body, each with the bytes its reference says. Content is nil
// exactly when no owner refers to a body; a record that refers to bytes not given
// with it, or bytes no record refers to, is refused here, before anything is
// written, so a record that is written never points at a body that was not.
func bodyFiles(content []byte, owners []bodyOwner) ([]file, error) {
	if len(content) > envelope.MaxBody {
		return nil, fmt.Errorf("store: a body of %d bytes is over the limit of %d", len(content), envelope.MaxBody)
	}
	var files []file
	for _, o := range owners {
		if o.ref == nil {
			continue
		}
		if content == nil {
			return nil, fmt.Errorf("store: %s refers to a body that is not written with it", o.name)
		}
		if want := BodyRefOf(content); *o.ref != want {
			return nil, fmt.Errorf("store: %s does not refer to the body written with it", o.name)
		}
		files = append(files, file{path.Join(BodiesDir, BodyFileName(o.name)), content})
	}
	if content != nil && len(files) == 0 {
		return nil, fmt.Errorf("store: a body is given and no record refers to one")
	}
	return files, nil
}

// droppedBodies are the body files of the owners that refer to none: a record
// that holds no reference owns no body, so a file of that name is what the change
// that dropped the reference has yet to remove. The owners are the records a
// transaction writes; the file is removed after them (removeBodies).
func droppedBodies(owners []bodyOwner) []FileRef {
	var files []FileRef
	for _, o := range owners {
		if o.ref == nil {
			files = append(files, FileRef{Dir: BodiesDir, Name: BodyFileName(o.name)})
		}
	}
	return files
}

// bodyFilesOf are the body files of the records named.
func bodyFilesOf(records []FileRef) []FileRef {
	files := make([]FileRef, len(records))
	for i, f := range records {
		files[i] = FileRef{Dir: BodiesDir, Name: BodyFileName(f.Name)}
	}
	return files
}

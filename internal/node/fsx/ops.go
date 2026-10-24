package fsx

// Stubs: the implementation lands after the tests that specify it.

func Replace(fs FS, tag Tag, path string, data []byte, mode Mode) error { panic("not implemented") }
func OpenAppend(fs FS, tag Tag, path string) (File, error)              { panic("not implemented") }
func Append(f File, tag Tag, data []byte) error                         { panic("not implemented") }
func SyncFile(fs FS, tag Tag, f File) error                             { panic("not implemented") }
func RenameFile(fs FS, tag Tag, from, to string) error                  { panic("not implemented") }
func Remove(fs FS, tag Tag, path string) error                          { panic("not implemented") }
func SyncDir(fs FS, tag Tag, dir string) error                          { panic("not implemented") }
func MakeDir(fs FS, tag Tag, dir string) error                          { panic("not implemented") }
func RenameDir(fs FS, tag Tag, from, to string) error                   { panic("not implemented") }
func RemoveAll(fs FS, tag Tag, dir string) error                        { panic("not implemented") }
func Discard(fs FS, tag Tag, dir string) error                          { panic("not implemented") }

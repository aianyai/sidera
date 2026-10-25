package ledger

import "os"

// ParseStream and SetSync expose the reader of one file and the sync of the file
// sink to the external tests.
var ParseStream = parseStream

// SetSync replaces the function a Dir's files are synced with.
func (d *Dir) SetSync(f func(*os.File) error) { d.syncFile = f }

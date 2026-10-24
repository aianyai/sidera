package store

import "time"

// ArchiveSummary is the summary of a record that left the live files.
type ArchiveSummary struct {
	At          time.Time
	Kind        Kind
	Key         string // the decoded text of the key
	State       string
	Reason      string
	AcceptedAt  time.Time
	ClosedAt    time.Time
	DeliveredAt time.Time
	Size        int
	Digest      string
	Quarantined bool
}

// ArchiveContents is what an archive file holds that can be read.
type ArchiveContents struct {
	Summaries []ArchiveSummary
	Events    []ArchiveEvent
	// Dropped counts the lines that are neither empty nor intact.
	Dropped int
}

// ParseArchive reads the contents of an archive file. A newline is a point
// where reading resumes, an empty line is skipped, a line that is not intact
// is dropped and counted, and a last line without a newline is a torn tail and
// is ignored.
func ParseArchive(data []byte) ArchiveContents { panic(errNotImplemented) }

// ReadArchive reads the archive file of the given name.
func (s *Store) ReadArchive(name string) (ArchiveContents, error) { panic(errNotImplemented) }

// ArchiveDay is the name of the archive file of the UTC date of t.
func ArchiveDay(t time.Time) string { panic(errNotImplemented) }

// ExpiredArchiveFiles returns the names of the archive files whose date is
// more than retentionDays days before the date of now, from the names of the
// directory. Names that are not archive file names are not returned.
func ExpiredArchiveFiles(names []string, now time.Time, retentionDays int) []string {
	panic(errNotImplemented)
}

package store

// CorruptFile is a quarantined file: `<original name>.<alert id>.corrupt`.
type CorruptFile struct {
	Dir      string
	Name     string // the full name
	Original string // the name the file had
	Alert    AlertID
	// Kind and Key are those of the first line, when it was readable; Key is
	// the parsed key.
	Kind Kind
	Key  any
}

// DamagedFile is a file that failed validation and is not quarantined yet.
type DamagedFile struct {
	Dir    string
	Name   string
	Damage Damage
	// Kind and Key are set when the damage is BodyDamaged: the kind and the
	// parsed key of the first line.
	Kind Kind
	Key  any
}

// Scan is everything state/ holds, decoded. The store keeps no index: the
// engine builds its view from this.
type Scan struct {
	Out         []OutRecord
	In          []InRecord
	Ctl         []CtlRecord
	Endpoints   []EndpointRecord
	Attempts    []AttemptsRecord
	Checkpoints []CheckpointRecord
	Admin       []AdminRecord
	Alerts      []AlertRecord
	Seq         []SeqRecord

	// Bodies are the names of the files of bodies/, none of them read.
	Bodies []string
	// Corrupt lists every quarantined file of every directory, bodies/
	// included.
	Corrupt []CorruptFile
	// Damaged lists the files that failed validation, in directory order.
	Damaged []DamagedFile
	// Unknown lists paths that are neither a record, a body, a quarantined
	// file nor debris of an interrupted write.
	Unknown []string
	// Archive lists the names of the files of archive/.
	Archive []string
}

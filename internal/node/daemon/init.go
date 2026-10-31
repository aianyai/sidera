package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/txn"
)

// InitReport is what init tells the owner: where the node is, which node it is,
// where the self-revocation is that has to be exported and kept off the machine,
// and where the interfaces will be reachable.
type InitReport struct {
	Home string
	Node ident.NodeID
	// SelfRevocationPath is the file that holds the statement that can retire the
	// identity for good; whoever holds a copy can do it.
	SelfRevocationPath string
	// Addresses is where each interface is: a socket path, or a pipe name.
	Addresses map[ipc.Kind]string
}

// Init makes a data directory: the root that only the owner can reach, the layout
// of every owner, the keys, the certificate, the self-revocation, the clock
// record, the default configuration, and last the marker that says it finished.
//
// It takes the instance lock first, so it cannot run beside a daemon or another
// init, and lets it go when it is done. It never overwrites: a file that is there
// and valid stays as it is, one that is there and not valid stops it, naming it;
// so an init that was interrupted is finished by running it again, and one that
// finished is refused (ErrAlreadyInitialized) with not one file changed. The
// directory of the sockets and the length of their paths are judged before the
// first directory is made.
func Init(ctx context.Context, cfg Config) (InitReport, error) {
	cfg = cfg.withDefaults()
	home, err := resolveHome(cfg.Home)
	if err != nil {
		return InitReport{}, err
	}
	rep := InitReport{Home: home, Addresses: map[ipc.Kind]string{}}
	for _, kind := range []ipc.Kind{ipc.KindAgent, ipc.KindManage} {
		addr, err := ipc.Locate(home, kind)
		if err != nil {
			return InitReport{}, err
		}
		rep.Addresses[kind] = addr.Name()
	}
	if err := ctx.Err(); err != nil {
		return InitReport{}, err
	}

	fsys := cfg.FS
	if fsys == nil {
		if fsys, err = fsx.OpenOS(home, cfg.TempRand); errors.Is(err, fs.ErrNotExist) {
			if err = fsx.MakeRoot(home); err == nil {
				fsys, err = fsx.OpenOS(home, cfg.TempRand)
			}
		}
		if err != nil {
			return InitReport{}, fmt.Errorf("daemon: making the data directory %s: %w", home, err)
		}
	}
	if err := checkRoot(fsys, home); err != nil {
		return InitReport{}, err
	}
	lock, err := takeLock(fsys, home)
	if err != nil {
		return InitReport{}, err
	}
	defer lock.release()

	// Looked at before anything is recovered or written: a data directory that is
	// finished is not touched at all.
	if done, err := keys.Initialized(fsys); err != nil {
		return InitReport{}, err
	} else if done {
		return InitReport{}, fmt.Errorf("%w: %s", ErrAlreadyInitialized, home)
	}

	tab, err := Table()
	if err != nil {
		return InitReport{}, err
	}
	w := txn.New(fsys, tab, cfg.Observer)
	if err := w.Recover(); err != nil {
		return InitReport{}, fmt.Errorf("daemon: recovering the data directory: %w", err)
	}
	r := w.Reader()
	kplan, err := keys.PlanInit(r, cfg.Rand, cfg.Clock.Now())
	if err != nil {
		return InitReport{}, err
	}
	cplan, err := config.PlanInit(r)
	if err != nil {
		return InitReport{}, err
	}
	var missing []string
	for _, d := range initDirs() {
		ok, err := fsx.Exists(r, d)
		if err != nil {
			return InitReport{}, err
		}
		if !ok {
			missing = append(missing, d)
		}
	}

	tx := w.Begin(&declInit)
	defer tx.Close()
	for _, d := range missing {
		if err := tx.MakeDir(roleDir, d); err != nil {
			return InitReport{}, err
		}
	}
	for _, step := range []func(*txn.Tx) error{cplan.MakeDirs, kplan.WriteKeys, cplan.WriteFiles, kplan.WriteMarker} {
		if err := step(tx); err != nil {
			return InitReport{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return InitReport{}, err
	}
	rep.Node = kplan.Node()
	rep.SelfRevocationPath = filepath.Join(home, filepath.FromSlash(keys.SelfRevocationPath))
	return rep, nil
}

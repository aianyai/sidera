package process

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// userHZ is the unit of the start time in /proc/<pid>/stat. It is 100 on every
// architecture Go supports; asking sysconf would need cgo.
const userHZ = 100

// The boot id and the boot time do not change while this process runs (the boot
// time moves only when the clock is set, which is why StartTime is not identity),
// so they are read once. A failed read is not kept, it is tried again.
var boot struct {
	sync.Mutex
	id string
	at time.Time
}

func bootID() (string, error) {
	boot.Lock()
	defer boot.Unlock()
	if boot.id == "" {
		raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
		if err != nil {
			return "", fmt.Errorf("process: read the boot id: %w", err)
		}
		boot.id = strings.TrimSpace(string(raw))
		if boot.id == "" {
			return "", errors.New("process: the boot id is empty")
		}
	}
	return boot.id, nil
}

func bootTime() (time.Time, error) {
	boot.Lock()
	defer boot.Unlock()
	if boot.at.IsZero() {
		raw, err := os.ReadFile("/proc/stat")
		if err != nil {
			return time.Time{}, fmt.Errorf("process: read the boot time: %w", err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if rest, ok := strings.CutPrefix(line, "btime "); ok {
				sec, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
				if err != nil {
					return time.Time{}, fmt.Errorf("process: read the boot time: %w", err)
				}
				boot.at = time.Unix(sec, 0)
				return boot.at, nil
			}
		}
		return time.Time{}, errors.New("process: /proc/stat has no btime")
	}
	return boot.at, nil
}

// parseStat reads the state and the start time (in clock ticks after boot) from
// the contents of /proc/<pid>/stat. The command name, field 2, is in parentheses
// and may hold spaces and parentheses itself, so the fields are counted from the
// last ')'.
func parseStat(raw []byte) (state byte, ticks uint64, err error) {
	end := bytes.LastIndexByte(raw, ')')
	if end < 0 {
		return 0, 0, errors.New("no command name in stat")
	}
	// After the name: state is field 3 and starttime field 22, that is index 0 and 19.
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 || len(fields[0]) != 1 {
		return 0, 0, errors.New("stat has too few fields")
	}
	ticks, err = strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("start time in stat: %w", err)
	}
	return fields[0][0], ticks, nil
}

// gone reports whether an error from /proc says the process does not exist (any
// more).
func gone(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

// denied reports whether an error from /proc says the process may not be read.
func denied(err error) bool {
	return errors.Is(err, os.ErrPermission)
}

// read describes the process of a /proc/<pid> directory. found is false for a
// process that does not exist and, unless includeEnded, for one that has ended
// (state Z or X).
func read(pid int, includeEnded bool) (p Process, found bool, err error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if gone(err) {
		return Process{}, false, nil
	}
	if err != nil {
		return Process{}, false, fmt.Errorf("process: look up process %d: %w", pid, err)
	}
	state, ticks, err := parseStat(raw)
	if err != nil {
		return Process{}, false, fmt.Errorf("process: look up process %d: %w", pid, err)
	}
	if (state == 'Z' || state == 'X') && !includeEnded {
		return Process{}, false, nil
	}
	id, err := bootID()
	if err != nil {
		return Process{}, false, err
	}
	at, err := bootTime()
	if err != nil {
		return Process{}, false, err
	}
	return Process{
		Identity:    Identity{PID: pid, StartKey: strconv.FormatUint(ticks, 10) + "@" + id},
		StartTime:   at.Add(time.Duration(ticks) * time.Second / userHZ),
		NativeStart: strconv.FormatUint(ticks, 10),
	}, true, nil
}

func lookup(pid int, includeEnded bool) (Process, bool, error) { return read(pid, includeEnded) }

func list() ([]Process, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("process: list processes: %w", err)
	}
	uid := uint32(os.Geteuid())
	var out []Process
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		fi, err := os.Stat("/proc/" + e.Name())
		if err != nil {
			if gone(err) || denied(err) {
				continue
			}
			return nil, fmt.Errorf("process: list processes: %w", err)
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Uid != uid {
			continue
		}
		p, found, err := read(pid, false)
		if err != nil {
			if denied(err) {
				continue
			}
			return nil, err
		}
		if !found {
			continue
		}
		cmdline, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil {
			if gone(err) || denied(err) {
				continue
			}
			return nil, fmt.Errorf("process: read the arguments of process %d: %w", pid, err)
		}
		p.Args = splitCmdline(cmdline)
		out = append(out, p)
	}
	return out, nil
}

// splitCmdline splits the contents of /proc/<pid>/cmdline, whose arguments are
// each ended by a NUL.
func splitCmdline(raw []byte) []string {
	raw = bytes.TrimSuffix(raw, []byte{0})
	if len(raw) == 0 {
		return nil
	}
	return strings.Split(string(raw), "\x00")
}

package skillwatch

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// MaybeRunHelper is the internal entry hosts call before any other work. When
// the process was started with the watcher helper environment flag it serves
// the pipe protocol on stdin/stdout and reports true; the caller must exit
// without initializing the application.
func MaybeRunHelper() bool {
	if os.Getenv(watchHelperEnv) != "1" {
		return false
	}
	_ = RunHelper(os.Stdin, os.Stdout)
	return true
}

// RunHelper serves one host connection: control frames are processed strictly
// serially on this goroutine (all watcher Add/Remove/Close calls happen here,
// which is the whole reason Windows watching moved into a helper), while a
// pump forwards filesystem events and a writer goroutine serializes outbound
// frames. Any write failure terminates the helper; the host detects the closed
// pipe and applies its restart budget.
func RunHelper(r io.Reader, w io.Writer) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		_ = writeFrame(w, frame{Kind: wireError, Msg: "backend unavailable: " + err.Error()})
		return err
	}

	outbound := make(chan frame, 256)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for f := range outbound {
			if err := writeFrame(w, f); err != nil {
				return
			}
		}
	}()

	var mu sync.Mutex
	// Physical directories are watched once across logical registrations.
	dirRefs := map[string]int{}
	// These indexes fence generations and map paths back to registrations.
	regDirs := map[uint64]map[string]struct{}{}
	regGen := map[uint64]uint64{}
	pathRegs := map[string]map[uint64]struct{}{}

	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				var op Op
				switch {
				case event.Op&fsnotify.Create != 0:
					op = OpCreate
				case event.Op&fsnotify.Remove != 0:
					op = OpRemove
				case event.Op&fsnotify.Rename != 0:
					op = OpRename
				case event.Op&fsnotify.Write != 0:
					op = OpWrite
				case event.Op&fsnotify.Chmod != 0:
					op = OpChmod
				default:
					continue
				}
				mu.Lock()
				// fsnotify reports the changed path; registrations cover
				// directories. Walk up to the watched ancestor(s).
				var ids []uint64
				for dir := event.Name; ; {
					for id := range pathRegs[dir] {
						ids = append(ids, id)
					}
					parent := filepath.Dir(dir)
					if parent == dir {
						break
					}
					dir = parent
				}
				mu.Unlock()
				slices.Sort(ids)
				for _, id := range ids {
					mu.Lock()
					gen := regGen[id]
					mu.Unlock()
					select {
					case outbound <- frame{Kind: wireEvent, ID: id, RootGen: gen, Op: op}:
					default:
						// The host timeout detects a stalled drain.
					}
				}
			case _, ok := <-watcher.Errors:
				if !ok {
					return
				}
				select {
				case outbound <- frame{Kind: wireError, Msg: "watcher backend error"}:
				default:
				}
			}
		}
	}()

	defer func() {
		// Stop pump sources before draining the writer.
		_ = watcher.Close()
		<-pumpDone
		close(outbound)
		<-writerDone
	}()

	_ = writeFrame(w, frame{Kind: wireReady})
	for {
		f, err := readFrame(r)
		if err != nil {
			return err
		}
		switch f.Kind {
		case wireRegister:
			if err := helperRegister(watcher, &mu, dirRefs, regDirs, regGen, pathRegs, f); err != nil {
				_ = writeFrame(w, frame{Kind: wireError, ID: f.ID, Msg: err.Error()})
				continue
			}
			_ = writeFrame(w, frame{Kind: wireRegistered, ID: f.ID})
		case wireCancel:
			helperCancel(&mu, dirRefs, regDirs, regGen, pathRegs, watcher, f.ID)
		case wirePing:
			_ = writeFrame(w, frame{Kind: wirePong})
		case wireShutdown:
			return nil
		default:
			_ = writeFrame(w, frame{Kind: wireError, ID: f.ID, Msg: "unexpected frame"})
		}
	}
}

func helperRegister(watcher *fsnotify.Watcher, mu *sync.Mutex, dirRefs map[string]int, regDirs map[uint64]map[string]struct{}, regGen map[uint64]uint64, pathRegs map[string]map[uint64]struct{}, f frame) error {
	mu.Lock()
	defer mu.Unlock()
	added := make([]string, 0, len(f.Dirs))
	dirs := make(map[string]struct{}, len(f.Dirs))
	for _, dir := range f.Dirs {
		clean := filepath.Clean(dir)
		dirs[clean] = struct{}{}
		if dirRefs[clean] == 0 {
			// The only goroutine that touches the watcher: the serialized
			// control loop, so Add cannot block a Close or another Add.
			if err := watcher.Add(clean); err != nil {
				for _, undo := range added {
					helperDropDir(dirRefs, pathRegs, watcher, undo, f.ID)
				}
				return err
			}
		}
		dirRefs[clean]++
		if pathRegs[clean] == nil {
			pathRegs[clean] = map[uint64]struct{}{}
		}
		pathRegs[clean][f.ID] = struct{}{}
		added = append(added, clean)
	}
	regDirs[f.ID] = dirs
	regGen[f.ID] = f.RootGen
	return nil
}

func helperCancel(mu *sync.Mutex, dirRefs map[string]int, regDirs map[uint64]map[string]struct{}, regGen map[uint64]uint64, pathRegs map[string]map[uint64]struct{}, watcher *fsnotify.Watcher, id uint64) {
	mu.Lock()
	defer mu.Unlock()
	for dir := range regDirs[id] {
		helperDropDir(dirRefs, pathRegs, watcher, dir, id)
	}
	delete(regDirs, id)
	delete(regGen, id)
}

func helperDropDir(dirRefs map[string]int, pathRegs map[string]map[uint64]struct{}, watcher *fsnotify.Watcher, dir string, id uint64) {
	// Caller holds mu.
	if regs := pathRegs[dir]; regs != nil {
		delete(regs, id)
		if len(regs) == 0 {
			delete(pathRegs, dir)
		}
	}
	dirRefs[dir]--
	if dirRefs[dir] <= 0 {
		delete(dirRefs, dir)
		_ = watcher.Remove(dir)
	}
}

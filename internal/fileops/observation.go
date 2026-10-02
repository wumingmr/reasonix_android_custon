// Package fileops owns the host-side observation state used by structured
// file tools.  Models never receive or provide these versions: a successful
// read records one and a later mutation compares it with the current source.
package fileops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

type ObservationKind uint8

const (
	Unseen ObservationKind = iota
	Absent
	Present
)

// Target is a normalized source identity. Route separates disk files from
// unsaved editor buffers; Key includes the host file identity when available.
type Target struct {
	Route string
	Key   string
	Path  string
}

// Version is an opaque value produced by the host.
type Version string

type Observation struct {
	Kind    ObservationKind
	Version Version
}

// Store belongs to one live agent session. It is intentionally not serialized.
type Store struct {
	mu    sync.Mutex
	items map[string]Observation
	paths map[string]Observation
}

func NewStore() *Store {
	return &Store{items: make(map[string]Observation), paths: make(map[string]Observation)}
}

// Clone transfers live observations to a replacement runtime in the same
// session. It is never serialized or reconstructed from transcript data.
func (s *Store) Clone() *Store {
	copy := NewStore()
	if s == nil {
		return copy
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	maps.Copy(copy.items, s.items)
	maps.Copy(copy.paths, s.paths)
	return copy
}

func (s *Store) Get(target Target) Observation {
	if s == nil {
		return Observation{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if observation, ok := s.items[target.Route+"\x00"+target.Key]; ok {
		return observation
	}
	return s.paths[target.Route+"\x00"+target.Path]
}

func (s *Store) ObservePresent(target Target, version Version) {
	if s == nil || target.Key == "" || version == "" {
		return
	}
	s.mu.Lock()
	observation := Observation{Kind: Present, Version: version}
	s.items[target.Route+"\x00"+target.Key] = observation
	s.paths[target.Route+"\x00"+target.Path] = observation
	s.mu.Unlock()
}

func (s *Store) ObserveAbsent(target Target) {
	if s == nil || target.Key == "" {
		return
	}
	s.mu.Lock()
	observation := Observation{Kind: Absent}
	s.items[target.Route+"\x00"+target.Key] = observation
	s.paths[target.Route+"\x00"+target.Path] = observation
	s.mu.Unlock()
}

func (s *Store) Forget(target Target) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.items, target.Route+"\x00"+target.Key)
	delete(s.paths, target.Route+"\x00"+target.Path)
	s.mu.Unlock()
}

type storeKey struct{}

func WithStore(ctx context.Context, store *Store) context.Context {
	return context.WithValue(ctx, storeKey{}, store)
}

func FromContext(ctx context.Context) *Store {
	store, _ := ctx.Value(storeKey{}).(*Store)
	return store
}

// DiskTarget canonicalizes symlinks and uses the native file identity fields
// exposed by os.FileInfo.Sys when the file exists. This makes hard-link aliases
// share observation and mutation-lock identities on supported platforms.
func DiskTarget(path string, info os.FileInfo) Target {
	path = canonicalPath(path)
	if identity, _ := diskNativeSnapshot(path); identity != "" {
		return Target{Route: "disk", Key: "native:" + identity, Path: path}
	}
	if info != nil {
		if identity := nativeIdentity(info); identity != "" {
			return Target{Route: "disk", Key: "native:" + identity, Path: path}
		}
	}
	return Target{Route: "disk", Key: "path:" + path, Path: path}
}

// DiskSnapshot returns a target identity and version derived from one path
// observation. Windows augments os.FileInfo with volume, file-index, and
// change-time data obtained from the native handle APIs.
func DiskSnapshot(path string, info os.FileInfo) (Target, Version) {
	path = canonicalPath(path)
	identity, native := diskNativeSnapshot(path)
	return diskSnapshot(path, info, identity, native)
}

// DiskHandleSnapshot derives native identity and change metadata from the open
// file handle that supplied the bytes. This prevents a pathname replacement
// during a bounded read from being mistaken for the source that was observed.
func DiskHandleSnapshot(path string, file *os.File, info os.FileInfo) (Target, Version) {
	path = canonicalPath(path)
	identity, native := diskNativeHandleSnapshot(file)
	return diskSnapshot(path, info, identity, native)
}

func diskSnapshot(path string, info os.FileInfo, identity string, native []string) (Target, Version) {
	if identity == "" && info != nil {
		identity = nativeIdentity(info)
	}
	key := "path:" + path
	if identity != "" {
		key = "native:" + identity
	}
	target := Target{Route: "disk", Key: key, Path: path}
	parts := diskVersionParts(info)
	parts = append(parts, native...)
	if len(parts) == 0 {
		return target, ""
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return target, Version("disk-v1:" + hex.EncodeToString(sum[:]))
}

func OverlayTarget(path string) Target {
	path = canonicalPath(path)
	return Target{Route: "overlay", Key: "path:" + path, Path: path}
}

// OverlayTargetWithIdentity binds an observation to the transport/buffer owner
// that supplied the text. The identity is hashed so it stays host-internal and
// never appears in a tool result or serialized session.
func OverlayTargetWithIdentity(path, identity string) Target {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return OverlayTarget(path)
	}
	sum := sha256.Sum256([]byte(identity))
	target := OverlayTarget(path)
	target.Route = "overlay:" + hex.EncodeToString(sum[:])
	return target
}

func canonicalPath(path string) string {
	path = filepath.Clean(path)
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	// Resolve the nearest existing ancestor too: an absent child beneath a
	// symlinked directory must keep its identity before and after creation.
	ancestor, suffix := path, ""
	for {
		if resolved, err := filepath.EvalSymlinks(ancestor); err == nil {
			path = filepath.Join(resolved, suffix)
			break
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			break
		}
		suffix = filepath.Join(filepath.Base(ancestor), suffix)
		ancestor = parent
	}
	return filepath.Clean(path)
}

// DiskVersion deliberately uses metadata only. A window read therefore never
// scans the rest of a large file merely to mint a version.
func DiskVersion(info os.FileInfo) Version {
	parts := diskVersionParts(info)
	if len(parts) == 0 {
		return ""
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return Version("disk-v1:" + hex.EncodeToString(sum[:]))
}

func diskVersionParts(info os.FileInfo) []string {
	if info == nil {
		return nil
	}
	parts := []string{
		fmt.Sprintf("size=%d", info.Size()),
		fmt.Sprintf("mode=%#o", uint32(info.Mode())),
		fmt.Sprintf("mtime=%d", info.ModTime().UnixNano()),
	}
	if sys := nativeMetadata(info); len(sys) != 0 {
		parts = append(parts, sys...)
	}
	return parts
}

func OverlayVersion(content string) Version {
	sum := sha256.Sum256([]byte(content))
	return Version("overlay-v1:" + hex.EncodeToString(sum[:]))
}

// nativeMetadata extracts stable scalar and time fields without importing a
// platform-specific syscall type. This includes inode/device/ctime on Unix and
// file index/change timestamps from the native Windows file info structure.
func nativeMetadata(info os.FileInfo) []string {
	v := reflect.ValueOf(info.Sys())
	if !v.IsValid() {
		return nil
	}
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	var out []string
	for i := range v.NumField() {
		field := v.Type().Field(i)
		name := strings.ToLower(field.Name)
		if !metadataField(name) {
			continue
		}
		if value, ok := scalarValue(v.Field(i)); ok {
			out = append(out, name+"="+value)
		}
	}
	return out
}

func metadataField(name string) bool {
	for _, part := range []string{"dev", "ino", "fileindex", "volume", "ctim", "change", "mtim", "lastwrite", "creation", "mode", "nlink", "uid", "gid"} {
		if strings.Contains(name, part) {
			return true
		}
	}
	return false
}

func scalarValue(v reflect.Value) (string, bool) {
	if !v.IsValid() {
		return "", false
	}
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return fmt.Sprintf("%d", v.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return fmt.Sprintf("%d", v.Uint()), true
	case reflect.Struct:
		if v.CanInterface() {
			if t, ok := v.Interface().(time.Time); ok {
				return fmt.Sprintf("%d", t.UnixNano()), true
			}
		}
		var parts []string
		for i := range v.NumField() {
			if value, ok := scalarValue(v.Field(i)); ok {
				parts = append(parts, value)
			}
		}
		if len(parts) != 0 {
			return strings.Join(parts, ":"), true
		}
	}
	return "", false
}

func nativeIdentity(info os.FileInfo) string {
	var parts []string
	for _, item := range nativeMetadata(info) {
		name := strings.SplitN(item, "=", 2)[0]
		if strings.Contains(name, "dev") || strings.Contains(name, "ino") || strings.Contains(name, "fileindex") || strings.Contains(name, "volume") {
			parts = append(parts, item)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

const lockStripes = 257

var mutationLocks [lockStripes]sync.Mutex

// Lock serializes mutations of one normalized target within this host process.
func Lock(target Target) func() {
	return LockMany(target)
}

// LockMany acquires unique striped locks in stable order.
func LockMany(targets ...Target) func() {
	indices := make([]int, 0, len(targets))
	seen := make(map[int]struct{}, len(targets))
	for _, target := range targets {
		// Replacement changes the inode. Keep a stable path lock as well as
		// the native identity lock, so new arrivals cannot bypass old waiters.
		for _, key := range []string{target.Key, "path:" + target.Path} {
			sum := sha256.Sum256([]byte(target.Route + "\x00" + key))
			index := int((uint16(sum[0])<<8 | uint16(sum[1])) % lockStripes)
			if _, ok := seen[index]; !ok {
				seen[index] = struct{}{}
				indices = append(indices, index)
			}
		}
	}
	sort.Ints(indices)
	for _, index := range indices {
		mutationLocks[index].Lock()
	}
	return func() {
		for _, index := range slices.Backward(indices) {
			mutationLocks[index].Unlock()
		}
	}
}

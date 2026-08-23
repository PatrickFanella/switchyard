package gitworktree

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	manifestReadBatch       = 128
	defaultManifestMaxFiles = 100_000
	defaultManifestMaxBytes = int64(1 << 30)
)

// ManifestLimits bounds worktree traversal before file contents are read.
type ManifestLimits struct {
	MaxFiles   int
	MaxBytes   int64
	MaxEntries int
	MaxDepth   int
}

type manifestFile struct {
	path string
	stat unix.Stat_t
}

// Manifest returns a deterministic SHA-256 digest of regular file paths,
// permission modes, and contents held beneath worktree's directory descriptor.
// The linked-worktree .git file at the root is metadata and is excluded.
func Manifest(ctx context.Context, worktree *os.File) (string, error) {
	return ManifestBounded(ctx, worktree, ManifestLimits{
		MaxFiles: defaultManifestMaxFiles,
		MaxBytes: defaultManifestMaxBytes,
	})
}

// ManifestBounded is Manifest with caller-supplied traversal limits.
func ManifestBounded(ctx context.Context, worktree *os.File, limits ManifestLimits) (string, error) {
	if worktree == nil {
		return "", errors.New("manifest worktree directory is required")
	}
	if limits.MaxFiles <= 0 || limits.MaxBytes <= 0 {
		return "", errors.New("manifest limits must be positive")
	}
	if limits.MaxEntries <= 0 {
		limits.MaxEntries = limits.MaxFiles
	}
	if limits.MaxDepth <= 0 {
		limits.MaxDepth = 256
	}
	files := make([]manifestFile, 0, min(limits.MaxFiles, manifestReadBatch))
	var bytes int64
	var entries int
	if err := collectManifestFiles(ctx, int(worktree.Fd()), "", 0, limits, &files, &bytes, &entries); err != nil {
		return "", err
	}
	sort.Slice(files, func(left, right int) bool { return files[left].path < files[right].path })
	digest := sha256.New()
	for _, file := range files {
		if err := hashManifestFile(ctx, int(worktree.Fd()), file, digest); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// Manifest returns the digest of the descriptor-bound worktree identity.
func (i Identity) Manifest(ctx context.Context) (string, error) {
	return Manifest(ctx, i.Worktree)
}

func collectManifestFiles(ctx context.Context, directoryFD int, prefix string, depth int, limits ManifestLimits, files *[]manifestFile, bytes *int64, entryCount *int) error {
	if depth > limits.MaxDepth {
		return errors.New("manifest depth limit exceeded")
	}
	duplicate, err := unix.Openat(directoryFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return errors.New("duplicate manifest directory")
	}
	directory := os.NewFile(uintptr(duplicate), "manifest-directory")
	defer directory.Close()
	for {
		entries, readErr := directory.ReadDir(manifestReadBatch)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("manifest canceled: %w", err)
			}
			if prefix == "" && entry.Name() == ".git" {
				continue
			}
			*entryCount++
			if *entryCount > limits.MaxEntries {
				return errors.New("manifest entry limit exceeded")
			}
			path := entry.Name()
			if prefix != "" {
				path = prefix + "/" + entry.Name()
			}
			var stat unix.Stat_t
			if err := unix.Fstatat(directoryFD, entry.Name(), &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return errors.New("inspect manifest entry")
			}
			switch stat.Mode & unix.S_IFMT {
			case unix.S_IFDIR:
				childFD, err := unix.Openat(directoryFD, entry.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
				if err != nil {
					return errors.New("open manifest directory")
				}
				err = collectManifestFiles(ctx, childFD, path, depth+1, limits, files, bytes, entryCount)
				_ = unix.Close(childFD)
				if err != nil {
					return err
				}
			case unix.S_IFREG:
				if len(*files) >= limits.MaxFiles {
					return errors.New("manifest file limit exceeded")
				}
				if stat.Size < 0 || *bytes > limits.MaxBytes-stat.Size {
					return errors.New("manifest byte limit exceeded")
				}
				*bytes += stat.Size
				*files = append(*files, manifestFile{path: path, stat: stat})
			default:
				return errors.New("manifest rejects symlinks and special files")
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return errors.New("read manifest directory")
		}
	}
}

func hashManifestFile(ctx context.Context, rootFD int, expected manifestFile, digest hash.Hash) error {
	fd, err := openManifestFile(rootFD, expected.path)
	if err != nil {
		return errors.New("open manifest file")
	}
	file := os.NewFile(uintptr(fd), expected.path)
	defer file.Close()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil || !sameManifestStat(expected.stat, before) {
		return errors.New("manifest entry changed during traversal")
	}
	writeManifestField(digest, []byte(expected.path))
	var mode [4]byte
	gitMode := uint32(0o100644)
	if before.Mode&0o111 != 0 {
		gitMode = 0o100755
	}
	binary.BigEndian.PutUint32(mode[:], gitMode)
	writeManifestField(digest, mode[:])
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(before.Size))
	writeManifestField(digest, size[:])
	written, err := io.Copy(digest, contextReader{ctx: ctx, reader: file})
	if err != nil || written != before.Size {
		return errors.New("read manifest file")
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil || !sameManifestStat(before, after) {
		return errors.New("manifest file changed while hashing")
	}
	return nil
}

func openManifestFile(rootFD int, path string) (int, error) {
	components := strings.Split(filepath.ToSlash(path), "/")
	current, err := unix.Dup(rootFD)
	if err != nil {
		return -1, err
	}
	for index, component := range components {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if index < len(components)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, openErr := unix.Openat(current, component, flags, 0)
		_ = unix.Close(current)
		if openErr != nil {
			return -1, openErr
		}
		current = next
	}
	return current, nil
}

func sameManifestStat(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && left.Mode == right.Mode && left.Size == right.Size &&
		left.Mtim == right.Mtim && left.Ctim == right.Ctim
}

func writeManifestField(digest hash.Hash, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(value)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(value []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(value)
}

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
)

func main() {
	// ls lives for a few ms, so skip GC entirely. the limit turns it back on
	// if a huge directory would otherwise eat all the ram
	debug.SetGCPercent(-1)
	debug.SetMemoryLimit(256 << 20)

	path := "."
	show_all, long_format := false, false

	for _, arg := range os.Args[1:] {
		if !strings.HasPrefix(arg, "-") {
			path = arg
			continue
		}
		// -l, -la, -a
		for _, char := range arg[1:] {
			switch char {
			case 'a':
				show_all = true
			case 'l':
				long_format = true
			default:
				fmt.Fprintf(os.Stderr, "usage: %s [-al] [path]\n", os.Args[0])
				os.Exit(1)
			}

		}
	}

	entries, error := os.ReadDir(path)
	if error != nil {
		fmt.Fprintln(os.Stderr, error)
		os.Exit(1)
	}

	var names []string
	if show_all {
		names = append(names, ".", "..")
	}

	for _, entry := range entries {
		if !show_all && strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		names = append(names, entry.Name())
	}

	// one write(2) at the end instead of one per line
	out := bufio.NewWriterSize(os.Stdout, 64<<10)
	defer out.Flush()

	if long_format {
		print_long(out, path, names)
		return
	}
	for _, name := range names {
		fmt.Fprintln(out, name)
	}
}

func mode_string(mode uint32) string {
	var s [10]byte
	switch mode & syscall.S_IFMT {
	case syscall.S_IFDIR:
		s[0] = 'd'
	case syscall.S_IFLNK:
		s[0] = 'l'
	case syscall.S_IFCHR:
		s[0] = 'c'
	case syscall.S_IFBLK:
		s[0] = 'b'
	case syscall.S_IFIFO:
		s[0] = 'p'
	case syscall.S_IFSOCK:
		s[0] = 's'
	default:
		s[0] = '-'
	}
	copy(s[1:], os.FileMode(mode).Perm().String()[1:])

	// setuid / setgid / sticky steal the x slot, lowercase if x is also set
	special := func(bit uint32, i int, c byte) {
		if mode&bit == 0 {
			return
		}
		if s[i] == 'x' {
			s[i] = c
		} else {
			s[i] = c - 'a' + 'A'
		}
	}
	special(syscall.S_ISUID, 3, 's')
	special(syscall.S_ISGID, 6, 's')
	special(syscall.S_ISVTX, 9, 't')
	return string(s[:])
}

// align: -1 left, 1 right, 0 none. fmt treats a negative %*s width as left-align
type cell struct {
	text  string
	align int
}

// two passes: stat everything first, then we know how wide each column is
func print_long(out *bufio.Writer, dir string, names []string) {
	rows := make([][7]cell, 0, len(names))
	var width [7]int
	// libc reopens /etc/passwd on every lookup, so ask once per id. unknown ids
	// print as the raw number, right-aligned like every other number in real ls
	owners, groups := map[uint32]cell{}, map[uint32]cell{}

	for _, name := range names {
		full_path := filepath.Join(dir, name)
		file_info, error := os.Lstat(full_path)
		if error != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, error)
			continue
		}
		file_stats := file_info.Sys().(*syscall.Stat_t)
		owner, ok := owners[file_stats.Uid]
		if !ok {
			owner = cell{strconv.Itoa(int(file_stats.Uid)), 1}
			if u, error := user.LookupId(owner.text); error == nil {
				owner = cell{u.Username, -1}
			}
			owners[file_stats.Uid] = owner
		}
		group, ok := groups[file_stats.Gid]
		if !ok {
			group = cell{strconv.Itoa(int(file_stats.Gid)), 1}
			if g, error := user.LookupGroupId(group.text); error == nil {
				group = cell{g.Name, -1}
			}
			groups[file_stats.Gid] = group
		}
		if file_info.Mode()&os.ModeSymlink != 0 {
			if target, error := os.Readlink(full_path); error == nil {
				name += " -> " + target
			}
		}
		row := [7]cell{
			{mode_string(uint32(file_stats.Mode)), 0},
			{strconv.FormatUint(uint64(file_stats.Nlink), 10), 1},
			owner,
			group,
			{strconv.FormatInt(file_info.Size(), 10), 1},
			{file_info.ModTime().Format("Jan _2 15:04"), 0},
			{name, 0},
		}
		for i, c := range row {
			width[i] = max(width[i], len(c.text))
		}
		rows = append(rows, row)
	}

	args := make([]any, 0, 14)
	for _, row := range rows {
		args = args[:0]
		for i, c := range row {
			args = append(args, c.align*width[i], c.text)
		}
		fmt.Fprintf(out, "%*s %*s %*s %*s %*s %*s %*s\n", args...)
	}
}

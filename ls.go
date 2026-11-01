package main

import (
	"bufio"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
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

	// os.Root keeps the directory open, so every stat below is an fstatat
	// on its fd instead of the kernel walking the full path again
	root, error := os.OpenRoot(path)
	if error != nil {
		fmt.Fprintln(os.Stderr, error)
		os.Exit(1)
	}
	dir, error := root.Open(".")
	if error != nil {
		fmt.Fprintln(os.Stderr, error)
		os.Exit(1)
	}
	// Readdirnames skips the DirEntry per file that ReadDir allocates, and
	// sorting plain strings beats sorting interfaces
	names, error := dir.Readdirnames(-1)
	if error != nil {
		fmt.Fprintln(os.Stderr, error)
		os.Exit(1)
	}
	slices.Sort(names)
	if show_all {
		names = append([]string{".", ".."}, names...)
	} else {
		names = slices.DeleteFunc(names, func(name string) bool { return name[0] == '.' })
	}

	// one write(2) per 64k instead of one per line
	out := bufio.NewWriterSize(os.Stdout, 64<<10)
	defer out.Flush()

	if long_format {
		print_long(out, root, names)
		return
	}
	for _, name := range names {
		out.WriteString(name)
		out.WriteByte('\n')
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

// memo returns cache[key], only calling compute the first time it sees key
func memo[K comparable, V any](cache map[K]V, key K, compute func(K) V) V {
	value, ok := cache[key]
	if !ok {
		value = compute(key)
		cache[key] = value
	}
	return value
}

// align: -1 left, 1 right, 0 none
type cell struct {
	text  string
	align int
}

// libc reopens /etc/passwd on every lookup, hence the memo. unknown ids print
// as the raw number, right-aligned like every other number in real ls
func owner_cell(uid uint32) cell {
	id := strconv.Itoa(int(uid))
	if u, error := user.LookupId(id); error == nil {
		return cell{u.Username, -1}
	}
	return cell{id, 1}
}

func group_cell(gid uint32) cell {
	id := strconv.Itoa(int(gid))
	if g, error := user.LookupGroupId(id); error == nil {
		return cell{g.Name, -1}
	}
	return cell{id, 1}
}

func time_string(sec int64) string {
	return time.Unix(sec, 0).Format("Jan _2 15:04")
}

// two passes: stat everything first, then we know how wide each column is
func print_long(out *bufio.Writer, root *os.Root, names []string) {
	rows := make([][7]cell, 0, len(names))
	var width [7]int
	owners, groups := map[uint32]cell{}, map[uint32]cell{}
	modes, times := map[uint32]string{}, map[int64]string{}

	// stat in parallel, one chunk of names per cpu
	infos, errors := make([]os.FileInfo, len(names)), make([]error, len(names))
	chunk := max(1, (len(names)+runtime.NumCPU()-1)/runtime.NumCPU())
	var wg sync.WaitGroup
	for start := 0; start < len(names); start += chunk {
		wg.Go(func() {
			for i := start; i < min(start+chunk, len(names)); i++ {
				if names[i] == ".." { // os.Root won't step outside itself
					infos[i], errors[i] = os.Lstat(filepath.Join(root.Name(), ".."))
				} else {
					infos[i], errors[i] = root.Lstat(names[i])
				}
			}
		})
	}
	wg.Wait()

	for i, name := range names {
		file_info, error := infos[i], errors[i]
		if error != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, error)
			continue
		}
		stat := file_info.Sys().(*syscall.Stat_t)
		if file_info.Mode()&os.ModeSymlink != 0 {
			if target, error := root.Readlink(name); error == nil {
				name += " -> " + target
			}
		}
		row := [7]cell{
			{memo(modes, uint32(stat.Mode), mode_string), 0},
			{strconv.FormatUint(uint64(stat.Nlink), 10), 1},
			memo(owners, stat.Uid, owner_cell),
			memo(groups, stat.Gid, group_cell),
			{strconv.FormatInt(file_info.Size(), 10), 1},
			{memo(times, file_info.ModTime().Unix(), time_string), 0},
			{name, 0},
		}
		for column, c := range row {
			width[column] = max(width[column], len(c.text))
		}
		rows = append(rows, row)
	}

	spaces := strings.Repeat(" ", slices.Max(width[:]))
	for _, row := range rows {
		for column, c := range row {
			gap := spaces[:width[column]-len(c.text)]
			if c.align > 0 {
				out.WriteString(gap)
			}
			out.WriteString(c.text)
			if c.align < 0 {
				out.WriteString(gap)
			}
			if column < len(row)-1 {
				out.WriteByte(' ')
			}
		}
		out.WriteByte('\n')
	}
}

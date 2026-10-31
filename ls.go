package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func main() {
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

	if long_format {
		print_long(path, names)
		return
	}
	for _, name := range names {
		fmt.Println(name)
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

type long_row struct {
	mode  string
	links uint64
	owner string
	group string
	size  int64
	time  string
	name  string
}

// two passes: stat everything first, then we know how wide each column is
func print_long(dir string, names []string) {
	var rows []long_row
	links_width, owner_width, group_width, size_width := 0, 0, 0, 0

	for _, name := range names {
		full_path := filepath.Join(dir, name)
		file_info, error := os.Lstat(full_path)
		if error != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, error)
			continue
		}
		file_stats := file_info.Sys().(*syscall.Stat_t)
		owner, group := "?", "?"
		if u, error := user.LookupId(strconv.Itoa(int(file_stats.Uid))); error == nil {
			owner = u.Username
		}
		if g, error := user.LookupGroupId(strconv.Itoa(int(file_stats.Gid))); error == nil {
			group = g.Name
		}
		if file_info.Mode()&os.ModeSymlink != 0 {
			if target, error := os.Readlink(full_path); error == nil {
				name += " -> " + target
			}
		}
		row := long_row{
			mode:  mode_string(uint32(file_stats.Mode)),
			links: uint64(file_stats.Nlink),
			owner: owner,
			group: group,
			size:  file_info.Size(),
			time:  file_info.ModTime().Format("Jan _2 15:04"),
			name:  name,
		}
		links_width = max(links_width, len(strconv.FormatUint(row.links, 10)))
		owner_width = max(owner_width, len(row.owner))
		group_width = max(group_width, len(row.group))
		size_width = max(size_width, len(strconv.FormatInt(row.size, 10)))
		rows = append(rows, row)
	}

	// numbers right-aligned, names left-aligned, same as real ls
	for _, row := range rows {
		fmt.Printf("%s %*d %-*s %-*s %*d %s %s\n",
			row.mode,
			links_width, row.links,
			owner_width, row.owner,
			group_width, row.group,
			size_width, row.size,
			row.time,
			row.name,
		)
	}
}

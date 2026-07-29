// Command notice writes the third-party notice file that ships beside a
// distributed artifact.
//
// It reads the module graph of the package being distributed — the same graph
// the linker walks — and reproduces, verbatim, each dependency module's
// license, notice, and patent-grant texts. Apache-2.0 §4(d) requires the
// attribution notices of Apache-licensed dependencies to travel with every
// distributed copy; MIT/BSD/ISC require their copyright and permission notices
// to travel the same way.
//
// The graph depends on the build configuration, so callers must describe the
// artifact they are shipping: -tags and CGO_ENABLED select which packages are
// reachable (blst is linked only under cgo), and -platforms unions over the
// GOOS/GOARCH set when one notice file serves several archives. Pointed at the
// wrong configuration this reports the wrong dependency set.
//
// Output is deterministic: modules sorted by path, files sorted by name, no
// timestamps. Byte-identical input produces a byte-identical file.
//
//	CGO_ENABLED=0 go run ./internal/notice -tags embedui -pkg ./cmd/amld \
//	  -platforms linux/amd64,darwin/arm64 -o THIRD-PARTY-NOTICES
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// module is the subset of `go list -json` output this command needs.
type module struct {
	Path    string
	Version string
	Dir     string
	Main    bool
}

type pkg struct {
	Module *module
}

// kinds maps a filename stem to the section heading it is reproduced under.
// One rule covers every spelling and extension a module might use: match the
// stem case-insensitively, ignore the extension.
var kinds = map[string]string{
	"license":   "LICENSE",
	"licence":   "LICENSE",
	"licenses":  "LICENSE",
	"copying":   "LICENSE",
	"unlicense": "LICENSE",
	"notice":    "NOTICE",
	"patents":   "PATENT GRANT",
}

// legal reports whether name is a legal text to reproduce, and under which
// heading. Names carry a distinguishing suffix through to the heading so a
// module shipping both LICENSE-MIT and LICENSE-APACHE keeps them apart.
func legal(name string) (heading string, ok bool) {
	stem := strings.ToLower(name)
	if i := strings.LastIndex(stem, "."); i > 0 {
		stem = stem[:i]
	}
	if h, ok := kinds[stem]; ok {
		return h, true
	}
	// LICENSE-MIT, LICENSE.APACHE2, COPYING.LESSER, license_apache.
	for _, sep := range []string{"-", "_", "."} {
		if i := strings.Index(stem, sep); i > 0 {
			if h, ok := kinds[stem[:i]]; ok {
				return h + " (" + name + ")", true
			}
		}
	}
	return "", false
}

// texts returns the legal texts in a module root, keyed by heading, sorted by
// filename so the result is stable across filesystems.
func texts(dir string) (map[string]string, []string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	names := make([]string, 0, 4)
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if _, ok := legal(e.Name()); ok {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	out := make(map[string]string, len(names))
	headings := make([]string, 0, len(names))
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, nil, err
		}
		heading, _ := legal(name)
		if _, dup := out[heading]; dup {
			heading += " (" + name + ")"
		}
		out[heading] = strings.TrimRight(string(b), "\n") + "\n"
		headings = append(headings, heading)
	}
	return out, headings, nil
}

// collect adds to seen every dependency module whose code is compiled into
// pkgPath for one GOOS/GOARCH. The main module is excluded: its own LICENSE
// ships separately. Standard-library packages have no module and are skipped.
func collect(seen map[string]module, tags, pkgPath, goos, goarch string) error {
	args := []string{"list", "-deps", "-json"}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, pkgPath)

	cmd := exec.Command(goBin(), args...)
	cmd.Stderr = os.Stderr
	if goos != "" {
		cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	dec := json.NewDecoder(out)
	for {
		var p pkg
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("decoding go list output: %w", err)
		}
		if p.Module == nil || p.Module.Main || p.Module.Version == "" {
			continue
		}
		seen[p.Module.Path] = *p.Module
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("go %s (GOOS=%s GOARCH=%s): %w", strings.Join(args, " "), goos, goarch, err)
	}
	return nil
}

// modules returns the union of the dependency modules across every platform the
// artifact is released for, sorted by module path.
//
// The set is platform-dependent — darwin links ncruces/go-strftime and linux
// does not, linux links prometheus/procfs and darwin does not — and one notice
// file goes into every archive. A union over-attributes on some platforms,
// which costs nothing; describing only the host platform would omit a license
// from the archives built for the others, which is the whole failure being
// fixed here.
func modules(tags, pkgPath string, platforms []string) ([]module, error) {
	seen := map[string]module{}
	if len(platforms) == 0 {
		if err := collect(seen, tags, pkgPath, "", ""); err != nil {
			return nil, err
		}
	}
	for _, p := range platforms {
		goos, goarch, ok := strings.Cut(p, "/")
		if !ok {
			return nil, fmt.Errorf("platform %q is not GOOS/GOARCH", p)
		}
		if err := collect(seen, tags, pkgPath, goos, goarch); err != nil {
			return nil, err
		}
	}

	mods := make([]module, 0, len(seen))
	for _, m := range seen {
		mods = append(mods, m)
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].Path < mods[j].Path })
	return mods, nil
}

// goBin resolves the toolchain that is running this command, so the generated
// file describes the graph the build will actually use.
func goBin() string {
	if exe := os.Getenv("GOEXE_PATH"); exe != "" {
		return exe
	}
	if root := os.Getenv("GOROOT"); root != "" {
		if exe := filepath.Join(root, "bin", "go"); fileExists(exe) {
			return exe
		}
	}
	return "go"
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

const rule = "--------------------------------------------------------------------------------"

func render(w io.Writer, artifact string, mods []module, platforms []string) error {
	var bare []module
	body := &strings.Builder{}

	for _, m := range mods {
		if m.Dir == "" {
			bare = append(bare, m)
			continue
		}
		byHeading, headings, err := texts(m.Dir)
		if err != nil {
			return fmt.Errorf("%s@%s: %w", m.Path, m.Version, err)
		}
		if len(headings) == 0 {
			bare = append(bare, m)
			continue
		}
		fmt.Fprintf(body, "\n%s\n%s %s\n%s\n", rule, m.Path, m.Version, rule)
		for _, h := range headings {
			fmt.Fprintf(body, "\n---- %s ----\n\n%s", h, byHeading[h])
		}
	}

	fmt.Fprintf(w, "Third-party notices for %s\n", artifact)
	fmt.Fprintf(w, "%s\n\n", strings.Repeat("=", 24+len(artifact)))
	scope := "the platform it was generated on"
	if len(platforms) > 0 {
		scope = strings.Join(platforms, ", ")
	}
	fmt.Fprintf(w, `%s links the modules listed below. Their license, notice, and patent-grant
texts are reproduced verbatim in the sections that follow. This file is
generated from the build's own module graph by `+"`go run ./internal/notice`"+`;
it covers third-party code only. The license of %s itself is in LICENSE.

The module set is platform-dependent, so this is the union over every platform
%s is released for: %s. A module listed here may
therefore not be linked into the particular binary beside this file.

Modules: %d

`, artifact, artifact, artifact, scope, len(mods))

	for _, m := range mods {
		fmt.Fprintf(w, "  %s %s\n", m.Path, m.Version)
	}

	if _, err := io.WriteString(w, body.String()); err != nil {
		return err
	}

	if len(bare) > 0 {
		fmt.Fprintf(w, "\n%s\nModules with no license text in the module root\n%s\n\n", rule, rule)
		io.WriteString(w, "No license, notice, or patent file was found at the root of these\nmodules, so there is no text to reproduce for them:\n\n")
		for _, m := range bare {
			fmt.Fprintf(w, "  %s %s\n", m.Path, m.Version)
		}
	}
	return nil
}

func main() {
	tags := flag.String("tags", "", "build tags for the distributed artifact")
	pkgPath := flag.String("pkg", "./...", "package whose module graph is described")
	out := flag.String("o", "THIRD-PARTY-NOTICES", "output file")
	name := flag.String("name", "", "artifact name in the header (default: base of -pkg)")
	plat := flag.String("platforms", "", "comma-separated GOOS/GOARCH list to union over (default: host)")
	flag.Parse()

	artifact := *name
	if artifact == "" {
		artifact = filepath.Base(*pkgPath)
	}

	var platforms []string
	if *plat != "" {
		platforms = strings.Split(*plat, ",")
	}

	mods, err := modules(*tags, *pkgPath, platforms)
	if err != nil {
		fmt.Fprintln(os.Stderr, "notice:", err)
		os.Exit(1)
	}
	if len(mods) == 0 {
		fmt.Fprintln(os.Stderr, "notice: no dependency modules found — refusing to write an empty notice file")
		os.Exit(1)
	}

	buf := &strings.Builder{}
	if err := render(buf, artifact, mods, platforms); err != nil {
		fmt.Fprintln(os.Stderr, "notice:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, []byte(buf.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "notice:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "notice: %s — %d modules across %d platform(s)\n", *out, len(mods), max(len(platforms), 1))
}

// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/doc"
	"go/printer"
	"go/token"
	"http"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"template"
	"time"
)

// ----------------------------------------------------------------------------
// Globals

type delayTime struct {
	RWValue
}

func (dt *delayTime) backoff(max int) {
	dt.mutex.Lock()
	v := dt.value.(int) * 2
	if v > max {
		v = max
	}
	dt.value = v
	// don't change dt.timestamp - calling backoff indicates an error condition
	dt.mutex.Unlock()
}

var (
	verbose = flag.Bool("v", false, "verbose mode")

	// file system roots
	// TODO(gri) consider the invariant that goroot always end in '/'
	goroot      = flag.String("goroot", runtime.GOROOT(), "Go root directory")
	testDir     = flag.String("testdir", "", "Go root subdirectory - for testing only (faster startups)")
	pkgPath     = flag.String("path", "", "additional package directories (colon-separated)")
	filter      = flag.String("filter", "", "filter file containing permitted package directory paths")
	filterMin   = flag.Int("filter_minutes", 0, "filter file update interval in minutes; disabled if <= 0")
	filterDelay delayTime // actual filter update interval in minutes; usually filterDelay == filterMin, but filterDelay may back off exponentially

	// layout control
	tabwidth       = flag.Int("tabwidth", 4, "tab width")
	showTimestamps = flag.Bool("timestamps", true, "show timestamps with directory listings")
	templateDir    = flag.String("templates", "", "directory containing alternate template files")

	// search index
	indexEnabled = flag.Bool("index", false, "enable search index")
	maxResults   = flag.Int("maxresults", 10000, "maximum number of full text search results shown")

	// file system mapping
	fs         FileSystem      // the underlying file system for godoc
	fsHttp     http.FileSystem // the underlying file system for http
	fsMap      Mapping         // user-defined mapping
	fsTree     RWValue         // *Directory tree of packages, updated with each sync
	pathFilter RWValue         // filter used when building fsMap directory trees
	fsModified RWValue         // timestamp of last call to invalidateIndex

	// http handlers
	fileServer http.Handler // default file server
	cmdHandler httpHandler
	pkgHandler httpHandler
)

func initHandlers() {
	paths := filepath.SplitList(*pkgPath)
	for _, t := range build.Path {
		if t.Goroot {
			continue
		}
		paths = append(paths, t.SrcDir())
	}
	fsMap.Init(paths)

	fileServer = http.FileServer(fsHttp)
	cmdHandler = httpHandler{"/cmd/", filepath.Join(*goroot, "src", "cmd"), false}
	pkgHandler = httpHandler{"/pkg/", filepath.Join(*goroot, "src", "pkg"), true}
}

func registerPublicHandlers(mux *http.ServeMux) {
	mux.Handle(cmdHandler.pattern, &cmdHandler)
	mux.Handle(pkgHandler.pattern, &pkgHandler)
	mux.HandleFunc("/doc/codewalk/", codewalk)
	mux.HandleFunc("/search", search)
	mux.Handle("/robots.txt", fileServer)
	mux.HandleFunc("/", serveFile)
}

func initFSTree() {
	fsTree.set(newDirectory(filepath.Join(*goroot, *testDir), nil, -1))
	invalidateIndex()
}

// ----------------------------------------------------------------------------
// Directory filters

// isParentOf returns true if p is a parent of (or the same as) q
// where p and q are directory paths.
func isParentOf(p, q string) bool {
	n := len(p)
	return strings.HasPrefix(q, p) && (len(q) <= n || q[n] == '/')
}

func setPathFilter(list []string) {
	if len(list) == 0 {
		pathFilter.set(nil)
		return
	}

	// len(list) > 0
	pathFilter.set(func(path string) bool {
		// list is sorted in increasing order and for each path all its children are removed
		i := sort.Search(len(list), func(i int) bool { return list[i] > path })
		// Now we have list[i-1] <= path < list[i].
		// Path may be a child of list[i-1] or a parent of list[i].
		return i > 0 && isParentOf(list[i-1], path) || i < len(list) && isParentOf(path, list[i])
	})
}

func getPathFilter() func(string) bool {
	f, _ := pathFilter.get()
	if f != nil {
		return f.(func(string) bool)
	}
	return nil
}

// readDirList reads a file containing a newline-separated list
// of directory paths and returns the list of paths.
func readDirList(filename string) ([]string, os.Error) {
	contents, err := fs.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	// create a sorted list of valid directory names
	filter := func(path string) bool {
		d, e := fs.Lstat(path)
		if e != nil && err == nil {
			// remember first error and return it from readDirList
			// so we have at least some information if things go bad
			err = e
		}
		return e == nil && isPkgDir(d)
	}
	list := canonicalizePaths(strings.Split(string(contents), "\n"), filter)
	// for each parent path, remove all its children q
	// (requirement for binary search to work when filtering)
	i := 0
	for _, q := range list {
		if i == 0 || !isParentOf(list[i-1], q) {
			list[i] = q
			i++
		}
	}
	return list[0:i], err
}

// updateMappedDirs computes the directory tree for
// each user-defined file system mapping. If a filter
// is provided, it is used to filter directories.
//
func updateMappedDirs(filter func(string) bool) {
	if !fsMap.IsEmpty() {
		fsMap.Iterate(func(path string, value *RWValue) bool {
			value.set(newDirectory(path, filter, -1))
			return true
		})
		invalidateIndex()
	}
}

func updateFilterFile() {
	updateMappedDirs(nil) // no filter for accuracy

	// collect directory tree leaf node paths
	var buf bytes.Buffer
	fsMap.Iterate(func(_ string, value *RWValue) bool {
		v, _ := value.get()
		if v != nil && v.(*Directory) != nil {
			v.(*Directory).writeLeafs(&buf)
		}
		return true
	})

	// update filter file
	if err := writeFileAtomically(*filter, buf.Bytes()); err != nil {
		log.Printf("writeFileAtomically(%s): %s", *filter, err)
		filterDelay.backoff(24 * 60) // back off exponentially, but try at least once a day
	} else {
		filterDelay.set(*filterMin) // revert to regular filter update schedule
	}
}

func initDirTrees() {
	// setup initial path filter
	if *filter != "" {
		list, err := readDirList(*filter)
		if err != nil {
			log.Printf("readDirList(%s): %s", *filter, err)
		}
		if *verbose || len(list) == 0 {
			log.Printf("found %d directory paths in file %s", len(list), *filter)
		}
		setPathFilter(list)
	}

	go updateMappedDirs(getPathFilter()) // use filter for speed

	// start filter update goroutine, if enabled.
	if *filter != "" && *filterMin > 0 {
		filterDelay.set(*filterMin) // initial filter update delay
		go func() {
			for {
				if *verbose {
					log.Printf("start update of %s", *filter)
				}
				updateFilterFile()
				delay, _ := filterDelay.get()
				if *verbose {
					log.Printf("next filter update in %dmin", delay.(int))
				}
				time.Sleep(int64(delay.(int)) * 60e9)
			}
		}()
	}
}

// ----------------------------------------------------------------------------
// Path mapping

// Absolute paths are file system paths (backslash-separated on Windows),
// but relative paths are always slash-separated.

func absolutePath(relpath, defaultRoot string) string {
	abspath := fsMap.ToAbsolute(relpath)
	if abspath == "" {
		// no user-defined mapping found; use default mapping
		abspath = filepath.Join(defaultRoot, filepath.FromSlash(relpath))
	}
	return abspath
}

func relativeURL(abspath string) string {
	relpath := fsMap.ToRelative(abspath)
	if relpath == "" {
		// prefix must end in a path separator
		prefix := *goroot
		if len(prefix) > 0 && prefix[len(prefix)-1] != filepath.Separator {
			prefix += string(filepath.Separator)
		}
		if strings.HasPrefix(abspath, prefix) {
			// no user-defined mapping found; use default mapping
			relpath = filepath.ToSlash(abspath[len(prefix):])
		}
	}
	// Only if path is an invalid absolute path is relpath == ""
	// at this point. This should never happen since absolute paths
	// are only created via godoc for files that do exist. However,
	// it is ok to return ""; it will simply provide a link to the
	// top of the pkg or src directories.
	return relpath
}

// ----------------------------------------------------------------------------
// Tab conversion

var spaces = []byte("                                ") // 32 spaces seems like a good number

const (
	indenting = iota
	collecting
)

// A tconv is an io.Writer filter for converting leading tabs into spaces.
type tconv struct {
	output io.Writer
	state  int // indenting or collecting
	indent int // valid if state == indenting
}

func (p *tconv) writeIndent() (err os.Error) {
	i := p.indent
	for i >= len(spaces) {
		i -= len(spaces)
		if _, err = p.output.Write(spaces); err != nil {
			return
		}
	}
	// i < len(spaces)
	if i > 0 {
		_, err = p.output.Write(spaces[0:i])
	}
	return
}

func (p *tconv) Write(data []byte) (n int, err os.Error) {
	if len(data) == 0 {
		return
	}
	pos := 0 // valid if p.state == collecting
	var b byte
	for n, b = range data {
		switch p.state {
		case indenting:
			switch b {
			case '\t':
				p.indent += *tabwidth
			case '\n':
				p.indent = 0
				if _, err = p.output.Write(data[n : n+1]); err != nil {
					return
				}
			case ' ':
				p.indent++
			default:
				p.state = collecting
				pos = n
				if err = p.writeIndent(); err != nil {
					return
				}
			}
		case collecting:
			if b == '\n' {
				p.state = indenting
				p.indent = 0
				if _, err = p.output.Write(data[pos : n+1]); err != nil {
					return
				}
			}
		}
	}
	n = len(data)
	if pos < n && p.state == collecting {
		_, err = p.output.Write(data[pos:])
	}
	return
}

// ----------------------------------------------------------------------------
// Templates

// Write an AST node to w.
func writeNode(w io.Writer, fset *token.FileSet, x interface{}) {
	// convert trailing tabs into spaces using a tconv filter
	// to ensure a good outcome in most browsers (there may still
	// be tabs in comments and strings, but converting those into
	// the right number of spaces is much harder)
	//
	// TODO(gri) rethink printer flags - perhaps tconv can be eliminated
	//           with an another printer mode (which is more efficiently
	//           implemented in the printer than here with another layer)
	mode := printer.TabIndent | printer.UseSpaces
	(&printer.Config{mode, *tabwidth}).Fprint(&tconv{output: w}, fset, x)
}

func filenameFunc(path string) string {
	_, localname := filepath.Split(path)
	return localname
}

func fileInfoNameFunc(fi FileInfo) string {
	name := fi.Name()
	if fi.IsDirectory() {
		name += "/"
	}
	return name
}

func fileInfoTimeFunc(fi FileInfo) string {
	if t := fi.Mtime_ns(); t != 0 {
		return time.SecondsToLocalTime(t / 1e9).String()
	}
	return "" // don't return epoch if time is obviously not set
}

// The strings in infoKinds must be properly html-escaped.
var infoKinds = [nKinds]string{
	PackageClause: "package&nbsp;clause",
	ImportDecl:    "import&nbsp;decl",
	ConstDecl:     "const&nbsp;decl",
	TypeDecl:      "type&nbsp;decl",
	VarDecl:       "var&nbsp;decl",
	FuncDecl:      "func&nbsp;decl",
	MethodDecl:    "method&nbsp;decl",
	Use:           "use",
}

func infoKind_htmlFunc(kind SpotKind) string {
	return infoKinds[kind] // infoKind entries are html-escaped
}

func infoLineFunc(info SpotInfo) int {
	line := info.Lori()
	if info.IsIndex() {
		index, _ := searchIndex.get()
		if index != nil {
			line = index.(*Index).Snippet(line).Line
		} else {
			// no line information available because
			// we don't have an index - this should
			// never happen; be conservative and don't
			// crash
			line = 0
		}
	}
	return line
}

func infoSnippet_htmlFunc(info SpotInfo) string {
	if info.IsIndex() {
		index, _ := searchIndex.get()
		// Snippet.Text was HTML-escaped when it was generated
		return index.(*Index).Snippet(info.Lori()).Text
	}
	return `<span class="alert">no snippet text available</span>`
}

func nodeFunc(node interface{}, fset *token.FileSet) string {
	var buf bytes.Buffer
	writeNode(&buf, fset, node)
	return buf.String()
}

func node_htmlFunc(node interface{}, fset *token.FileSet) string {
	var buf1 bytes.Buffer
	writeNode(&buf1, fset, node)
	var buf2 bytes.Buffer
	FormatText(&buf2, buf1.Bytes(), -1, true, "", nil)
	return buf2.String()
}

func comment_htmlFunc(comment string) string {
	var buf bytes.Buffer
	// TODO(gri) Provide list of words (e.g. function parameters)
	//           to be emphasized by ToHTML.
	doc.ToHTML(&buf, []byte(comment), nil) // does html-escaping
	return buf.String()
}

func pkgLinkFunc(path string) string {
	relpath := relativeURL(path)
	// because of the irregular mapping under goroot
	// we need to correct certain relative paths
	if strings.HasPrefix(relpath, "src/pkg/") {
		relpath = relpath[len("src/pkg/"):]
	}
	return pkgHandler.pattern[1:] + relpath // remove trailing '/' for relative URL
}

func posLink_urlFunc(node ast.Node, fset *token.FileSet) string {
	var relpath string
	var line int
	var low, high int // selection

	if p := node.Pos(); p.IsValid() {
		pos := fset.Position(p)
		relpath = relativeURL(pos.Filename)
		line = pos.Line
		low = pos.Offset
	}
	if p := node.End(); p.IsValid() {
		high = fset.Position(p).Offset
	}

	var buf bytes.Buffer
	template.HTMLEscape(&buf, []byte(relpath))
	// selection ranges are of form "s=low:high"
	if low < high {
		fmt.Fprintf(&buf, "?s=%d:%d", low, high) // no need for URL escaping
		// if we have a selection, position the page
		// such that the selection is a bit below the top
		line -= 10
		if line < 1 {
			line = 1
		}
	}
	// line id's in html-printed source are of the
	// form "L%d" where %d stands for the line number
	if line > 0 {
		fmt.Fprintf(&buf, "#L%d", line) // no need for URL escaping
	}

	return buf.String()
}

// fmap describes the template functions installed with all godoc templates.
// Convention: template function names ending in "_html" or "_url" produce
//             HTML- or URL-escaped strings; all other function results may
//             require explicit escaping in the template.
var fmap = template.FuncMap{
	// various helpers
	"filename": filenameFunc,
	"repeat":   strings.Repeat,

	// accss to FileInfos (directory listings)
	"fileInfoName": fileInfoNameFunc,
	"fileInfoTime": fileInfoTimeFunc,

	// access to search result information
	"infoKind_html":    infoKind_htmlFunc,
	"infoLine":         infoLineFunc,
	"infoSnippet_html": infoSnippet_htmlFunc,

	// formatting of AST nodes
	"node":         nodeFunc,
	"node_html":    node_htmlFunc,
	"comment_html": comment_htmlFunc,

	// support for URL attributes
	"pkgLink":     pkgLinkFunc,
	"srcLink":     relativeURL,
	"posLink_url": posLink_urlFunc,
}

func readTemplate(name string) *template.Template {
	path := filepath.Join(*goroot, "lib", "godoc", name)
	if *templateDir != "" {
		defaultpath := path
		path = filepath.Join(*templateDir, name)
		if _, err := fs.Stat(path); err != nil {
			log.Print("readTemplate:", err)
			path = defaultpath
		}
	}
	return template.Must(template.New(name).Funcs(fmap).ParseFile(path))
}

var (
	codewalkHTML,
	codewalkdirHTML,
	dirlistHTML,
	errorHTML,
	godocHTML,
	packageHTML,
	packageText,
	searchHTML,
	searchText *template.Template
)

func readTemplates() {
	// have to delay until after flags processing since paths depend on goroot
	codewalkHTML = readTemplate("codewalk.html")
	codewalkdirHTML = readTemplate("codewalkdir.html")
	dirlistHTML = readTemplate("dirlist.html")
	errorHTML = readTemplate("error.html")
	godocHTML = readTemplate("godoc.html")
	packageHTML = readTemplate("package.html")
	packageText = readTemplate("package.txt")
	searchHTML = readTemplate("search.html")
	searchText = readTemplate("search.txt")
}

// ----------------------------------------------------------------------------
// Generic HTML wrapper

func servePage(w http.ResponseWriter, title, subtitle, query string, content []byte) {
	d := struct {
		Title     string
		Subtitle  string
		PkgRoots  []string
		SearchBox bool
		Query     string
		Version   string
		Menu      []byte
		Content   []byte
	}{
		title,
		subtitle,
		fsMap.PrefixList(),
		*indexEnabled,
		query,
		runtime.Version(),
		nil,
		content,
	}

	if err := godocHTML.Execute(w, &d); err != nil {
		log.Printf("godocHTML.Execute: %s", err)
	}
}

func serveText(w http.ResponseWriter, text []byte) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(text)
}

// ----------------------------------------------------------------------------
// Files

var (
	titleRx        = regexp.MustCompile(`<!-- title ([^\-]*)-->`)
	subtitleRx     = regexp.MustCompile(`<!-- subtitle ([^\-]*)-->`)
	firstCommentRx = regexp.MustCompile(`<!--([^\-]*)-->`)
)

func extractString(src []byte, rx *regexp.Regexp) (s string) {
	m := rx.FindSubmatch(src)
	if m != nil {
		s = strings.TrimSpace(string(m[1]))
	}
	return
}

func serveHTMLDoc(w http.ResponseWriter, r *http.Request, abspath, relpath string) {
	// get HTML body contents
	src, err := fs.ReadFile(abspath)
	if err != nil {
		log.Printf("ReadFile: %s", err)
		serveError(w, r, relpath, err)
		return
	}

	// if it begins with "<!DOCTYPE " assume it is standalone
	// html that doesn't need the template wrapping.
	if bytes.HasPrefix(src, []byte("<!DOCTYPE ")) {
		w.Write(src)
		return
	}

	// if it's the language spec, add tags to EBNF productions
	if strings.HasSuffix(abspath, "go_spec.html") {
		var buf bytes.Buffer
		linkify(&buf, src)
		src = buf.Bytes()
	}

	// get title and subtitle, if any
	title := extractString(src, titleRx)
	if title == "" {
		// no title found; try first comment for backward-compatibility
		title = extractString(src, firstCommentRx)
	}
	subtitle := extractString(src, subtitleRx)

	servePage(w, title, subtitle, "", src)
}

func applyTemplate(t *template.Template, name string, data interface{}) []byte {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		log.Printf("%s.Execute: %s", name, err)
	}
	return buf.Bytes()
}

func redirect(w http.ResponseWriter, r *http.Request) (redirected bool) {
	if canonical := path.Clean(r.URL.Path) + "/"; r.URL.Path != canonical {
		http.Redirect(w, r, canonical, http.StatusMovedPermanently)
		redirected = true
	}
	return
}

func serveTextFile(w http.ResponseWriter, r *http.Request, abspath, relpath, title string) {
	src, err := fs.ReadFile(abspath)
	if err != nil {
		log.Printf("ReadFile: %s", err)
		serveError(w, r, relpath, err)
		return
	}

	var buf bytes.Buffer
	buf.WriteString("<pre>")
	FormatText(&buf, src, 1, filepath.Ext(abspath) == ".go", r.FormValue("h"), rangeSelection(r.FormValue("s")))
	buf.WriteString("</pre>")

	servePage(w, title+" "+relpath, "", "", buf.Bytes())
}

func serveDirectory(w http.ResponseWriter, r *http.Request, abspath, relpath string) {
	if redirect(w, r) {
		return
	}

	list, err := fs.ReadDir(abspath)
	if err != nil {
		log.Printf("ReadDir: %s", err)
		serveError(w, r, relpath, err)
		return
	}

	contents := applyTemplate(dirlistHTML, "dirlistHTML", list)
	servePage(w, "Directory "+relpath, "", "", contents)
}

func serveFile(w http.ResponseWriter, r *http.Request) {
	relpath := r.URL.Path[1:] // serveFile URL paths start with '/'
	abspath := absolutePath(relpath, *goroot)

	// pick off special cases and hand the rest to the standard file server
	switch r.URL.Path {
	case "/":
		serveHTMLDoc(w, r, filepath.Join(*goroot, "doc", "root.html"), "doc/root.html")
		return

	case "/doc/root.html":
		// hide landing page from its real name
		http.Redirect(w, r, "/", http.StatusMovedPermanently)
		return
	}

	switch path.Ext(relpath) {
	case ".html":
		if strings.HasSuffix(relpath, "/index.html") {
			// We'll show index.html for the directory.
			// Use the dir/ version as canonical instead of dir/index.html.
			http.Redirect(w, r, r.URL.Path[0:len(r.URL.Path)-len("index.html")], http.StatusMovedPermanently)
			return
		}
		serveHTMLDoc(w, r, abspath, relpath)
		return

	case ".go":
		serveTextFile(w, r, abspath, relpath, "Source file")
		return
	}

	dir, err := fs.Lstat(abspath)
	if err != nil {
		log.Print(err)
		serveError(w, r, relpath, err)
		return
	}

	if dir != nil && dir.IsDirectory() {
		if redirect(w, r) {
			return
		}
		if index := filepath.Join(abspath, "index.html"); isTextFile(index) {
			serveHTMLDoc(w, r, index, relativeURL(index))
			return
		}
		serveDirectory(w, r, abspath, relpath)
		return
	}

	if isTextFile(abspath) {
		serveTextFile(w, r, abspath, relpath, "Text file")
		return
	}

	fileServer.ServeHTTP(w, r)
}

// ----------------------------------------------------------------------------
// Packages

// Fake package file and name for commands. Contains the command documentation.
const fakePkgFile = "doc.go"
const fakePkgName = "documentation"

// Fake relative package path for built-ins. Documentation for all globals
// (not just exported ones) will be shown for packages in this directory.
const builtinPkgPath = "builtin/"

type PageInfoMode uint

const (
	exportsOnly PageInfoMode = 1 << iota // only keep exported stuff
	genDoc                               // generate documentation
)

type PageInfo struct {
	Dirname string          // directory containing the package
	PList   []string        // list of package names found
	FSet    *token.FileSet  // corresponding file set
	PAst    *ast.File       // nil if no single AST with package exports
	PDoc    *doc.PackageDoc // nil if no single package documentation
	Dirs    *DirList        // nil if no directory information
	DirTime int64           // directory time stamp in seconds since epoch
	IsPkg   bool            // false if this is not documenting a real package
	Err     os.Error        // directory read error or nil
}

func (info *PageInfo) IsEmpty() bool {
	return info.Err != nil || info.PAst == nil && info.PDoc == nil && info.Dirs == nil
}

type httpHandler struct {
	pattern string // url pattern; e.g. "/pkg/"
	fsRoot  string // file system root to which the pattern is mapped
	isPkg   bool   // true if this handler serves real package documentation (as opposed to command documentation)
}

// getPageInfo returns the PageInfo for a package directory abspath. If the
// parameter genAST is set, an AST containing only the package exports is
// computed (PageInfo.PAst), otherwise package documentation (PageInfo.Doc)
// is extracted from the AST. If there is no corresponding package in the
// directory, PageInfo.PAst and PageInfo.PDoc are nil. If there are no sub-
// directories, PageInfo.Dirs is nil. If a directory read error occurred,
// PageInfo.Err is set to the respective error but the error is not logged.
//
func (h *httpHandler) getPageInfo(abspath, relpath, pkgname string, mode PageInfoMode) PageInfo {
	// filter function to select the desired .go files
	filter := func(d FileInfo) bool {
		// If we are looking at cmd documentation, only accept
		// the special fakePkgFile containing the documentation.
		return isPkgFile(d) && (h.isPkg || d.Name() == fakePkgFile)
	}

	// get package ASTs
	fset := token.NewFileSet()
	pkgs, err := parseDir(fset, abspath, filter)
	if err != nil && pkgs == nil {
		// only report directory read errors, ignore parse errors
		// (may be able to extract partial package information)
		return PageInfo{Dirname: abspath, Err: err}
	}

	// select package
	var pkg *ast.Package // selected package
	var plist []string   // list of other package (names), if any
	if len(pkgs) == 1 {
		// Exactly one package - select it.
		for _, p := range pkgs {
			pkg = p
		}

	} else if len(pkgs) > 1 {
		// Multiple packages - select the best matching package: The
		// 1st choice is the package with pkgname, the 2nd choice is
		// the package with dirname, and the 3rd choice is a package
		// that is not called "main" if there is exactly one such
		// package. Otherwise, don't select a package.
		dirpath, dirname := filepath.Split(abspath)

		// If the dirname is "go" we might be in a sub-directory for
		// .go files - use the outer directory name instead for better
		// results.
		if dirname == "go" {
			_, dirname = filepath.Split(filepath.Clean(dirpath))
		}

		var choice3 *ast.Package
	loop:
		for _, p := range pkgs {
			switch {
			case p.Name == pkgname:
				pkg = p
				break loop // 1st choice; we are done
			case p.Name == dirname:
				pkg = p // 2nd choice
			case p.Name != "main":
				choice3 = p
			}
		}
		if pkg == nil && len(pkgs) == 2 {
			pkg = choice3
		}

		// Compute the list of other packages
		// (excluding the selected package, if any).
		plist = make([]string, len(pkgs))
		i := 0
		for name := range pkgs {
			if pkg == nil || name != pkg.Name {
				plist[i] = name
				i++
			}
		}
		plist = plist[0:i]
	}

	// compute package documentation
	var past *ast.File
	var pdoc *doc.PackageDoc
	if pkg != nil {
		if mode&exportsOnly != 0 {
			ast.PackageExports(pkg)
		}
		if mode&genDoc != 0 {
			pdoc = doc.NewPackageDoc(pkg, path.Clean(relpath)) // no trailing '/' in importpath
		} else {
			past = ast.MergePackageFiles(pkg, ast.FilterUnassociatedComments)
		}
	}

	// get directory information
	var dir *Directory
	var timestamp int64
	if tree, ts := fsTree.get(); tree != nil && tree.(*Directory) != nil {
		// directory tree is present; lookup respective directory
		// (may still fail if the file system was updated and the
		// new directory tree has not yet been computed)
		dir = tree.(*Directory).lookup(abspath)
		timestamp = ts
	}
	if dir == nil {
		// the path may refer to a user-specified file system mapped
		// via fsMap; lookup that mapping and corresponding RWValue
		// if any
		var v *RWValue
		fsMap.Iterate(func(path string, value *RWValue) bool {
			if isParentOf(path, abspath) {
				// mapping found
				v = value
				return false
			}
			return true
		})
		if v != nil {
			// found a RWValue associated with a user-specified file
			// system; a non-nil RWValue stores a (possibly out-of-date)
			// directory tree for that file system
			if tree, ts := v.get(); tree != nil && tree.(*Directory) != nil {
				dir = tree.(*Directory).lookup(abspath)
				timestamp = ts
			}
		}
	}
	if dir == nil {
		// no directory tree present (too early after startup or
		// command-line mode); compute one level for this page
		// note: cannot use path filter here because in general
		//       it doesn't contain the fsTree path
		dir = newDirectory(abspath, nil, 1)
		timestamp = time.Seconds()
	}

	return PageInfo{abspath, plist, fset, past, pdoc, dir.listing(true), timestamp, h.isPkg, nil}
}

func (h *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if redirect(w, r) {
		return
	}

	relpath := r.URL.Path[len(h.pattern):]
	abspath := absolutePath(relpath, h.fsRoot)
	var mode PageInfoMode
	if relpath != builtinPkgPath {
		mode = exportsOnly
	}
	if r.FormValue("m") != "src" {
		mode |= genDoc
	}
	info := h.getPageInfo(abspath, relpath, r.FormValue("p"), mode)
	if info.Err != nil {
		log.Print(info.Err)
		serveError(w, r, relpath, info.Err)
		return
	}

	if r.FormValue("f") == "text" {
		contents := applyTemplate(packageText, "packageText", info)
		serveText(w, contents)
		return
	}

	var title, subtitle string
	switch {
	case info.PAst != nil:
		title = "Package " + info.PAst.Name.Name
	case info.PDoc != nil:
		switch {
		case info.IsPkg:
			title = "Package " + info.PDoc.PackageName
		case info.PDoc.PackageName == fakePkgName:
			// assume that the directory name is the command name
			_, pkgname := path.Split(path.Clean(relpath))
			title = "Command " + pkgname
		default:
			title = "Command " + info.PDoc.PackageName
		}
	default:
		title = "Directory " + relativeURL(info.Dirname)
		if *showTimestamps {
			subtitle = "Last update: " + time.SecondsToLocalTime(info.DirTime).String()
		}
	}

	contents := applyTemplate(packageHTML, "packageHTML", info)
	servePage(w, title, subtitle, "", contents)
}

// ----------------------------------------------------------------------------
// Search

var searchIndex RWValue

type SearchResult struct {
	Query string
	Alert string // error or warning message

	// identifier matches
	Hit *LookupResult // identifier matches of Query
	Alt *AltWords     // alternative identifiers to look for

	// textual matches
	Found    int         // number of textual occurrences found
	Textual  []FileLines // textual matches of Query
	Complete bool        // true if all textual occurrences of Query are reported
}

func lookup(query string) (result SearchResult) {
	result.Query = query

	index, timestamp := searchIndex.get()
	if index != nil {
		index := index.(*Index)

		// identifier search
		var err os.Error
		result.Hit, result.Alt, err = index.Lookup(query)
		if err != nil && *maxResults <= 0 {
			// ignore the error if full text search is enabled
			// since the query may be a valid regular expression
			result.Alert = "Error in query string: " + err.String()
			return
		}

		// full text search
		if *maxResults > 0 && query != "" {
			rx, err := regexp.Compile(query)
			if err != nil {
				result.Alert = "Error in query regular expression: " + err.String()
				return
			}
			// If we get maxResults+1 results we know that there are more than
			// maxResults results and thus the result may be incomplete (to be
			// precise, we should remove one result from the result set, but
			// nobody is going to count the results on the result page).
			result.Found, result.Textual = index.LookupRegexp(rx, *maxResults+1)
			result.Complete = result.Found <= *maxResults
			if !result.Complete {
				result.Found-- // since we looked for maxResults+1
			}
		}
	}

	// is the result accurate?
	if *indexEnabled {
		if _, ts := fsModified.get(); timestamp < ts {
			// The index is older than the latest file system change
			// under godoc's observation. Indexing may be in progress
			// or start shortly (see indexer()).
			result.Alert = "Indexing in progress: result may be inaccurate"
		}
	} else {
		result.Alert = "Search index disabled: no results available"
	}

	return
}

func search(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.FormValue("q"))
	result := lookup(query)

	if r.FormValue("f") == "text" {
		contents := applyTemplate(searchText, "searchText", result)
		serveText(w, contents)
		return
	}

	var title string
	if result.Hit != nil || len(result.Textual) > 0 {
		title = fmt.Sprintf(`Results for query %q`, query)
	} else {
		title = fmt.Sprintf(`No results found for query %q`, query)
	}

	contents := applyTemplate(searchHTML, "searchHTML", result)
	servePage(w, title, "", query, contents)
}

// ----------------------------------------------------------------------------
// Indexer

// invalidateIndex should be called whenever any of the file systems
// under godoc's observation change so that the indexer is kicked on.
//
func invalidateIndex() {
	fsModified.set(nil)
}

// indexUpToDate() returns true if the search index is not older
// than any of the file systems under godoc's observation.
//
func indexUpToDate() bool {
	_, fsTime := fsModified.get()
	_, siTime := searchIndex.get()
	return fsTime <= siTime
}

// feedDirnames feeds the directory names of all directories
// under the file system given by root to channel c.
//
func feedDirnames(root *RWValue, c chan<- string) {
	if dir, _ := root.get(); dir != nil {
		for d := range dir.(*Directory).iter(false) {
			c <- d.Path
		}
	}
}

// fsDirnames() returns a channel sending all directory names
// of all the file systems under godoc's observation.
//
func fsDirnames() <-chan string {
	c := make(chan string, 256) // asynchronous for fewer context switches
	go func() {
		feedDirnames(&fsTree, c)
		fsMap.Iterate(func(_ string, root *RWValue) bool {
			feedDirnames(root, c)
			return true
		})
		close(c)
	}()
	return c
}

func indexer() {
	for {
		if !indexUpToDate() {
			// index possibly out of date - make a new one
			if *verbose {
				log.Printf("updating index...")
			}
			start := time.Nanoseconds()
			index := NewIndex(fsDirnames(), *maxResults > 0)
			stop := time.Nanoseconds()
			searchIndex.set(index)
			if *verbose {
				secs := float64((stop-start)/1e6) / 1e3
				stats := index.Stats()
				log.Printf("index updated (%gs, %d bytes of source, %d files, %d lines, %d unique words, %d spots)",
					secs, stats.Bytes, stats.Files, stats.Lines, stats.Words, stats.Spots)
			}
			log.Printf("before GC: bytes = %d footprint = %d", runtime.MemStats.HeapAlloc, runtime.MemStats.Sys)
			runtime.GC()
			log.Printf("after  GC: bytes = %d footprint = %d", runtime.MemStats.HeapAlloc, runtime.MemStats.Sys)
		}
		var delay int64 = 60 * 1e9 // by default, try every 60s
		if *testDir != "" {
			// in test mode, try once a second for fast startup
			delay = 1 * 1e9
		}
		time.Sleep(delay)
	}
}

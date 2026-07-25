package rtk

import (
	"regexp"
	"strings"
)

// ── constants (mirror 9router open-sse/rtk/constants.js) ──
const (
	rawCap            = 10 * 1024 * 1024 // 10 MiB hard cap
	minCompressSize   = 500              // skip tiny blobs
	detectWindow      = 1024             // autodetect peek size
	gitDiffHunkMax    = 100              // per-hunk line cap
	gitDiffContext    = 3                // context lines around changes
	gitLogMaxLines    = 200
	dedupLineMax      = 2000
	grepPerFileMax    = 10
	findPerDirMax     = 10
	findTotalDirMax   = 20
	statusMaxFiles    = 10
	statusMaxUntracked = 10
	lsExtSummaryTop   = 5
	treeMaxLines      = 200
	searchListPerDir  = 10
	searchListTotalDir = 20
	smartHead         = 120
	smartTail         = 60
	smartMinLines     = 250
	readNumberedRatio = 0.7
)

// filter is one compressor.
type filter struct {
	name  string
	apply func(string) string
}

// ── regexes ──
var (
	reGitLog     = regexp.MustCompile(`(?m)^[*|/\\ ]*commit [0-9a-f]{7,40}$`)
	reGitDiff    = regexp.MustCompile(`(?m)^diff --git `)
	reGitDiffHunk = regexp.MustCompile(`(?m)^@@ `)
	reGitStatus  = regexp.MustCompile(`(?m)^(On branch |nothing to commit|Changes (not |to be )|Untracked files:)`)
	rePorcelain  = regexp.MustCompile(`^[ MADRCU?!][ MADRCU?!] \S`)
	reBuildOut   = regexp.MustCompile(`(?im)^(npm (warn|error|ERR!)|yarn (warn|error)|\s*Compiling\s+\S+|\s*Downloading\s+\S+|added \d+ package|\[ERROR\]|BUILD (SUCCESS|FAILED)|\s*Finished\s+|Successfully (installed|built)|ERROR:)`)
	reTreeGlyph  = regexp.MustCompile(`[├└]──|│  `)
	reLsRow      = regexp.MustCompile(`(?m)^[-dlbcps][rwx-]{9}`)
	reLsTotal    = regexp.MustCompile(`(?m)^total \d+$`)
	reSearchHdr  = regexp.MustCompile(`(?m)^(Result of search|Found \d+ files|Search results|Files found|(\d+ (files|results)))`)
	reReadNumbered = regexp.MustCompile(`^\s*\d+\|`)
	reLsDate     = regexp.MustCompile(`\s+(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\s+\d{1,2}\s+(\d{4}|\d{2}:\d{2})\s+`)
	lsNoiseDirs  = map[string]bool{
		"node_modules": true, ".git": true, "target": true, "__pycache__": true,
		".next": true, "dist": true, "build": true, ".cache": true, ".turbo": true,
		".vercel": true, ".pytest_cache": true, ".mypy_cache": true, ".tox": true,
		".venv": true, "venv": true, "env": true, "coverage": true, ".nyc_output": true,
		".DS_Store": true, "Thumbs.db": true, ".idea": true, ".vscode": true,
		".vs": true, "*.egg-info": true, ".eggs": true,
	}
)

// autoDetect picks a filter by inspecting the head of the text.
func autoDetect(text string) *filter {
	head := text
	if len(head) > detectWindow {
		head = head[:detectWindow]
	}
	switch {
	case reGitLog.MatchString(head):
		return &filter{"git-log", gitLog}
	case reGitDiff.MatchString(head) || reGitDiffHunk.MatchString(head):
		return &filter{"git-diff", gitDiff}
	case reGitStatus.MatchString(head):
		return &filter{"git-status", gitStatus}
	case reBuildOut.MatchString(head):
		return &filter{"build-output", buildOutput}
	case isMostlyPorcelain(head):
		return &filter{"git-status", gitStatus}
	}
	lines := strings.Split(head, "\n")
	nonEmpty := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			nonEmpty = append(nonEmpty, l)
		}
	}
	if len(nonEmpty) >= 5 {
		first5 := nonEmpty[:min(5, len(nonEmpty))]
		for _, l := range first5 {
			if isGrepLine(l) {
				return &filter{"grep", grepFilter}
			}
		}
	}
	if len(nonEmpty) >= 3 {
		all := true
		for _, l := range nonEmpty {
			if !isPathLike(l) {
				all = false
				break
			}
		}
		if all {
			return &filter{"find", findFilter}
		}
	}
	if reTreeGlyph.MatchString(head) {
		return &filter{"tree", treeFilter}
	}
	lsRows := 0
	for _, l := range strings.Split(head, "\n") {
		if reLsRow.MatchString(l) {
			lsRows++
		}
	}
	if reLsTotal.MatchString(head) || lsRows >= 3 {
		return &filter{"ls", lsFilter}
	}
	if reSearchHdr.MatchString(head) {
		return &filter{"search-list", searchListFilter}
	}
	if len(lines) >= smartMinLines && isLineNumbered(lines) {
		return &filter{"read-numbered", readNumberedFilter}
	}
	if len(nonEmpty) >= 5 {
		return &filter{"dedup-log", dedupLog}
	}
	if len(strings.Split(text, "\n")) >= smartMinLines {
		return &filter{"smart-truncate", smartTruncate}
	}
	return nil
}

func isGrepLine(line string) bool {
	first := strings.Index(line, ":")
	if first == -1 {
		return false
	}
	rest := line[first+1:]
	second := strings.Index(rest, ":")
	if second == -1 {
		return false
	}
	lineno := rest[:second]
	return isAllDigits(lineno)
}

func isPathLike(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	if regexp.MustCompile(`^[A-Za-z]:[\\/]`).MatchString(t) {
		return true
	}
	if strings.Contains(t, ":") {
		return false
	}
	if strings.HasPrefix(t, ".") || strings.HasPrefix(t, "/") || strings.Contains(t, "/") {
		return true
	}
	return false
}

func isMostlyPorcelain(head string) bool {
	lines := strings.Split(head, "\n")
	var nonEmpty, hits int
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		nonEmpty++
		if rePorcelain.MatchString(l) {
			hits++
		}
	}
	if nonEmpty < 3 {
		return false
	}
	return float64(hits)/float64(nonEmpty) >= 0.6
}

func isLineNumbered(lines []string) bool {
	var nonEmpty, hits int
	sample := lines
	if len(sample) > 100 {
		sample = sample[:100]
	}
	for _, l := range sample {
		if l == "" {
			continue
		}
		nonEmpty++
		if reReadNumbered.MatchString(l) {
			hits++
		}
	}
	if nonEmpty < 5 {
		return false
	}
	return float64(hits)/float64(nonEmpty) >= readNumberedRatio
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ───────────────────────────────────────────────────────────────────────────
// Filters (ported from 9router open-sse/rtk/filters/*)
// ───────────────────────────────────────────────────────────────────────────

// gitStatus — collapse porcelain status to compact change list.
func gitStatus(s string) string {
	lines := strings.Split(s, "\n")
	var staged, modified, untracked []string
	seenUntracked := false
	for _, l := range lines {
		if strings.HasPrefix(l, "Untracked files:") {
			seenUntracked = true
			continue
		}
		if seenUntracked && strings.TrimSpace(l) != "" {
			if len(untracked) < statusMaxUntracked {
				untracked = append(untracked, strings.TrimSpace(l))
			}
			continue
		}
		if len(l) < 4 {
			continue
		}
		x, y := l[0], l[1]
		if x == '?' && y == '?' {
			if len(untracked) < statusMaxUntracked {
				untracked = append(untracked, strings.TrimSpace(l[3:]))
			}
			continue
		}
		if x != ' ' && x != '?' {
			if len(staged) < statusMaxFiles {
				staged = append(staged, strings.TrimSpace(l[3:]))
			}
		} else if y != ' ' && y != '?' {
			if len(modified) < statusMaxFiles {
				modified = append(modified, strings.TrimSpace(l[3:]))
			}
		}
	}
	var b strings.Builder
	b.WriteString("git status (compressed):\n")
	writeList(&b, "Staged", staged)
	writeList(&b, "Modified", modified)
	writeList(&b, "Untracked", untracked)
	if b.Len() == len("git status (compressed):\n") {
		return s
	}
	return b.String()
}

func writeList(b *strings.Builder, label string, items []string) {
	if len(items) == 0 {
		return
	}
	b.WriteString("  " + label + ": ")
	b.WriteString(strings.Join(items, ", "))
	b.WriteString("\n")
}

// gitDiff — keep +/- lines + small context, cap hunk size.
func gitDiff(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	var pendingCtx []string
	changed := 0
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "diff --git "), strings.HasPrefix(l, "index "),
			strings.HasPrefix(l, "--- "), strings.HasPrefix(l, "+++ "),
			strings.HasPrefix(l, "@@ "):
			if len(out) > gitDiffHunkMax {
				out = append(out, "...(truncated)...")
				return strings.Join(out, "\n")
			}
			out = append(out, l)
			pendingCtx = nil
		case strings.HasPrefix(l, "+"), strings.HasPrefix(l, "-"):
			out = append(out, pendingCtx...)
			pendingCtx = nil
			out = append(out, l)
			changed++
			if changed >= gitDiffHunkMax {
				out = append(out, "...(truncated)...")
				return strings.Join(out, "\n")
			}
		default:
			if len(pendingCtx) < gitDiffContext {
				pendingCtx = append(pendingCtx, l)
			}
		}
	}
	return strings.Join(out, "\n")
}

// gitLog — keep short hash + subject, cap lines.
func gitLog(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, gitLogMaxLines)
	for _, l := range lines {
		if m := reGitLog.FindString(l); m != "" {
			if len(out) >= gitLogMaxLines {
				out = append(out, "...(truncated)...")
				break
			}
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return s
	}
	return strings.Join(out, "\n")
}

// buildOutput — keep first/last error lines + summary.
func buildOutput(s string) string {
	lines := strings.Split(s, "\n")
	var errs, warns []string
	for _, l := range lines {
		if reBuildOut.MatchString(l) {
			if strings.Contains(strings.ToLower(l), "error") {
				if len(errs) < 20 {
					errs = append(errs, l)
				}
			} else if len(warns) < 10 {
				warns = append(warns, l)
			}
		}
	}
	if len(errs) == 0 && len(warns) == 0 {
		return s
	}
	var b strings.Builder
	for _, e := range errs {
		b.WriteString(e + "\n")
	}
	for _, w := range warns {
		b.WriteString(w + "\n")
	}
	b.WriteString("...(build output compressed)")
	return b.String()
}

// grep — keep file + count, cap matches per file.
func grepFilter(s string) string {
	byFile := map[string][]string{}
	order := []string{}
	for _, l := range strings.Split(s, "\n") {
		first := strings.Index(l, ":")
		if first == -1 {
			continue
		}
		file := l[:first]
		rest := l[first+1:]
		if strings.Index(rest, ":") == -1 {
			continue
		}
		if _, ok := byFile[file]; !ok {
			order = append(order, file)
		}
		if len(byFile[file]) < grepPerFileMax {
			byFile[file] = append(byFile[file], l)
		}
	}
	if len(order) == 0 {
		return s
	}
	var b strings.Builder
	for _, f := range order {
		matches := byFile[f]
		for _, m := range matches {
			b.WriteString(m + "\n")
		}
		if len(matches) >= grepPerFileMax {
			b.WriteString("  ...(+" + itoa(len(byFile[f])-grepPerFileMax) + " more in " + f + ")\n")
		}
	}
	return b.String()
}

// find — keep dirs + top-N files per dir.
func findFilter(s string) string {
	dirs := map[string][]string{}
	order := []string{}
	for _, l := range strings.Split(s, "\n") {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		idx := strings.LastIndex(t, "/")
		var dir, name string
		if idx < 0 {
			dir, name = ".", t
		} else {
			dir, name = t[:idx], t[idx+1:]
		}
		if _, ok := dirs[dir]; !ok {
			order = append(order, dir)
		}
		if len(dirs[dir]) < findPerDirMax {
			dirs[dir] = append(dirs[dir], name)
		}
	}
	if len(order) == 0 {
		return s
	}
	if len(order) > findTotalDirMax {
		order = order[:findTotalDirMax]
	}
	var b strings.Builder
	for _, d := range order {
		files := dirs[d]
		if len(files) > findPerDirMax {
			files = files[:findPerDirMax]
		}
		b.WriteString(d + "/: " + strings.Join(files, " ") + "\n")
	}
	b.WriteString("...(find output compressed)")
	return b.String()
}

// tree — collapse, cap lines.
func treeFilter(s string) string {
	lines := strings.Split(s, "\n")
	kept := make([]string, 0, treeMaxLines)
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			kept = append(kept, l)
		}
		if len(kept) >= treeMaxLines {
			kept = append(kept, "...(truncated)")
			break
		}
	}
	return strings.Join(kept, "\n")
}

// ls — compact ls -la to name + size + ext summary.
func lsFilter(s string) string {
	var dirs []string
	files := [][2]string{}
	byExt := map[string]int{}
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "total ") || strings.TrimSpace(l) == "" {
			continue
		}
		m := reLsDate.FindStringIndex(l)
		if m == nil {
			continue
		}
		name := strings.TrimSpace(l[m[1]:])
		before := strings.Fields(l[:m[0]])
		if len(before) < 4 {
			continue
		}
		perms := before[0]
		if name == "." || name == ".." {
			continue
		}
		if lsNoiseDirs[name] {
			continue
		}
		ft := permType(perms)
		if ft == "d" {
			dirs = append(dirs, name)
		} else if ft == "-" || ft == "l" {
			dot := strings.LastIndex(name, ".")
			ext := "no ext"
			if dot > 0 {
				ext = name[dot:]
			}
			byExt[ext]++
			files = append(files, [2]string{name, humanSize(parseSize(before))})
		}
	}
	if len(dirs) == 0 && len(files) == 0 {
		return s
	}
	var b strings.Builder
	for _, d := range dirs {
		b.WriteString(d + "/\n")
	}
	for _, f := range files {
		b.WriteString(f[0] + "  " + f[1] + "\n")
	}
	b.WriteString("Summary: " + itoa(len(files)) + " files, " + itoa(len(dirs)) + " dirs")
	if len(byExt) > 0 {
		type kv struct {
			k string
			v int
		}
		exts := make([]kv, 0, len(byExt))
		for k, v := range byExt {
			exts = append(exts, kv{k, v})
		}
		for i := 1; i < len(exts); i++ {
			for j := i; j > 0 && exts[j].v > exts[j-1].v; j-- {
				exts[j], exts[j-1] = exts[j-1], exts[j]
			}
		}
		top := exts
		if len(top) > lsExtSummaryTop {
			top = top[:lsExtSummaryTop]
		}
		parts := make([]string, 0, len(top))
		for _, e := range top {
			parts = append(parts, itoa(e.v)+" "+e.k)
		}
		b.WriteString(" (" + strings.Join(parts, ", "))
		if len(exts) > lsExtSummaryTop {
			b.WriteString(", +" + itoa(len(exts)-lsExtSummaryTop) + " more")
		}
		b.WriteString(")")
	}
	return b.String()
}

func permType(perms string) string {
	if len(perms) == 0 {
		return ""
	}
	return string(perms[0])
}

func parseSize(parts []string) int {
	for i := len(parts) - 1; i >= 0; i-- {
		if n, ok := atoiSafe(parts[i]); ok {
			return n
		}
	}
	return 0
}

func humanSize(b int) string {
	switch {
	case b >= 1<<20:
		return ftoa(float64(b)/float64(1<<20)) + "M"
	case b >= 1<<10:
		return ftoa(float64(b)/float64(1<<10)) + "K"
	default:
		return itoa(b) + "B"
	}
}

func atoiSafe(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// searchList — Cursor Glob style result list.
func searchListFilter(s string) string {
	lines := strings.Split(s, "\n")
	dirs := map[string][]string{}
	order := []string{}
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || reSearchHdr.MatchString(l) {
			continue
		}
		idx := strings.LastIndex(t, "/")
		var dir, name string
		if idx < 0 {
			dir, name = ".", t
		} else {
			dir, name = t[:idx], t[idx+1:]
		}
		if _, ok := dirs[dir]; !ok {
			order = append(order, dir)
		}
		if len(dirs[dir]) < searchListPerDir {
			dirs[dir] = append(dirs[dir], name)
		}
	}
	if len(order) == 0 {
		return s
	}
	if len(order) > searchListTotalDir {
		order = order[:searchListTotalDir]
	}
	var b strings.Builder
	for _, d := range order {
		files := dirs[d]
		if len(files) > searchListPerDir {
			files = files[:searchListPerDir]
		}
		b.WriteString(d + "/: " + strings.Join(files, " ") + "\n")
	}
	b.WriteString("...(search list compressed)")
	return b.String()
}

// readNumbered — "  N|content" dumps: keep head + tail.
func readNumberedFilter(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) < smartMinLines {
		return s
	}
	if len(lines) <= smartHead+smartTail {
		return s
	}
	head := lines[:smartHead]
	tail := lines[len(lines)-smartTail:]
	var b strings.Builder
	b.WriteString(strings.Join(head, "\n"))
	b.WriteString("\n...(RTK: " + itoa(len(lines)-smartHead-smartTail) + " lines omitted)...\n")
	b.WriteString(strings.Join(tail, "\n"))
	return b.String()
}

// dedupLog — collapse repeated lines.
func dedupLog(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) > dedupLineMax {
		lines = lines[:dedupLineMax]
	}
	seen := map[string]int{}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		seen[l]++
	}
	// Emit unique lines; for repeats, emit once with a count.
	emitted := map[string]bool{}
	for _, l := range lines {
		if emitted[l] {
			continue
		}
		emitted[l] = true
		if c := seen[l]; c > 1 {
			out = append(out, l+" (x"+itoa(c)+")")
		} else {
			out = append(out, l)
		}
	}
	if len(out) >= len(lines) {
		return s
	}
	return strings.Join(out, "\n")
}

// smartTruncate — keep head + tail of a big blob.
func smartTruncate(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) < smartMinLines {
		return s
	}
	if len(lines) <= smartHead+smartTail {
		return s
	}
	head := strings.Join(lines[:smartHead], "\n")
	tail := strings.Join(lines[len(lines)-smartTail:], "\n")
	return head + "\n...(RTK: " + itoa(len(lines)-smartHead-smartTail) + " lines truncated)...\n" + tail
}

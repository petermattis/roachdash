// Copyright 2015 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Godash generates Go dashboards about issues and CLs.
//
// Usage:
//
//	godash [-cl] [-html] [-rcache] [-wcache]
//
// By default, godash prints a textual release dashboard to standard output.
// The release dashboard shows all open issues in the milestones for the upcoming
// release (currently Go 1.5), along with all open CLs mentioning those issues,
// and all other open CLs working in the main Go repository.
//
// If the -cl flag is specified, godash instead prints a CL dashboard, showing all
// open CLs, along with information about review status and review latency.
//
// If the -html flag is specified, godash prints HTML instead of text.
//
// Godash expects to find golang.org/x/build/cmd/cl and rsc.io/github/issue
// on its $PATH, to read data from Gerrit and GitHub.
// If the -wcache flag is specified, godash writes gathered data to $HOME/.godash-cache.
// If the -rcache flag is specified, godash reads data from $HOME/.godash-cache
// instead of Gerrit and GitHub. These flags are useful to avoid network delays and
// ensure consistency when generating multiple forms of dashboard; they are also
// useful when adjusting the output code.
//
// https://swtch.com/godash is periodically updated with the HTML versions of
// the two dashboards.
//
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

const Release = ""

type CL struct {
	Number             int
	Subject            string
	Project            string
	Author             string
	Reviewer           string
	ReviewerEmail      string
	NeedsReview        bool
	NeedsReviewChanged time.Time
	Start              time.Time
	Issues             []int
	Closed             bool
	Scores             map[string]int
	Files              []string
}

type Issue struct {
	Number    int
	Title     string
	Labels    []string
	Assignee  string
	Milestone string
}

type Group struct {
	Dir   string
	Items []*Item
}

type Item struct {
	Issue *Issue
	CLs   []*CL
}

var (
	cls       []*CL
	issues    []*Issue
	maybe     []*Issue
	groups    []*Group
	assignees []*Item
	output    bytes.Buffer
	skipCL    int

	now = time.Now()

	flagHTML = flag.Bool("html", false, "print HTML output")

	cache      = map[string]string{}
	cacheFile  = os.Getenv("HOME") + "/.roachdash-cache"
	readCache  = flag.Bool("rcache", false, "read from cached copy of data")
	writeCache = flag.Bool("wcache", false, "write cached copy of data")
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("godash: ")
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
	}
	fetchData()
	groupData()
	what := "release"
	fmt.Fprintf(&output, "CockroachDB %s dashboard\n", what)
	fmt.Fprintf(&output, "%v\n\n", time.Now().UTC().Format(time.UnixDate))
	fmt.Fprintf(&output, "%d %s issues\n", len(issues)-len(maybe), Release)
	printGroups()
	if *flagHTML {
		printHTML()
		return
	}
	os.Stdout.Write(output.Bytes())
}

func printHTML() {
	data := html.EscapeString(output.String())
	i := strings.Index(data, "\n")
	if i < 0 {
		i = len(data)
	}
	fmt.Printf("<html>\n")
	fmt.Printf("<title>%s</title>\n", data[:i])
	fmt.Printf("<style>\n")
	fmt.Printf(".maybe {color: #777}\n")
	fmt.Printf(".late {color: #700; text-decoration: underline;}\n")
	fmt.Printf("</style>\n")
	fmt.Printf("<pre>\n")
	// data = regexp.MustCompile(`(?m)^HOWTO`).ReplaceAllString(data, `<a target="_blank" href="index.html">about the dashboard</a>`)
	// data = regexp.MustCompile(`(CL (\d+))\b`).ReplaceAllString(data, "<a target=\"_blank\" href='https://golang.org/cl/$2'>$1</a>")
	data = regexp.MustCompile(`(#(\d+))\s`).ReplaceAllString(data, "<a target=\"_blank\" href='https://github.com/cockroachdb/cockroach/issues/$2'>$1</a>")
	data = regexp.MustCompile(`(?m)^([\?A-Za-z0-9][^\n]*)`).ReplaceAllString(data, "<b>$1</b>")
	data = regexp.MustCompile(`(?m)^    ([\?A-Za-z0-9][^\n]*)`).ReplaceAllString(data, "    <b>$1</b>")
	data = regexp.MustCompile(`(?m)^([^\n]*\[maybe[^\n]*)`).ReplaceAllString(data, "<span class='maybe'>$1</span>")
	data = regexp.MustCompile(`(?m)^( +)(.*)( → )(.*)(, [\d/]+ days)(, waiting for reviewer)`).ReplaceAllString(data, "$1$2$3<b>$4</b>$5$6")
	data = regexp.MustCompile(`(?m)^( +)(.*)( → )(.*)(, [\d/]+ days)(, waiting for author)`).ReplaceAllString(data, "$1<b>$2</b>$3$4$5$6")
	data = regexp.MustCompile(`(→ )(.*, \d\d+)(/\d+ days)(, waiting for reviewer)`).ReplaceAllString(data, "$1<b class='late'>$2</b>$3$4")
	fmt.Printf("%s\n", data)

	fmt.Printf("</pre>\n")
	fmt.Printf("</html>\n")
}

func fetchData() {
	if *readCache {
		data, err := ioutil.ReadFile(cacheFile)
		if err != nil {
			log.Fatal(err)
		}
		if err := json.Unmarshal(data, &cache); err != nil {
			log.Fatal("loading cache: %v", err)
		}
	}

	// readJSON(&cls, "CLs", "cl", "-json")
	// var open []*CL
	// for _, cl := range cls {
	// 	if !cl.Closed {
	// 		open = append(open, cl)
	// 	}
	// }
	// cls = open

	readJSON(&issues, "issues", "issue", "-json", "")
	// readJSON(&issues, Release+" issues", "issue", "-json", "milestone:"+Release)
	// readJSON(&maybe, Release+"Maybe issues", "issue", "-json", "milestone:"+Release+"Maybe")
	issues = append(issues, maybe...)

	if *writeCache {
		flushCache()
	}
}

func flushCache() {
	data, err := json.Marshal(cache)
	if err != nil {
		log.Fatal("marshaling cache: %v", err)
	}
	if err := ioutil.WriteFile(cacheFile, data, 0666); err != nil {
		log.Fatal("writing cache: %v", err)
	}
}

func readJSON(dst interface{}, desc string, cmd ...string) {
	var data []byte
	if *readCache {
		data = []byte(cache[desc])
		if len(data) == 0 {
			log.Fatalf("%s not cached", desc)
		}
	} else {
		var err error
		data, err = exec.Command(cmd[0], cmd[1:]...).CombinedOutput()
		if err != nil {
			log.Fatalf("fetching %s: %v\n%s", desc, err, data)
		}
	}
	if *writeCache {
		cache[desc] = string(data)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		log.Fatalf("parsing %s: %v", desc, err)
	}
}

func groupData() {
	groupsByDir := make(map[string]*Group)
	addGroup := func(item *Item) {
		dir := item.Dir()
		g := groupsByDir[dirKey(dir)]
		if g == nil {
			g = &Group{Dir: dir}
			groupsByDir[dirKey(dir)] = g
		}
		g.Items = append(g.Items, item)
	}
	itemsByBug := map[int]*Item{}

	for _, issue := range issues {
		item := &Item{Issue: issue}
		addGroup(item)
		itemsByBug[issue.Number] = item
		assignees = append(assignees, item)
	}

	for _, cl := range cls {
		found := false
		for _, id := range cl.Issues {
			item := itemsByBug[id]
			if item != nil {
				found = true
				item.CLs = append(item.CLs, cl)
			}
		}
		if !found {
			if cl.Project == "go" {
				item := &Item{CLs: []*CL{cl}}
				addGroup(item)
			} else {
				skipCL++
			}
		}
	}

	var keys []string
	for key, g := range groupsByDir {
		sort.Sort(itemsBySummary(g.Items))
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		groups = append(groups, groupsByDir[key])
	}
	sort.Sort(itemsBySummary(assignees))
}

func printItems(indent int, items []*Item) {
	var lastAssignee *string
	for i, item := range items {
		if assignee := itemAssignee(item); lastAssignee == nil || *lastAssignee != assignee {
			if i > 0 {
				fmt.Fprintf(&output, "\n")
			}
			lastAssignee = new(string)
			*lastAssignee = assignee
			if assignee == "" {
				assignee = "unassigned"
			}
			fmt.Fprintf(&output, "%s%s\n", strings.Repeat("    ", indent), assignee)
		}

		prefix := ""
		if item.Issue != nil {
			fmt.Fprintf(&output, "%s%-10s  [%s] %s", strings.Repeat("    ", indent+1),
				fmt.Sprintf("#%d", item.Issue.Number), item.Issue.Milestone, item.Issue.Title)
			prefix = "\u2937 "
			var tags []string
			if strings.HasSuffix(item.Issue.Milestone, "Maybe") {
				tags = append(tags, "maybe")
			}
			sort.Strings(item.Issue.Labels)
			for _, label := range item.Issue.Labels {
				switch label {
				case "documentation":
					tags = append(tags, "doc")
				case "testing":
					tags = append(tags, "test")
				case "started":
					tags = append(tags, strings.ToLower(label))
				}
			}
			if len(tags) > 0 {
				fmt.Fprintf(&output, " [%s]", strings.Join(tags, ", "))
			}
			fmt.Fprintf(&output, "\n")
		}
		for _, cl := range item.CLs {
			fmt.Fprintf(&output, "%s%-10s  %s%s\n", strings.Repeat("    ", indent),
				fmt.Sprintf("%sCL %d", prefix, cl.Number), prefix, cl.Subject)
		}
	}
}

func printGroups() {
	for _, g := range groups {
		fmt.Fprintf(&output, "\n%s\n", g.Dir)
		printItems(1, g.Items)
	}

	fmt.Fprintf(&output, "\n\n\nBy assignee\n\n")
	printItems(1, assignees)
}

var okDesc = map[string]bool{
	"all":   true,
	"build": true,
}

func (item *Item) Dir() string {
	for _, cl := range item.CLs {
		dirs := cl.Dirs()
		desc := titleDir(cl.Subject)

		// Accept description if it is a global prefix like "all".
		if okDesc[desc] {
			return desc
		}

		// Accept description if it matches one of the directories.
		for _, dir := range dirs {
			if dir == desc {
				return dir
			}
		}

		// Otherwise use most common directory.
		if len(dirs) > 0 {
			return dirs[0]
		}

		// Otherwise accept description.
		return desc
	}
	if item.Issue != nil {
		if dir := titleDir(item.Issue.Title); dir != "" {
			return dir
		}
		return "?"
	}
	return "?"
}

func titleDir(title string) string {
	if i := strings.Index(title, "\n"); i >= 0 {
		title = title[:i]
	}
	title = strings.TrimSpace(title)
	i := strings.Index(title, ":")
	if i < 0 {
		return ""
	}
	title = title[:i]
	if i := strings.Index(title, ","); i >= 0 {
		title = strings.TrimSpace(title[:i])
	}
	if strings.Contains(title, " ") {
		return ""
	}
	return title
}

// Dirs returns the list of directories that this CL might be said to be about,
// in preference order.
func (cl *CL) Dirs() []string {
	prefix := ""
	if cl.Project != "go" {
		prefix = "x/" + cl.Project + "/"
	}
	counts := map[string]int{}
	for _, file := range cl.Files {
		name := file
		i := strings.LastIndex(name, "/")
		if i >= 0 {
			name = name[:i]
		} else {
			name = ""
		}
		name = strings.TrimPrefix(name, "src/")
		if name == "src" {
			name = ""
		}
		name = prefix + name
		if name == "" {
			name = "build"
		}
		counts[name]++
	}

	if _, ok := counts["test"]; ok {
		counts["test"] -= 10000 // do not pick as most frequent
	}

	var dirs dirCounts
	for name, count := range counts {
		dirs = append(dirs, dirCount{name, count})
	}
	sort.Sort(dirs)

	var names []string
	for _, d := range dirs {
		names = append(names, d.name)
	}
	return names
}

type dirCount struct {
	name  string
	count int
}

type dirCounts []dirCount

func (x dirCounts) Len() int      { return len(x) }
func (x dirCounts) Swap(i, j int) { x[i], x[j] = x[j], x[i] }
func (x dirCounts) Less(i, j int) bool {
	if x[i].count != x[j].count {
		return x[i].count > x[j].count
	}
	return x[i].name < x[j].name
}

type itemsBySummary []*Item

func (x itemsBySummary) Len() int      { return len(x) }
func (x itemsBySummary) Swap(i, j int) { x[i], x[j] = x[j], x[i] }
func (x itemsBySummary) Less(i, j int) bool {
	ia := itemAssignee(x[i])
	ja := itemAssignee(x[j])
	switch {
	case ia < ja:
		return true
	case ia > ja:
		return false
	default:
		return itemSummary(x[i]) < itemSummary(x[j])
	}
}

func itemAssignee(it *Item) string {
	if it.Issue == nil {
		return ""
	}
	return it.Issue.Assignee
}

func itemSummary(it *Item) string {
	if it.Issue != nil {
		return it.Issue.Title
	}
	for _, cl := range it.CLs {
		return cl.Subject
	}
	return ""
}

func dirKey(s string) string {
	if strings.Contains(s, ".") {
		return "\x7F" + s
	}
	return s
}

func (cl *CL) Status() string {
	var buf bytes.Buffer
	who := "author"
	if cl.NeedsReview {
		who = "reviewer"
	}
	rev := cl.Reviewer
	if rev == "" {
		rev = "???"
	}
	score := ""
	if x := cl.Scores[cl.ReviewerEmail]; x != 0 {
		score = fmt.Sprintf("%+d", x)
	}
	fmt.Fprintf(&buf, "%s → %s%s, %d/%d days, waiting for %s", cl.Author, rev, score, int(now.Sub(cl.NeedsReviewChanged).Seconds()/86400), int(now.Sub(cl.Start).Seconds()/86400), who)
	for _, id := range cl.Issues {
		fmt.Fprintf(&buf, " #%d", id)
	}
	return buf.String()
}

package main

import (
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

func buildAmpTree(home, pwd string, days int, now int64, cacheOnly bool) (sessionTree, error) {
	threads, err := ampListForTree(home, cacheOnly)
	groups := map[string]*treeFolder{}
	repoCache := map[string]string{}
	var order []string
	cutoff := int64(0)
	if days > 0 {
		cutoff = now - int64(days)*86400
	}
	for _, th := range threads {
		mtime := th.updatedUnix()
		if cutoff > 0 && mtime < cutoff {
			continue
		}
		cwd := th.cwd()
		label, dir := cwd, ""
		if cwd != "" {
			label = cwd
			if isDir(cwd) {
				dir = cwd
				if !cacheOnly {
					label = repoForCwd(cwd, home, repoCache)
				}
			}
		} else {
			label = strings.TrimPrefix(th.Tree, "file://")
			if label == "" {
				label = "Amp · remote"
			}
		}
		g := groups[label]
		if g == nil {
			g = &treeFolder{Cwd: label, Dir: dir, Slug: claudeSlug(label), Expanded: filepath.Clean(cwd) == filepath.Clean(pwd)}
			groups[label] = g
			order = append(order, label)
		}
		content := ""
		if ex, cacheErr := ampExportThread(home, th.ID, true); cacheErr == nil {
			content = ampExportContent(ex, contentBudget)
		}
		g.Sessions = append(g.Sessions, treeSession{
			Agent: AgentAmp, Path: th.ID, ID: th.ID, Mtime: mtime,
			Snippet: collapsePreview(th.Title), Msgs: th.MessageCount, cwd: cwd, Repo: label, Content: content,
		})
		if mtime > g.Mtime {
			g.Mtime = mtime
		}
	}
	tree := sessionTree{Now: now, Pwd: pwd, Home: home}
	for _, label := range order {
		g := groups[label]
		sortSessions(g.Sessions)
		tree.Folders = append(tree.Folders, *g)
	}
	sortFolders(tree.Folders)
	return tree, err
}

func ampExportContent(ex ampExport, budget int) string {
	var parts []string
	total := 0
	for i := len(ex.Messages) - 1; i >= 0 && total < budget; i-- {
		for j := len(ex.Messages[i].Content) - 1; j >= 0 && total < budget; j-- {
			block := ex.Messages[i].Content[j]
			if block.Type != "text" || block.Text == "" {
				continue
			}
			parts = append(parts, block.Text)
			total += len(block.Text) + 1
		}
	}
	slices.Reverse(parts)
	joined := strings.ToLower(strings.Join(parts, "\n"))
	if len(joined) > budget {
		joined = joined[len(joined)-budget:]
	}
	return joined
}

func ampListForTree(home string, cacheOnly bool) ([]ampThread, error) {
	if cacheOnly {
		return ampList(home, true)
	}
	path := filepath.Join(ampCacheDir(home), "threads.json")
	if cached, err := readAmpList(path); err == nil {
		go func() { _, _ = ampList(home, false) }()
		return cached, nil
	}
	return ampListWithin(home, false, ampFocusRefreshTimeout)
}

func mergeAgentTrees(base sessionTree, extra sessionTree) sessionTree {
	byGroup := make(map[string]int, len(base.Folders))
	for i := range base.Folders {
		byGroup[base.Folders[i].Cwd] = i
	}
	for _, f := range extra.Folders {
		if i, ok := byGroup[f.Cwd]; ok {
			base.Folders[i].Sessions = append(base.Folders[i].Sessions, f.Sessions...)
			sortSessions(base.Folders[i].Sessions)
			if f.Mtime > base.Folders[i].Mtime {
				base.Folders[i].Mtime = f.Mtime
			}
			base.Folders[i].Expanded = base.Folders[i].Expanded || f.Expanded
			if base.Folders[i].Dir == "" {
				base.Folders[i].Dir = f.Dir
			}
			continue
		}
		byGroup[f.Cwd] = len(base.Folders)
		base.Folders = append(base.Folders, f)
	}
	sort.SliceStable(base.Folders, func(i, j int) bool { return base.Folders[i].Mtime > base.Folders[j].Mtime })
	return base
}

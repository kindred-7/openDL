package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"openDL/download"
	"openDL/pathrec"
)

// 站点抓取：从来源页递归跟进同一站点的页面，收集可下载文件链接。
// 不执行页面脚本；动态资源仍由浏览器扩展嗅探覆盖。

const (
	grabDepthMax = 3
	grabPageMax  = 100
	grabTimeout  = 3 * time.Minute
)

type grabRequest struct {
	start string
	depth int
	types map[string]bool // 允许的扩展名（含点）；nil 表示全部可下载类型
	rules []pathrec.SiteRule
	fetch func(*http.Request) (*http.Response, error)
}

type grabResult struct {
	links     []string
	pages     int
	truncated bool
	err       error // 起始页不可读时的错误
}

// grabLinks 抓取起始页并按深度跟进页面链接，只在同一主机内跟进。
// 单页失败或非页面响应跳过不中断；达到页面数或时间上限返回部分结果。
func grabLinks(req grabRequest) grabResult {
	ctx, cancel := context.WithTimeout(context.Background(), grabTimeout)
	defer cancel()
	start, err := url.Parse(strings.TrimSpace(req.start))
	if err != nil || start.Hostname() == "" || (start.Scheme != "http" && start.Scheme != "https") {
		return grabResult{err: fmt.Errorf(tr("来源页地址无效"))}
	}
	depth := req.depth
	if depth < 0 {
		depth = 0
	}
	if depth > grabDepthMax {
		depth = grabDepthMax
	}
	host := strings.ToLower(start.Host)
	type queuedPage struct {
		address string
		level   int
	}
	queue := []queuedPage{{address: start.String(), level: 0}}
	visited := map[string]bool{start.String(): true}
	result := grabResult{}
	seenLinks := map[string]bool{}
	for len(queue) > 0 && result.pages < grabPageMax {
		if ctx.Err() != nil {
			result.truncated = true
			break
		}
		current := queue[0]
		queue = queue[1:]
		body, base, pageErr := fetchPage(ctx, current.address, req.fetch)
		if pageErr != nil {
			if current.level == 0 && result.pages == 0 {
				result.err = pageErr
			}
			continue
		}
		result.pages++
		files, pages, scanErr := pageLinkScan(body, base, req.rules)
		if scanErr != nil {
			continue
		}
		for _, link := range files {
			if seenLinks[link] || !grabTypeAllowed(link, req.types) {
				continue
			}
			seenLinks[link] = true
			result.links = append(result.links, link)
		}
		if current.level >= depth {
			continue
		}
		for _, page := range pages {
			ref, refErr := url.Parse(page)
			if refErr != nil || strings.ToLower(ref.Host) != host || visited[page] {
				continue
			}
			visited[page] = true
			queue = append(queue, queuedPage{address: page, level: current.level + 1})
		}
	}
	if len(queue) > 0 {
		result.truncated = true
	}
	return result
}

// grabTypeAllowed 按扩展名筛选抓取结果。无扩展名的下载属性或媒体链接无法
// 分类，保留给用户在链接列表中确认。
func grabTypeAllowed(link string, types map[string]bool) bool {
	if types == nil {
		return true
	}
	u, err := url.Parse(link)
	if err != nil {
		return false
	}
	ext := strings.ToLower(filepath.Ext(u.Path))
	if ext == "" {
		return true
	}
	return types[ext]
}

// mirrorSubdir 返回链接相对来源页目录的归档子路径；不同源或越出来源页
// 目录时返回空，链接平铺保存。
func mirrorSubdir(rootPage, link string) string {
	root, err := url.Parse(strings.TrimSpace(rootPage))
	target, targetErr := url.Parse(strings.TrimSpace(link))
	if err != nil || targetErr != nil || root.Hostname() == "" || !strings.EqualFold(root.Host, target.Host) {
		return ""
	}
	rootPath := root.Path
	if !strings.HasSuffix(rootPath, "/") {
		if i := strings.LastIndexByte(rootPath, '/'); i >= 0 {
			rootPath = rootPath[:i+1]
		} else {
			rootPath = "/"
		}
	}
	if !strings.HasPrefix(target.Path, rootPath) {
		return ""
	}
	rest := strings.TrimPrefix(target.Path, rootPath)
	i := strings.LastIndexByte(rest, '/')
	if i < 0 {
		return ""
	}
	rest = rest[:i]
	cleaned := make([]string, 0, strings.Count(rest, "/")+1)
	for _, part := range strings.Split(rest, "/") {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return ""
		}
		if name := pathrec.SanitizeFilename(part); name != "" {
			cleaned = append(cleaned, name)
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	return filepath.Join(cleaned...)
}


// grabProject 是保存的抓取项目：来源页与抓取参数，可选周期执行。
type grabProject struct {
	Name      string `json:"name"`
	Page      string `json:"page"`
	Depth     int    `json:"depth"`
	Types     string `json:"types,omitempty"`
	Mirror    bool   `json:"mirror,omitempty"`
	Directory string `json:"directory,omitempty"`
	Interval  int    `json:"interval,omitempty"` // 周期抓取间隔（分钟）；0 表示仅手动
}

// grabProjectsPath 返回配置目录中的抓取项目表。
func grabProjectsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "opendl", "grab-projects.json"), nil
}

// loadGrabProjects 读取项目表；文件缺失或损坏时返回空表。
func loadGrabProjects() []grabProject {
	path, err := grabProjectsPath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var projects []grabProject
	if json.Unmarshal(data, &projects) != nil {
		return nil
	}
	return normalizeGrabProjects(projects)
}

// saveGrabProjects 校验后原子保存项目表。
func saveGrabProjects(projects []grabProject) error {
	path, err := grabProjectsPath()
	if err != nil {
		return err
	}
	projects = normalizeGrabProjects(projects)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(projects)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// normalizeGrabProjects 清洗项目表：名称唯一、地址与参数合法。
func normalizeGrabProjects(projects []grabProject) []grabProject {
	out := make([]grabProject, 0, len(projects))
	seen := map[string]bool{}
	for _, project := range projects {
		name := strings.TrimSpace(project.Name)
		page := strings.TrimSpace(project.Page)
		if name == "" || len([]rune(name)) > 24 || seen[name] || len(out) >= 32 {
			continue
		}
		u, err := url.Parse(page)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		if project.Depth < 0 || project.Depth > grabDepthMax {
			continue
		}
		if project.Types != "" {
			if _, ok := catExt[project.Types]; !ok && project.Types != "other" {
				continue
			}
		}
		interval := project.Interval
		if interval != 0 {
			if normalized, err := download.NormalizeSyncMinutes(interval); err != nil {
				continue
			} else {
				interval = normalized
			}
		}
		seen[name] = true
		out = append(out, grabProject{
			Name:      name,
			Page:      page,
			Depth:     project.Depth,
			Types:     project.Types,
			Mirror:    project.Mirror,
			Directory: strings.TrimSpace(project.Directory),
			Interval:  interval,
		})
	}
	return out
}

// grabWatch 周期执行保存的抓取项目；项目表每次从磁盘读取，编辑即时生效。
func (pg *mainPage) grabWatch() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	nextRun := map[string]time.Time{}
	for {
		select {
		case <-pg.stop:
			return
		case <-ticker.C:
		}
		now := time.Now()
		for _, project := range loadGrabProjects() {
			if project.Interval <= 0 {
				continue
			}
			due, ok := nextRun[project.Name]
			if !ok {
				// 首次见到的项目从下一个周期开始，避免启动即抓取。
				nextRun[project.Name] = now.Add(time.Duration(project.Interval) * time.Minute)
				continue
			}
			if now.Before(due) {
				continue
			}
			nextRun[project.Name] = now.Add(time.Duration(project.Interval) * time.Minute)
			pg.runGrabProject(project)
		}
	}
}

// runGrabProject 抓取项目并为新增链接建任务；同地址已有任务的链接跳过。
func (pg *mainPage) runGrabProject(project grabProject) {
	go func() {
		types := grabTypes(project.Types)
		result := grabLinks(grabRequest{
			start: project.Page,
			depth: project.Depth,
			types: types,
			rules: loadSiteRules(),
			fetch: pg.grabFetch,
		})
		existing := map[string]bool{}
		if pg.mgr != nil {
			for _, snapshot := range pg.mgr.Snapshots() {
				existing[snapshot.URL] = true
			}
		}
		added := 0
		for _, link := range result.links {
			if existing[link] || pg.mgr == nil {
				continue
			}
			existing[link] = true
			pg.mgr.Add(grabProjectRequest(project, link))
			added++
		}
		if pg.win == nil {
			return
		}
		pg.win.Update(func() {
			select {
			case <-pg.stop:
				return
			default:
			}
			if added == 0 {
				pg.flash(tr("抓取项目没有新增链接"))
				return
			}
			pg.flash(fmt.Sprintf(tr("抓取项目新增 %d 个任务"), added))
		})
	}()
}


// findGrabProject 按名称查找项目。
func findGrabProject(projects []grabProject, name string) (grabProject, bool) {
	for _, project := range projects {
		if project.Name == name {
			return project, true
		}
	}
	return grabProject{}, false
}

// loadGrabProject 把选中的项目参数载入抓取对话框。
func (pg *mainPage) loadGrabProject() {
	project, ok := findGrabProject(pg.grabProjectCache, pg.dlgGrabProject)
	if !ok {
		return
	}
	pg.dlgPage = project.Page
	pg.dlgGrabDepth = strconv.Itoa(project.Depth)
	pg.dlgGrabType = project.Types
	pg.dlgMirror = project.Mirror
	pg.dlgGrabInterval = grabIntervalLabel(project.Interval)
	if project.Directory != "" {
		pg.dlgDir = project.Directory
	}
}

// grabIntervalLabel 把周期分钟数转成对话框选项值。
func grabIntervalLabel(minutes int) string {
	switch minutes {
	case 60:
		return "60"
	case 360:
		return "360"
	case 1440:
		return "1440"
	default:
		return ""
	}
}

// saveGrabProject 保存当前抓取参数为项目；已选项目按原名更新。
func (pg *mainPage) saveGrabProject() {
	name := strings.TrimSpace(pg.dlgGrabProject)
	if name == "" {
		if u, err := url.Parse(strings.TrimSpace(pg.dlgPage)); err == nil && u.Hostname() != "" {
			name = u.Hostname()
		}
	}
	if name == "" {
		pg.flash(tr("请先填写来源页"))
		return
	}
	depth, _ := strconv.Atoi(pg.dlgGrabDepth)
	interval := 0
	switch pg.dlgGrabInterval {
	case "60", "360", "1440":
		interval, _ = strconv.Atoi(pg.dlgGrabInterval)
	}
	updated := grabProject{
		Name:      name,
		Page:      strings.TrimSpace(pg.dlgPage),
		Depth:     depth,
		Types:     pg.dlgGrabType,
		Mirror:    pg.dlgMirror,
		Directory: strings.TrimSpace(pg.dlgDir),
		Interval:  interval,
	}
	projects := []grabProject{updated}
	for _, project := range pg.grabProjectCache {
		if project.Name != name {
			projects = append(projects, project)
		}
	}
	projects = normalizeGrabProjects(projects)
	if _, ok := findGrabProject(projects, name); !ok {
		pg.flash(tr("抓取项目参数无效，未保存"))
		return
	}
	if err := saveGrabProjects(projects); err != nil {
		pg.flash(fmt.Sprintf(tr("保存抓取项目失败：%s"), err.Error()))
		return
	}
	pg.grabProjectCache = projects
	pg.dlgGrabProject = name
	pg.flash(fmt.Sprintf(tr("已保存抓取项目 %s"), name))
}

// removeGrabProject 删除选中的项目。
func (pg *mainPage) removeGrabProject() {
	name := strings.TrimSpace(pg.dlgGrabProject)
	if name == "" {
		return
	}
	projects := make([]grabProject, 0, len(pg.grabProjectCache))
	for _, project := range pg.grabProjectCache {
		if project.Name != name {
			projects = append(projects, project)
		}
	}
	if err := saveGrabProjects(projects); err != nil {
		pg.flash(fmt.Sprintf(tr("删除抓取项目失败：%s"), err.Error()))
		return
	}
	pg.grabProjectCache = projects
	pg.dlgGrabProject = ""
	pg.flash(fmt.Sprintf(tr("已删除抓取项目 %s"), name))
}

// grabProjectRequest 按项目参数构造下载请求；镜像目录优先于分类归档。
func grabProjectRequest(project grabProject, link string) download.Request {
	req := download.Request{
		URL:       link,
		Directory: strings.TrimSpace(project.Directory),
		PageURL:   project.Page,
		Headers:   map[string]string{"Referer": project.Page},
	}
	if project.Mirror {
		if sub := mirrorSubdir(project.Page, link); sub != "" {
			req.Directory = filepath.Join(req.Directory, sub)
		}
	}
	return req
}

// loadSiteRules 读取站点规则：本地文件与远程规则源缓存合并，本地优先；
// 修改后重新提取或抓取即生效。
func loadSiteRules() []pathrec.SiteRule {
	path, err := pathrec.SiteRulesPath()
	if err != nil {
		return nil
	}
	return pathrec.LoadSiteRulesWithRemote(path)
}

// grabTypes 把分类选项转换成扩展名筛选表；空分类返回 nil（全部类型）。
func grabTypes(category string) map[string]bool {
	exts := catExt[category]
	if category == "" || len(exts) == 0 {
		return nil
	}
	set := make(map[string]bool, len(exts))
	for _, ext := range exts {
		set[ext] = true
	}
	return set
}

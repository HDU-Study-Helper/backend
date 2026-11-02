package chaoxing

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"academic/backend/internal/contract"
	"golang.org/x/net/html"
)

// Folder navigation belongs only in the encrypted checkpoint. In particular,
// enc must never become part of a material record or a public URL.
type materialFolder struct {
	ID   string
	Name string
	Enc  string
	Path []string
}

const materialList = "https://mooc2-ans.chaoxing.com/mooc2-ans/coursedata/stu-datalist"

func syncMaterials(ctx context.Context, c *http.Client, v *cursor) ([]contract.Record, error) {
	co := v.Courses[v.Index]
	if co.ID == "" || co.Class == "" || co.CPI == "" {
		return nil, contract.ErrParse
	}
	if len(v.MaterialFolders) == 0 {
		v.MaterialFolders = []materialFolder{{ID: "0"}}
	}
	folder := v.MaterialFolders[0]
	q := url.Values{
		"courseid": {co.ID}, "clazzid": {co.Class}, "cpi": {co.CPI}, "ut": {"s"},
		"pages": {strconv.Itoa(v.Page)}, "type": {"1"}, "dataId": {folder.ID},
		"dataName": {folder.Name}, "enc": {folder.Enc}, "parent": {""}, "order": {""},
		"microTopicId": {""}, "t": {strconv.FormatInt(time.Now().UnixMilli(), 10)},
	}
	b, err := request(ctx, c, http.MethodGet, materialList+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	rows, children, totalPages, err := parseMaterials(b, co, folder, v.Page)
	if err != nil {
		return nil, err
	}
	if v.Page > 1 && totalPages != v.MaterialTotalPages {
		return nil, contract.ErrParse
	}
	v.MaterialTotalPages = totalPages
	known := map[string]bool{}
	for _, id := range v.MaterialIDs {
		known[id] = true
	}
	for _, row := range rows {
		// IDs are course-scoped, including across directories. A repeated ID
		// detects ignored page parameters, directory cycles and unstable scans.
		if known[row.ExternalID] || row.ExternalID == "0" {
			return nil, contract.ErrParse
		}
		known[row.ExternalID] = true
		v.MaterialIDs = append(v.MaterialIDs, row.ExternalID)
	}
	v.MaterialFolders = append(v.MaterialFolders, children...)
	if len(v.MaterialIDs) > 10000 || len(v.MaterialFolders) > 10000 {
		return nil, contract.ErrParse
	}
	if v.Page < totalPages {
		v.Page++
	} else {
		v.MaterialFolders = v.MaterialFolders[1:]
		v.Page = 1
		v.MaterialTotalPages = 0
		if len(v.MaterialFolders) == 0 {
			v.Index++
			v.MaterialIDs = nil
		}
	}
	if len(encode(*v)) > 1<<20 {
		return nil, contract.ErrParse
	}
	return rows, nil
}

var materialPageCount = regexp.MustCompile(`当前页共\s*([0-9]+)\s*个`)

func parseMaterials(b []byte, co course, folder materialFolder, page int) ([]contract.Record, []materialFolder, int, error) {
	d, err := document(b)
	if err != nil {
		return nil, nil, 0, err
	}
	rows := []contract.Record{}
	children := []materialFolder{}
	container, bad := false, false
	walk(d, func(n *html.Node) {
		if class(n, "dataBody") {
			container = true
		}
		if n.Type != html.ElementNode || n.Data != "ul" || !class(n, "dataBody_td") {
			return
		}
		id, name, kind := strings.TrimSpace(attr(n, "id")), strings.TrimSpace(attr(n, "dataname")), strings.TrimSpace(attr(n, "type"))
		// Upstream also includes an unused blank row template.
		if name == "" && id == "" && kind == "" {
			return
		}
		if id == "" || name == "" || kind == "" {
			bad = true
			return
		}
		path := append(append([]string{}, folder.Path...), name)
		isFolder := kind == "afolder"
		fields := map[string]any{
			"course_id": co.ID, "class_id": co.Class, "data_id": id,
			"parent_data_id": folder.ID, "path": path, "is_folder": isFolder, "file_type": kind,
		}
		walk(n, func(cell *html.Node) {
			for cls, key := range map[string]string{"dataBody_size_stu": "size_raw", "dataBody_creater_stu": "creator", "dataBody_time_stu": "time_raw"} {
				if class(cell, cls) {
					if value := text(cell); value != "" {
						fields[key] = value
					}
				}
			}
		})
		rows = append(rows, record(contract.Material, id, co.key(), name, "", fields))
		if isFolder {
			if len(path) > 32 {
				bad = true
			}
			children = append(children, materialFolder{ID: id, Name: name, Enc: attr(n, "enc"), Path: path})
		}
	})
	// Both pairs occur in captured official HTML. Check all supplied values:
	// "当前页共 N 个" is a page count, never a total or end-of-feed signal.
	totalPages, currentPage := -1, -1
	walk(d, func(n *html.Node) {
		if n.Data != "input" {
			return
		}
		key := attr(n, "id")
		if key == "" {
			key = attr(n, "name")
		}
		if key != "totalPages" && key != "totalPage" && key != "pages" && key != "currentPageNo" {
			return
		}
		number, e := strconv.Atoi(attr(n, "value"))
		if e != nil || number < 0 || number > 1000 {
			bad = true
			return
		}
		if key == "totalPages" || key == "totalPage" {
			if totalPages != -1 && totalPages != number {
				bad = true
			}
			totalPages = number
		} else {
			if currentPage != -1 && currentPage != number {
				bad = true
			}
			currentPage = number
		}
	})
	if totalPages < 0 || currentPage != page || (page > totalPages && !(page == 1 && totalPages == 0 && len(rows) == 0)) || (page < totalPages && len(rows) == 0) {
		bad = true
	}
	visible := text(d)
	count := materialPageCount.FindStringSubmatch(visible)
	if len(count) == 2 {
		n, e := strconv.Atoi(count[1])
		if e != nil || n != len(rows) {
			bad = true
		}
	}
	empty := strings.Contains(visible, "暂无资料") || strings.Contains(visible, "暂无数据") || (len(count) == 2 && count[1] == "0")
	if bad || len(rows) > 500 || (len(rows) == 0 && !container && !empty) {
		return nil, nil, 0, contract.ErrParse
	}
	return rows, children, totalPages, nil
}

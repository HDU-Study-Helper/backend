// academic-maintain migrates/rebuilds or exports the current owner's structured academic archive.
// It never invokes models or starts source polling. Secrets stay in process environment.
package main

import (
	"academic/backend/internal/academic"
	"academic/backend/internal/dbbridge"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func check(err error) {
	if err != nil {
		panic("academic maintenance failed: " + err.Error())
	}
}
func main() {
	if len(os.Args) != 2 {
		panic("usage: academic-maintain rebuild|export|sync-prompts")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	db, err := dbbridge.Open(ctx, os.Getenv("DATABASE_URL"))
	check(err)
	defer db.Close()
	owner := os.Getenv("ACADEMIC_OWNER_ID")
	if owner == "" {
		name := os.Getenv("BOOTSTRAP_ADMIN_USERNAME")
		if name == "" {
			panic("ACADEMIC_OWNER_ID or BOOTSTRAP_ADMIN_USERNAME required")
		}
		user, err := db.Users().GetByUsername(ctx, name)
		check(err)
		owner = user.ID
	}
	if os.Args[1] == "sync-prompts" {
		check(syncPrompts(ctx, db, owner))
		return
	}
	if os.Args[1] == "rebuild" {
		check(db.Migrate(ctx))
		result, err := db.Academic().Rebuild(ctx, owner, "")
		check(err)
		raw, _ := json.Marshal(result)
		fmt.Println(string(raw))
		return
	}
	if os.Args[1] != "export" {
		panic("unknown operation")
	}
	tree, err := db.Academic().Tree(ctx, owner, "", "")
	check(err)
	dir := os.Getenv("ACADEMIC_OUTPUT_DIR")
	if dir == "" {
		dir = "../.build/academic-acceptance"
	}
	check(os.MkdirAll(dir, 0700))
	raw, err := json.MarshalIndent(tree, "", "  ")
	check(err)
	check(os.WriteFile(filepath.Join(dir, "directory.json"), raw, 0600))
	text := academic.RenderCompactTree(tree)
	check(os.WriteFile(filepath.Join(dir, "directory.txt"), []byte(text), 0600))
	check(os.WriteFile(filepath.Join(dir, "directory-details.txt"), []byte(academic.RenderTree(tree)), 0600))
	counts := map[string]map[string]int{}
	for _, item := range tree.Items {
		if counts[item.Source] == nil {
			counts[item.Source] = map[string]int{}
		}
		counts[item.Source][item.Kind]++
	}
	summary := map[string]any{"exported_at": time.Now().UTC(), "offerings": len(tree.Offerings), "items": len(tree.Items), "counts": counts, "source_completeness": "inspect offering coverage and item missing fields"}
	out, _ := json.MarshalIndent(summary, "", "  ")
	check(os.WriteFile(filepath.Join(dir, "summary.json"), out, 0600))
	for _, src := range []string{"chaoxing", "mooc"} {
		small := tree
		small.Offerings = nil
		small.Items = nil
		for _, o := range tree.Offerings {
			if o.Source == src {
				small.Offerings = append(small.Offerings, o)
			}
		}
		for _, i := range tree.Items {
			if i.Source == src {
				small.Items = append(small.Items, i)
			}
		}
		check(os.WriteFile(filepath.Join(dir, src+"-directory.txt"), []byte(academic.RenderCompactTree(small)), 0600))
	}
	fmt.Printf("Exported %d offerings, %d items to %s\n", len(tree.Offerings), len(tree.Items), strings.TrimSpace(dir))
}

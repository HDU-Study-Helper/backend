package main

import (
	"academic/backend/internal/academic"
	"academic/backend/internal/model"
	"encoding/json"
	"testing"
)

func TestExportUsesCompleteReadableTree(t *testing.T) {
	tree := model.AcademicTree{Offerings: []model.AcademicOffering{{ID: "course", Source: "mooc", ConnectionID: "connection", Name: "具体课程", Description: "真实简介", Details: json.RawMessage(`{"requirements":"源要求"}`)}}, Items: []model.AcademicItem{{ID: "item", Source: "mooc", ConnectionID: "connection", OfferingID: "course", Title: "具体事项", Kind: "assignment"}}}
	if academic.RenderCompactTree(tree) == "" || academic.RenderTree(tree) == "" {
		t.Fatal("empty export")
	}
}

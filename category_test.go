package main

import "testing"

// buildTestTree returns a small category tree for path-resolution tests:
//
//	root
//	├── coding (server: serena -> find_symbol, get_symbols_overview)
//	└── web
//	    ├── browsers (server: chrome -> open_url, get_page_source)
//	    └── [server: github -> create_issue]
func buildTestTree() *Category {
	return &Category{
		Description: "root",
		Children: map[string]*Category{
			"coding": {
				Description: "coding tools",
				MCP: map[string]*ServerDef{
					"serena": {Command: "echo"},
				},
			},
			"web": {
				Description: "web tools",
				MCP: map[string]*ServerDef{
					"github": {Command: "echo"},
				},
				Children: map[string]*Category{
					"browsers": {
						Description: "browser tools",
						MCP: map[string]*ServerDef{
							"chrome": {ServerURL: "http://localhost:5001"},
						},
					},
				},
			},
		},
	}
}

func TestFindCategory(t *testing.T) {
	root := buildTestTree()
	if c := FindCategory(root, ""); c != root {
		t.Error("empty path should return root")
	}
	if c := FindCategory(root, "coding"); c == nil || c.Description != "coding tools" {
		t.Errorf("coding: %+v", c)
	}
	if c := FindCategory(root, "web.browsers"); c == nil || c.Description != "browser tools" {
		t.Errorf("web.browsers: %+v", c)
	}
	if c := FindCategory(root, "nope"); c != nil {
		t.Errorf("nonexistent path should return nil, got %+v", c)
	}
	if c := FindCategory(root, "web.nope.deep"); c != nil {
		t.Errorf("partial-miss path should return nil, got %+v", c)
	}
}

func TestListChildCategories(t *testing.T) {
	root := buildTestTree()
	cats := ListChildCategories(root)
	if len(cats) != 2 {
		t.Fatalf("expected 2 top-level categories, got %d: %v", len(cats), cats)
	}
	if cats["coding"] != "coding tools" {
		t.Errorf("coding description = %q", cats["coding"])
	}
	// A leaf category with no children returns nil.
	leaf := FindCategory(root, "coding")
	if got := ListChildCategories(leaf); got != nil {
		t.Errorf("leaf category children = %v, want nil", got)
	}
	// Nil category returns nil.
	if got := ListChildCategories(nil); got != nil {
		t.Errorf("nil category children = %v, want nil", got)
	}
}

func TestSplitPath(t *testing.T) {
	tests := []struct {
		in       string
		wantCat  string
		wantTool string
	}{
		{"", "", ""},
		{"tool", "", "tool"},
		{"a.b.tool", "a.b", "tool"},
		{"a.b.c.tool", "a.b.c", "tool"},
	}
	for _, tt := range tests {
		gotCat, gotTool := SplitPath(tt.in)
		if gotCat != tt.wantCat || gotTool != tt.wantTool {
			t.Errorf("SplitPath(%q) = (%q, %q), want (%q, %q)",
				tt.in, gotCat, gotTool, tt.wantCat, tt.wantTool)
		}
	}
}

func TestServerPathFor(t *testing.T) {
	if got := serverPathFor("", "serena"); got != "serena" {
		t.Errorf("serverPathFor(\"\", serena) = %q", got)
	}
	if got := serverPathFor("coding", "serena"); got != "coding.serena" {
		t.Errorf("serverPathFor(coding, serena) = %q", got)
	}
}
package main

import "strings"

// FindCategory walks the category tree from root along the given dot-delimited
// path. An empty path returns the root. It returns nil if any segment is not a
// child of the current category.
func FindCategory(root *Category, path string) *Category {
	if path == "" {
		return root
	}
	cur := root
	for _, seg := range strings.Split(path, ".") {
		if cur == nil {
			return nil
		}
		next, ok := cur.Children[seg]
		if !ok {
			return nil
		}
		cur = next
	}
	return cur
}

// ListChildCategories returns the subcategories of the given category as a
// map of name to description. Returns nil if the category has no children.
func ListChildCategories(c *Category) map[string]string {
	if c == nil || len(c.Children) == 0 {
		return nil
	}
	out := make(map[string]string, len(c.Children))
	for name, child := range c.Children {
		out[name] = child.Description
	}
	return out
}

// linkParents sets the parent pointer of every child category so timeout
// resolution can walk up the tree. UnmarshalJSON already does this during
// LoadConfig; calling it covers trees built programmatically. It is
// idempotent.
func (c *Category) linkParents() {
	for _, child := range c.Children {
		child.parent = c
		child.linkParents()
	}
}

// SplitPath splits a full tool path "a.b.tool" into the category path "a.b"
// and the tool name "tool". The tool name is the last dot-delimited segment;
// the category path is everything before it.
func SplitPath(full string) (categoryPath, toolName string) {
	if full == "" {
		return "", ""
	}
	idx := strings.LastIndex(full, ".")
	if idx < 0 {
		return "", full
	}
	return full[:idx], full[idx+1:]
}
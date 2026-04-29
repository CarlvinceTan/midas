package snapshot

import (
	"strings"
)

type treeA11yNode struct {
	Role             string
	Name             string
	Description      string
	Value            any
	NodeID           string
	BackendDOMNodeID int
	ParentID         string
	ChildIDs         []string
	Children         []*treeA11yNode
	EncodedID        string
}

func formatTreeLine(node *treeA11yNode, level int) string {
	indent := strings.Repeat("  ", level)
	labelID := node.EncodedID
	if labelID == "" {
		labelID = node.NodeID
	}
	label := "[" + labelID + "] " + node.Role
	if node.Name != "" {
		label += ": " + cleanSnapshotText(node.Name)
	}
	if len(node.Children) == 0 {
		return indent + label
	}
	lines := []string{indent + label}
	for _, child := range node.Children {
		lines = append(lines, formatTreeLine(child, level+1))
	}
	return strings.Join(lines, "\n")
}

func injectSubtrees(rootOutline string, idToTree map[string]string) string {
	type frame struct {
		lines []string
		i     int
	}
	out := make([]string, 0)
	visited := make(map[string]bool)
	stack := []frame{{lines: strings.Split(rootOutline, "\n")}}
	for len(stack) > 0 {
		top := &stack[len(stack)-1]
		if top.i >= len(top.lines) {
			stack = stack[:len(stack)-1]
			continue
		}
		raw := top.lines[top.i]
		top.i++
		out = append(out, raw)
		indent := leadingWhitespace(raw)
		content := strings.TrimPrefix(raw, indent)
		if !strings.HasPrefix(content, "[") {
			continue
		}
		end := strings.Index(content, "]")
		if end <= 1 {
			continue
		}
		encID := content[1:end]
		childOutline := strings.TrimSpace(idToTree[encID])
		if childOutline == "" || visited[encID] {
			continue
		}
		visited[encID] = true
		out = append(out, indentBlock(injectSubtrees(childOutline, idToTree), indent+"  "))
	}
	return strings.Join(out, "\n")
}

func indentBlock(block, indent string) string {
	if block == "" {
		return ""
	}
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		lines[i] = indent + line
	}
	return strings.Join(lines, "\n")
}

func DiffTrees(prevTree, nextTree string) string {
	prevSet := make(map[string]struct{})
	for _, line := range strings.Split(prevTree, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		prevSet[line] = struct{}{}
	}
	added := make([]string, 0)
	for _, line := range strings.Split(nextTree, "\n") {
		core := strings.TrimSpace(line)
		if core == "" {
			continue
		}
		if _, ok := prevSet[core]; !ok {
			added = append(added, line)
		}
	}
	if len(added) == 0 {
		return ""
	}
	minIndent := -1
	for _, line := range added {
		indent := len(leadingWhitespace(line))
		if minIndent == -1 || indent < minIndent {
			minIndent = indent
		}
	}
	for i, line := range added {
		if len(line) >= minIndent {
			added[i] = line[minIndent:]
		}
	}
	return strings.Join(added, "\n")
}

func cleanSnapshotText(input string) string {
	const puaStart = 0xe000
	const puaEnd = 0xf8ff
	nbSpaces := map[rune]bool{
		0x00a0: true,
		0x202f: true,
		0x2007: true,
		0xfeff: true,
	}
	var out []rune
	prevSpace := false
	for _, r := range input {
		if r >= puaStart && r <= puaEnd {
			continue
		}
		if nbSpaces[r] {
			if !prevSpace {
				out = append(out, ' ')
				prevSpace = true
			}
			continue
		}
		out = append(out, r)
		prevSpace = r == ' '
	}
	return strings.TrimSpace(string(out))
}

func normalizeSpaces(s string) string {
	var out strings.Builder
	inWS := false
	for _, r := range s {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' || r == '\f' || r == '\v' {
			if !inWS {
				out.WriteByte(' ')
				inWS = true
			}
			continue
		}
		out.WriteRune(r)
		inWS = false
	}
	return out.String()
}

func leadingWhitespace(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return s[:i]
}

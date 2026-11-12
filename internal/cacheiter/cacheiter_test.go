// Package cacheiter holds a source check: no loop may range directly over a
// disgo cache iterator.
//
// A disgo cache iterator (caches.VoiceStates, Members, Roles, Guilds, ...)
// holds the cache's read lock until the loop ends. Any work in the loop body
// that reaches the same cache again (AddMember runs the member policy, which
// reads the voice states) takes the read lock a second time; a gateway update
// waiting for the write lock in between then blocks everyone for good. That is
// how the main bot stopped reading the gateway in v66. Collect first:
//
//	for _, vs := range slices.Collect(caches.VoiceStates(guildID)) { ... }
package cacheiter

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// iteratorMethods are the disgo cache methods that return a lock-holding iterator.
var iteratorMethods = map[string]bool{
	"VoiceStates": true, "Members": true, "Roles": true, "Channels": true,
	"ChannelsForGuild": true, "Guilds": true, "Emojis": true, "Stickers": true,
	"Threads": true, "ThreadMembers": true, "Messages": true, "Presences": true,
	"StageInstances": true, "GuildScheduledEvents": true, "SoundboardSounds": true,
	"All": true, "GroupAll": true,
}

func TestNoRangeOverCacheIterators(t *testing.T) {
	fset := token.NewFileSet()
	var bad []string
	for _, root := range []string{"../../internal", "../../integration", "../../cmd"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				rs, ok := n.(*ast.RangeStmt)
				if !ok {
					return true
				}
				if receiver, method, ok := cacheIteratorCall(rs.X); ok {
					bad = append(bad, fset.Position(rs.Pos()).String()+": range over "+receiver+"."+method+"(...)")
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, b := range bad {
		t.Errorf("%s holds the cache lock during the loop: wrap it in slices.Collect", b)
	}
}

// cacheIteratorCall reports whether expr calls an iterator method on a value
// whose expression names a cache (caches, Caches, k.caches, ...).
func cacheIteratorCall(expr ast.Expr) (receiver, method string, ok bool) {
	call, isCall := expr.(*ast.CallExpr)
	if !isCall {
		return "", "", false
	}
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel || !iteratorMethods[sel.Sel.Name] {
		return "", "", false
	}
	receiver = exprString(sel.X)
	if !strings.Contains(strings.ToLower(receiver), "cache") {
		return "", "", false
	}
	return receiver, sel.Sel.Name, true
}

func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	case *ast.CallExpr:
		return exprString(v.Fun) + "()"
	case *ast.StarExpr:
		return "*" + exprString(v.X)
	case *ast.ParenExpr:
		return "(" + exprString(v.X) + ")"
	case *ast.IndexExpr:
		return exprString(v.X) + "[]"
	default:
		return "?"
	}
}

func TestCheckSpotsADirectRange(t *testing.T) {
	src := `package p
func f() {
	for vs := range m.ownerClient.Caches.VoiceStates(g) { _ = vs }
	for _, vs := range slices.Collect(caches.VoiceStates(g)) { _ = vs }
	for _, l := range s.subs.Guilds(id) { _ = l }
}`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var hits []string
	ast.Inspect(f, func(n ast.Node) bool {
		if rs, ok := n.(*ast.RangeStmt); ok {
			if r, m, ok := cacheIteratorCall(rs.X); ok {
				hits = append(hits, r+"."+m)
			}
		}
		return true
	})
	if len(hits) != 1 || hits[0] != "m.ownerClient.Caches.VoiceStates" {
		t.Fatalf("hits = %v, want only the direct range over Caches.VoiceStates", hits)
	}
}

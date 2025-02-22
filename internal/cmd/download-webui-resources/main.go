package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
)

const assetDirName = "assets"

func main() {
	assets, err := parseAssets()
	if err != nil {
		log.Fatalf("failed to parse assets: %v", err)
	}

	if err := os.MkdirAll(assetDirName, 0700); err != nil {
		log.Fatalf("failed to create asset directory: %v", err)
	}

	assetsMap := make(map[string]string, len(assets))
	for _, asset := range assets {
		if !strings.HasPrefix(asset, "https://") {
			log.Printf("skipping local asset %q", asset)
			continue
		}

		bytes, err := downloadAsset(asset)
		if err != nil {
			log.Fatalf("failed to download asset %q: %v", asset, err)
		}

		hash := sha256.Sum256(bytes)
		hashedName := assetDirName + "/" + hex.EncodeToString(hash[:]) + path.Ext(asset)

		if err := os.WriteFile(hashedName, bytes, 0600); err != nil {
			log.Fatalf("failed to write asset %q: %v", hashedName, err)
		}

		assetsMap[asset] = hashedName
	}

	assetsGoSrc := "//go:build builtinassets\n\npackage webui\n\nvar assetsMap = map[string]string{"
	for asset, hashedName := range assetsMap {
		assetsGoSrc += fmt.Sprintf("\n\t%q: %q,", asset, hashedName)
	}

	assetsGoSrc += "\n}\n"

	if err := os.WriteFile("assets_builtinassets_gen.go", []byte(assetsGoSrc), 0600); err != nil {
		log.Fatalf("failed to write asset assets_builtinassets_gen.go: %v", err)
	}
}

func downloadAsset(url string) ([]byte, error) {
	res, err := http.Get(url)
	if err != nil {
		return nil, err
	}

	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", res.StatusCode)
	}

	return io.ReadAll(res.Body)
}

func parseAssets() ([]string, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(
		fset,
		".",
		func(fi fs.FileInfo) bool {
			return true
		},
		0,
	)
	if err != nil {
		return nil, err
	}

	assets := make(map[string]struct{})
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				if fd, ok := decl.(*ast.FuncDecl); ok {
					if fd.Name.Name == "RootLayout" {
						for _, stmt := range fd.Body.List {
							if rng, ok := stmt.(*ast.RangeStmt); ok {
								ast.Walk((*RangeAssetVisitor)(&assets), rng.X)
							}
						}
					} else {
						ast.Walk((*PropAssetVisitor)(&assets), decl)
					}
				}
			}
		}
	}

	out := make([]string, 0, len(assets))
	for asset := range assets {
		out = append(out, asset)
	}

	return out, nil
}

type RangeAssetVisitor map[string]struct{}

func (v *RangeAssetVisitor) Visit(node ast.Node) ast.Visitor {
	if call, ok := node.(*ast.CallExpr); ok && len(call.Args) == 2 {
		if ident, ok := call.Fun.(*ast.Ident); ok {
			if ident.Name == "append" {
				if sel, ok := call.Args[1].(*ast.SelectorExpr); ok {
					if xIdent, ok := sel.X.(*ast.Ident); ok {
						if xIdent.Name == "props" {
							switch sel.Sel.Name {
							case "stylesheets", "scripts":
								return (*StringSliceAssetVisitor)(v)
							}
						}
					}
				}
			}
		}
	}

	return v
}

type PropAssetVisitor map[string]struct{}

func (v *PropAssetVisitor) Visit(node ast.Node) ast.Visitor {
	if comp, ok := node.(*ast.CompositeLit); ok {
		if ident, ok := comp.Type.(*ast.Ident); ok {
			if ident.Name == "RootLayoutProps" {
				for _, elt := range comp.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						if kIdent, ok := kv.Key.(*ast.Ident); ok {
							switch kIdent.Name {
							case "stylesheets", "scripts":
								return (*StringSliceAssetVisitor)(v)
							}
						}
					}
				}
			}
		}

		return nil
	}

	return v
}

type StringSliceAssetVisitor map[string]struct{}

func (v *StringSliceAssetVisitor) Visit(node ast.Node) ast.Visitor {
	if vComp, ok := node.(*ast.CompositeLit); ok && isStringArrayTypeExpr(vComp.Type) {
		for _, vElt := range vComp.Elts {
			if lit, ok := vElt.(*ast.BasicLit); ok {
				if s, _ := strconv.Unquote(lit.Value); s != "" {
					(*v)[s] = struct{}{}
				}
			}
		}

		return nil
	}

	return v
}

func isStringArrayTypeExpr(expr ast.Expr) bool {
	if arr, ok := expr.(*ast.ArrayType); ok {
		if ident, ok := arr.Elt.(*ast.Ident); ok {
			return ident.Name == "string"
		}
	}

	return false
}

// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package doctor

import (
	"fmt"
	"go/ast"

	"code.google.com/p/go.tools/go/loader"
	"code.google.com/p/go.tools/go/types"
)

type SearchEngine struct {
	program *loader.Program
}

/* -=-=- Utility Methods -=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=- */

// TODO: These are duplicated from refactoring.go

func (r *SearchEngine) fileContaining(node ast.Node) *ast.File {
	tfile := r.program.Fset.File(node.Pos())
	for _, pkgInfo := range r.program.AllPackages {
		for _, thisFile := range pkgInfo.Files {
			thisTFile := r.program.Fset.File(thisFile.Package)
			if thisTFile == tfile {
				return thisFile
			}
		}
	}
	panic("No ast.File for node")
}

func (r *SearchEngine) pkgInfo(file *ast.File) *loader.PackageInfo {
	for _, pkgInfo := range r.program.AllPackages {
		for _, thisFile := range pkgInfo.Files {
			if thisFile == file {
				return pkgInfo
			}
		}
	}
	return nil
}

/* -=-=- Search Across Interfaces =-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=- */

// FindDeclarationsAcrossInterfaces finds all objects that might need to be
// renamed if the given identifier is renamed.  In the case of a method, there
// may be indirect relationships such as the following:
//
//      Interface1  Interface2
//         /  \      /  \
//        /  implements  \
//       /      \   /     \
//     Type1    Type2    Type3
//
// where renaming a method in Type1 could force a method of the same name to
// be renamed in Interface1, Interface2, Type2, and Type3.  This method
// returns a slice of all of all such identifiers.
func (r *SearchEngine) FindDeclarationsAcrossInterfaces(ident *ast.Ident) (decls []types.Object, err error) {
	pkgInfo := r.pkgInfo(r.fileContaining(ident))
	obj := pkgInfo.ObjectOf(ident)
	if obj == nil {
		err = fmt.Errorf("Unable to find declaration of %s", ident.Name)
		return
	}

	sig, ok := types.Object.Type(obj).(*types.Signature)
	if ok && sig.Recv() != nil {
		// Method -- match closure with respect to interfaces
		methodName := ident.Name
		allInterfaces := r.allInterfacesIncluding(methodName)
		allConcrete := r.allTypesIncluding(methodName)
		closure := closure(allInterfaces, allConcrete)
		recvType := sig.Recv().Type()
		return r.methods(closure[recvType], methodName), nil
	} else {
		// Function, variable, or something else -- resolves uniquely
		return []types.Object{obj}, nil
	}
}

func (r *SearchEngine) allInterfacesIncluding(method string) []*types.Interface {
	result := make(map[*types.Interface]int)
	for _, pkgInfo := range r.program.AllPackages {
		for _, typ := range pkgInfo.Types {
			intf, isInterface := typ.Underlying().(*types.Interface)
			if isInterface {
				if _, ok := result[intf]; !ok {
					obj, _, _ := types.LookupFieldOrMethod(
						typ, pkgInfo.Pkg, method)
					if obj != nil {
						result[intf] = 0
					}
				}
			}
		}
	}

	slice := make([]*types.Interface, 0, len(result))
	for t, _ := range result {
		slice = append(slice, t)
	}
	return slice
}

func (r *SearchEngine) allTypesIncluding(method string) []types.Type {
	result := make(map[types.Type]int)
	for _, pkgInfo := range r.program.AllPackages {
		for _, obj := range pkgInfo.Objects {
			if obj, ok := obj.(*types.TypeName); ok {
				typ := obj.Type()
				if _, ok := result[typ]; !ok {
					obj1, _, _ := types.LookupFieldOrMethod(
						typ, pkgInfo.Pkg, method)
					obj2, _, _ := types.LookupFieldOrMethod(
						typ.Underlying(), pkgInfo.Pkg,
						method)
					if obj1 != nil || obj2 != nil {
						result[typ] = 0
						result[typ.Underlying()] = 0
					}
				}

			}
		}
		for _, typ := range pkgInfo.Types {
			if _, ok := result[typ]; !ok {
				obj1, _, _ := types.LookupFieldOrMethod(
					typ, pkgInfo.Pkg, method)
				obj2, _, _ := types.LookupFieldOrMethod(
					typ.Underlying(), pkgInfo.Pkg, method)
				if obj1 != nil || obj2 != nil {
					result[typ] = 0
					result[typ.Underlying()] = 0
				}
			}
		}
	}

	slice := make([]types.Type, 0, len(result))
	for t, _ := range result {
		slice = append(slice, t)
	}
	return slice
}

func closure(interfcs []*types.Interface, typs []types.Type) map[types.Type][]types.Type {
	graph := digraphClosure(implementsGraph(interfcs, typs))

	result := make(map[types.Type][]types.Type, len(interfcs)+len(typs))
	for u, adj := range graph {
		typ := mapType(u, interfcs, typs)
		typClsr := make([]types.Type, 0, len(adj))
		for _, v := range adj {
			typClsr = append(typClsr, mapType(v, interfcs, typs))
		}
		result[typ] = typClsr
	}
	return result
}

func implementsGraph(interfcs []*types.Interface, typs []types.Type) [][]int {
	adj := make([][]int, len(interfcs)+len(typs))
	for i, interf := range interfcs {
		for j, typ := range typs {
			if types.Implements(typ, interf, false) {
				adj[i] = append(adj[i], len(interfcs)+j)
				adj[len(interfcs)+j] =
					append(adj[len(interfcs)+j], i)
			}
		}
	}
	// TODO: Handle subtype relationships due to embedded structs
	return adj
}

func mapType(node int, interfcs []*types.Interface, typs []types.Type) types.Type {
	if node >= len(interfcs) {
		return typs[node-len(interfcs)]
	} else {
		return interfcs[node]
	}
}

// methods returns a slice of objects, each of which corresponds to a
// definition of a method with the given name on a type in the given list.
func (r *SearchEngine) methods(ts []types.Type, methodName string) []types.Object {
	var result []types.Object
	for _, pkgInfo := range r.program.AllPackages {
		for id, obj := range pkgInfo.Objects {
			if obj != nil &&
				obj.Pos() == id.Pos() &&
				obj.Name() == methodName &&
				r.isMethodFor(ts, obj) {
				result = append(result, obj)
			}
		}
	}
	return result
}

func (r *SearchEngine) isMethodFor(ts []types.Type, obj types.Object) bool {
	fun, isFunc := obj.(*types.Func)
	if isFunc {
		sig := fun.Type().(*types.Signature)
		if sig.Recv() != nil {
			recvT := sig.Recv().Type()
			if r.containsType(ts, recvT) {
				return true
			}
		}
	}
	return false
}

func (r *SearchEngine) containsType(ts []types.Type, t types.Type) bool {
	for _, typ := range ts {
		if typ == t {
			return true
		}
	}
	return false
}

/* -=-=- Search by Identifier  -=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=- */

// FindOccurrences finds the location of all identifiers that are direct or
// indirect references to the same object as given identifier.  The returned
// map maps filenames to a slice of (offset, length) pairs describing locations
// at which the given identifier is referenced.
func (r *SearchEngine) FindOccurrences(ident *ast.Ident) (allOccurrences map[string][]OffsetLength, err error) {
	obj := r.pkgInfo(r.fileContaining(ident)).ObjectOf(ident)
	if obj == nil {
		err = fmt.Errorf("Unable to find declaration of %s", ident.Name)
		return
	}

	decls := []types.Object{obj}
	if r.isMethod(obj) {
		decls, err = r.FindDeclarationsAcrossInterfaces(ident)
		if err != nil {
			return
		}
	}
	allOccurrences = r.findOccurrences(decls)
	return
}

// isMethod returns true if the given object denotes a declaration of, or
// reference to, a method
func (r *SearchEngine) isMethod(obj types.Object) bool {
	switch sig := types.Object.Type(obj).(type) {
	case *types.Signature:
		return sig.Recv() != nil

	default:
		return false
	}
}

// findOccurrences finds all identifiers that resolve to one of the given
// objects.
func (r *SearchEngine) findOccurrences(decls []types.Object) map[string][]OffsetLength {
	result := make(map[string][]OffsetLength)
	for _, pkgInfo := range r.getPackages(decls) {
		for id, obj := range pkgInfo.Objects {
			if r.containsObject(decls, obj) {
				position := r.program.Fset.Position(id.NamePos)
				filename := position.Filename
				offset := position.Offset
				length := len(id.Name)
				result[filename] = append(result[filename],
					OffsetLength{offset, length})
			}
		}
	}
	return result
}

func (r *SearchEngine) containsObject(decls []types.Object, o types.Object) bool {
	for _, decl := range decls {
		if decl == o {
			return true
		}
	}
	return false
}

func (r *SearchEngine) getPackages(decls []types.Object) []*loader.PackageInfo {
	pkgs := make(map[*loader.PackageInfo]int)
	for _, decl := range decls {
		if types.Object.IsExported(decl) {
			return r.allPackages()
		} else {
			pkgs[r.pkgInfoForPkg(decl.Pkg())] = 0
		}
	}

	result := make([]*loader.PackageInfo, 0, len(pkgs))
	for pkgInfo, _ := range pkgs {
		result = append(result, pkgInfo)
	}
	return result
}

func (r *SearchEngine) pkgInfoForPkg(pkg *types.Package) *loader.PackageInfo {
	for _, pkgInfo := range r.program.AllPackages {
		if pkgInfo.Pkg == pkg {
			return pkgInfo
		}
	}
	return nil
}

func (r *SearchEngine) allPackages() []*loader.PackageInfo {
	var pkgs []*loader.PackageInfo
	for _, pkgInfo := range r.program.AllPackages {
		pkgs = append(pkgs, pkgInfo)
	}
	return pkgs
}

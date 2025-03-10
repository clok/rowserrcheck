package rowserr

import (
	"go/ast"
	"go/types"
	"strconv"

	"github.com/gostaticanalysis/analysisutil"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/ssa"
)

func NewAnalyzer(sqlPkgs ...string) *analysis.Analyzer {
	return &analysis.Analyzer{
		Name: "rowserrcheck",
		Doc:  Doc,
		Run:  NewRun(sqlPkgs...),
		Requires: []*analysis.Analyzer{
			buildssa.Analyzer,
		},
	}
}

const (
	Doc       = "rowserrcheck checks whether Rows.Err is checked"
	errMethod = "Err"
	rowsName  = "Rows"
)

type runner struct {
	pass     *analysis.Pass
	rowsTyp  *types.Pointer
	rowsObj  types.Object
	skipFile map[*ast.File]bool
	sqlPkgs  []string
}

func NewRun(pkgs ...string) func(pass *analysis.Pass) (interface{}, error) {
	return func(pass *analysis.Pass) (interface{}, error) {
		sqlPkgs := append(pkgs, "database/sql")
		for _, pkg := range sqlPkgs {
			r := new(runner)
			r.sqlPkgs = sqlPkgs
			r.run(pass, pkg)
		}
		return nil, nil
	}
}

// run executes an analysis for the pass. The receiver is passed
// by value because this func is called in parallel for different passes.
func (r runner) run(pass *analysis.Pass, pkgPath string) {
	r.pass = pass
	pssa := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA)
	funcs := pssa.SrcFuncs

	pkg := pssa.Pkg.Prog.ImportedPackage(pkgPath)
	if pkg == nil {
		// skip
		return
	}

	rowsType := pkg.Type(rowsName)
	if rowsType == nil {
		// skip checking
		return nil, nil
	}

	r.rowsObj = rowsType.Object()
	if r.rowsObj == nil {
		// skip checking
		return
	}

	resNamed, ok := r.rowsObj.Type().(*types.Named)
	if !ok {
		return
	}

	r.rowsTyp = types.NewPointer(resNamed)
	r.skipFile = map[*ast.File]bool{}

	for _, f := range funcs {
		if r.noImportedDBSQL(f) {
			// skip this
			continue
		}

		// skip if the function is just referenced
		var isRefFunc bool

		for i := 0; i < f.Signature.Results().Len(); i++ {
			if types.Identical(f.Signature.Results().At(i).Type(), r.rowsTyp) {
				isRefFunc = true
			}
		}

		if isRefFunc {
			continue
		}

		for _, b := range f.Blocks {
			for i := range b.Instrs {
				if r.notCheck(b, i) {
					pass.Reportf(b.Instrs[i].Pos(), "rows.Err must be checked")
				}
			}
		}
	}
}

func (r *runner) notCheck(b *ssa.BasicBlock, i int) (ret bool) {
	call, ok := r.getReqCall(b.Instrs[i])
	if !ok {
		return false
	}

	for _, cRef := range *call.Referrers() {
		val, ok := r.getResVal(cRef)
		if !ok {
			continue
		}
		if len(*val.Referrers()) == 0 {
			return true
		}

		resRefs := *val.Referrers()
		var notCallClose func(resRef ssa.Instruction) bool
		notCallClose = func(resRef ssa.Instruction) bool {
			switch resRef := resRef.(type) {
			case *ssa.Phi:
				resRefs = append(resRefs, *resRef.Referrers()...)
				for _, rf := range *resRef.Referrers() {
					if !notCallClose(rf) {
						return false
					}
				}

			case *ssa.Store: // Call in Closure function
				if len(*resRef.Addr.Referrers()) == 0 {
					return true
				}

				for _, aref := range *resRef.Addr.Referrers() {
					if c, ok := aref.(*ssa.MakeClosure); ok {
						f := c.Fn.(*ssa.Function)
						if r.noImportedDBSQL(f) {
							// skip this
							return false
						}
						called := r.isClosureCalled(c)

						return r.calledInFunc(f, called)
					}
				}
			case *ssa.Call: // Indirect function call
				if r.isCloseCall(resRef) {
					return false
				}
				if f, ok := resRef.Call.Value.(*ssa.Function); ok {
					for _, b := range f.Blocks {
						for i := range b.Instrs {
							return r.notCheck(b, i)
						}
					}
				}
			case *ssa.FieldAddr:
				for _, bRef := range *resRef.Referrers() {
					bOp, ok := r.getBodyOp(bRef)
					if !ok {
						continue
					}

					for _, ccall := range *bOp.Referrers() {
						if r.isCloseCall(ccall) {
							return false
						}
					}
				}
			}

			return true
		}

		for _, resRef := range resRefs {
			if !notCallClose(resRef) {
				return false
			}
		}
	}

	return true
}

func (r *runner) getReqCall(instr ssa.Instruction) (*ssa.Call, bool) {
	call, ok := instr.(*ssa.Call)
	if !ok {
		return nil, false
	}

	res := call.Call.Signature().Results()
	flag := false

	for i := 0; i < res.Len(); i++ {
		flag = flag || types.Identical(res.At(i).Type(), r.rowsTyp)
	}

	if !flag {
		return nil, false
	}

	return call, true
}

func (r *runner) getResVal(instr ssa.Instruction) (ssa.Value, bool) {
	switch instr := instr.(type) {
	case *ssa.Call:
		if len(instr.Call.Args) == 1 && types.Identical(instr.Call.Args[0].Type(), r.rowsTyp) {
			return instr.Call.Args[0], true
		}
	case ssa.Value:
		if types.Identical(instr.Type(), r.rowsTyp) {
			return instr, true
		}
	default:
	}

	return nil, false
}

func (r *runner) getBodyOp(instr ssa.Instruction) (*ssa.UnOp, bool) {
	op, ok := instr.(*ssa.UnOp)
	if !ok {
		return nil, false
	}
	// fix: try to check type
	// if op.Type() != r.rowsObj.Type() {
	// 	return nil, false
	// }
	return op, true
}

func (r *runner) isCloseCall(ccall ssa.Instruction) bool {
	switch ccall := ccall.(type) {
	case *ssa.Defer:
		if ccall.Call.Value != nil && ccall.Call.Value.Name() == errMethod {
			return true
		}
	case *ssa.Call:
		if ccall.Call.Value != nil && ccall.Call.Value.Name() == errMethod {
			return true
		}
	}

	return false
}

func (r *runner) isClosureCalled(c *ssa.MakeClosure) bool {
	for _, ref := range *c.Referrers() {
		switch ref.(type) {
		case *ssa.Call, *ssa.Defer:
			return true
		}
	}

	return false
}

func (r *runner) noImportedDBSQL(f *ssa.Function) (ret bool) {
	obj := f.Object()
	if obj == nil {
		return false
	}

	file := analysisutil.File(r.pass, obj.Pos())
	if file == nil {
		return false
	}

	if skip, has := r.skipFile[file]; has {
		return skip
	}
	defer func() {
		r.skipFile[file] = ret
	}()

	for _, impt := range file.Imports {
		path, err := strconv.Unquote(impt.Path.Value)
		if err != nil {
			continue
		}
		path = analysisutil.RemoveVendor(path)
		for _, pkg := range r.sqlPkgs {
			if pkg == path {
				return false
			}
		}
	}

	return true
}

func (r *runner) calledInFunc(f *ssa.Function, called bool) bool {
	for _, b := range f.Blocks {
		for i, instr := range b.Instrs {
			switch instr := instr.(type) {
			case *ssa.UnOp:
				for _, ref := range *instr.Referrers() {
					if v, ok := ref.(ssa.Value); ok {
						if vCall, ok := v.(*ssa.Call); ok {
							if vCall.Call.Value != nil && vCall.Call.Value.Name() == errMethod {
								if called {
									return false
								}
							}
						}
					}
				}
			default:
				if r.notCheck(b, i) || !called {
					return true
				}
			}
		}
	}

	return true
}

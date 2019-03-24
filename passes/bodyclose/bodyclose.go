package bodyclose

import (
	"go/ast"
	"go/types"
	"log"
	"strconv"

	"github.com/gostaticanalysis/analysisutil"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/ssa"
)

var Analyzer = &analysis.Analyzer{
	Name: "bodyclose",
	Doc:  Doc,
	Run:  new(runner).run,
	Requires: []*analysis.Analyzer{
		buildssa.Analyzer,
	},
}

const (
	Doc = "bodyclose checks whether HTTP response body is closed successfully"

	nethttpPath = "net/http"
)

type runner struct {
	pass   *analysis.Pass
	resObj types.Object
	//resNamed *types.Named
	//resTyp   *types.Pointer
	//closeMthd  *types.Func
	skipFile map[*ast.File]bool
}

func (r *runner) run(pass *analysis.Pass) (interface{}, error) {
	r.pass = pass
	funcs := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA).SrcFuncs

	r.resObj = analysisutil.LookupFromImports(pass.Pkg.Imports(), nethttpPath, "Response")
	if r.resObj == nil {
		// skip checking
		return nil, nil
	}

	//log.Printf("%+v", r.resObj)
	//
	//resNamed, ok := r.resObj.Type().(*types.Named)
	//if !ok {
	//	return nil, fmt.Errorf("cannot find http.Response")
	//}
	//log.Printf("resNamed: %+v", resNamed)
	//r.resNamed = resNamed
	//r.resTyp = types.NewPointer(r.resNamed)
	//log.Printf("r.resTyp: %+v", r.resTyp)
	//log.Printf("r.resNamed.Obj(): %+v", 	r.resNamed.Obj())
	//
	//for i := 0; i < r.resNamed.NumMethods(); i++ {
	//	mthd := r.resNamed.Method(i)
	//	switch mthd.Id() {
	//	case "Close":
	//		r.closeMthd = mthd
	//	}
	//}
	//if r.closeMthd == nil {
	//	return nil, fmt.Errorf("cannot find http.Response.Body.Close")
	//}

	r.skipFile = map[*ast.File]bool{}
	for _, f := range funcs {
		if r.noImportedNetHTTP(f) {
			// skip this
			continue
		}

		for _, b := range f.Blocks {
			for i := range b.Instrs {
				pos := b.Instrs[i].Pos()
				if r.isopen(b, i) {
					pass.Reportf(pos, "response body must be closed")
				}
			}
		}
	}

	return nil, nil
}

func (r *runner) isopen(b *ssa.BasicBlock, i int) bool {
	log.Printf("b.Instrs[i]: %+v", b.Instrs[i])

	if r.callDeferIn(b.Instrs[i]) {
		return false
	}

	return true
	//if !types.Identical(call.Type(), r.resTyp) {
	//	return false
	//}
	//
	//if r.callCloseIn(b.Instrs[i:], call) {
	//	return false
	//}
	//
	//if r.callCloseInSuccs(b, call, map[*ssa.BasicBlock]bool{}) {
	//	return false
	//}
	//
	//return true
}
func (r *runner) callDeferIn(instr ssa.Instruction) bool {
	switch instr := instr.(type) {
	case *ssa.Defer:
		log.Printf("df.Call: %+v", instr.Call)
		log.Printf("instr.Call.Signature().Recv(): %+v", instr.Call.Signature().Recv())
		log.Printf("instr.Call.Method.Name(): %+v", instr.Call.Method.Name())
		log.Printf("instr.Call.Value.Type(): %+v", instr.Call.Value.Type())
		log.Printf("instr.Call.Value: %+v", instr.Call.Value)

		log.Printf("instr.Parent(): %+v", instr.Parent())

		//call, ok := instr.(*ssa.Call)
		//if !ok {
		//	return false
		//}
		//
		//log.Printf("call: %+v", call)
		//fn := instr.Common().StaticCallee()
		//args := instr.Common().Args
		//if fn != nil && fn.Package() != nil &&
		//	(fn.RelString(fn.Package().Pkg) == "(*Response).Body.Close" &&
		//		types.Identical(fn.Signature, r.closeMthd.Type())) &&
		//	len(args) != 0 && call == args[0] {
		//	return true
		//}
	}

	return false
}

//
//func (r *runner) callCloseIn(instrs []ssa.Instruction, call *ssa.Call) bool {
//	for _, instr := range instrs {
//		switch instr := instr.(type) {
//		case ssa.CallInstruction:
//			fn := instr.Common().StaticCallee()
//			args := instr.Common().Args
//			if fn != nil && fn.Package() != nil &&
//				(fn.RelString(fn.Package().Pkg) == "(*Response).Body.Close" &&
//					types.Identical(fn.Signature, r.closeMthd.Type())) &&
//				len(args) != 0 && call == args[0] {
//				return true
//			}
//		}
//	}
//	return false
//}
//
//func (r *runner) callCloseInSuccs(b *ssa.BasicBlock, call *ssa.Call, done map[*ssa.BasicBlock]bool) bool {
//	if done[b] {
//		return false
//	}
//	done[b] = true
//
//	if len(b.Succs) == 0 {
//		return r.isReturnIter(b.Instrs, call)
//	}
//
//	for _, s := range b.Succs {
//		if !r.callCloseIn(s.Instrs, call) &&
//			!r.callCloseInSuccs(s, call, done) {
//			return false
//		}
//	}
//
//	return true
//}
//
//func (r *runner) isReturnIter(instrs []ssa.Instruction, call *ssa.Call) bool {
//	if len(instrs) == 0 {
//		return false
//	}
//
//	ret, isRet := instrs[len(instrs)-1].(*ssa.Return)
//	if !isRet {
//		return false
//	}
//
//	for _, r := range ret.Results {
//		if r == call {
//			return true
//		}
//	}
//
//	return false
//}

func (r *runner) noImportedNetHTTP(f *ssa.Function) (ret bool) {
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
		if path == nethttpPath {
			return false
		}
	}

	return true
}

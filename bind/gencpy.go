// Copyright 2015 The go-python Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bind

import (
	"fmt"
	"go/token"
	"path/filepath"
	"strings"
)

const (
	cPreamble = `/*
  C stubs for package %[1]s.
  gopy gen -lang=python %[1]s

  File is generated by gopy gen. Do not edit.
*/

#ifdef _POSIX_C_SOURCE
#undef _POSIX_C_SOURCE
#endif

#include "Python.h"
#include "structmember.h"

// header exported from 'go tool cgo'
#include "%[3]s.h"

`
)

type cpyGen struct {
	decl *printer
	impl *printer

	fset *token.FileSet
	pkg  *Package
	err  ErrorList
}

func (g *cpyGen) gen() error {

	g.genPreamble()

	// first, process structs
	for _, s := range g.pkg.structs {
		g.genStruct(s)
	}

	for _, f := range g.pkg.funcs {
		g.genFunc(f)
	}

	g.impl.Printf("static PyMethodDef cpy_%s_methods[] = {\n", g.pkg.pkg.Name())
	g.impl.Indent()
	for _, f := range g.pkg.funcs {
		name := f.GoName()
		//obj := scope.Lookup(name)
		g.impl.Printf("{%[1]q, %[2]s, METH_VARARGS, %[3]q},\n",
			name, "gopy_"+f.ID(), f.Doc(),
		)
	}
	g.impl.Printf("{NULL, NULL, 0, NULL}        /* Sentinel */\n")
	g.impl.Outdent()
	g.impl.Printf("};\n\n")

	g.impl.Printf("PyMODINIT_FUNC\ninit%[1]s(void)\n{\n", g.pkg.pkg.Name())
	g.impl.Indent()
	g.impl.Printf("PyObject *module = NULL;\n\n")

	for _, s := range g.pkg.structs {
		g.impl.Printf(
			"if (PyType_Ready(&_gopy_%sType) < 0) { return; }\n",
			s.ID(),
		)
	}

	g.impl.Printf("module = Py_InitModule3(%[1]q, cpy_%[1]s_methods, %[2]q);\n\n",
		g.pkg.pkg.Name(),
		g.pkg.doc.Doc,
	)

	for _, s := range g.pkg.structs {
		g.impl.Printf("Py_INCREF(&_gopy_%sType);\n", s.ID())
		g.impl.Printf("PyModule_AddObject(module, %q, (PyObject*)&_gopy_%sType);\n\n",
			s.GoName(),
			s.ID(),
		)
	}
	g.impl.Outdent()
	g.impl.Printf("}\n\n")

	if len(g.err) > 0 {
		return g.err
	}

	return nil
}

func (g *cpyGen) genFunc(o Func) {

	g.impl.Printf(`
/* pythonization of: %[1]s.%[2]s */
static PyObject*
gopy_%[3]s(PyObject *self, PyObject *args) {
`,
		g.pkg.pkg.Name(),
		o.GoName(),
		o.ID(),
	)

	g.impl.Indent()
	g.genFuncBody(o.ID(), o.Signature())
	g.impl.Outdent()
	g.impl.Printf("}\n\n")
}

func (g *cpyGen) genFuncBody(id string, sig *Signature) {
	funcArgs := []string{}

	res := sig.Results()
	args := sig.Params()
	var recv *Var
	if sig.Recv() != nil {
		recv = sig.Recv()
		recv.genRecvDecl(g.impl)
		funcArgs = append(funcArgs, recv.getFuncArg())
	}

	for _, arg := range args {
		arg.genDecl(g.impl)
		funcArgs = append(funcArgs, arg.getFuncArg())
	}

	// FIXME(sbinet) pythonize (turn errors into python exceptions)
	if len(res) > 0 {
		switch len(res) {
		case 1:
			ret := res[0]
			ret.genRetDecl(g.impl)
		default:
			g.impl.Printf("struct %[1]s_return c_gopy_ret;\n", id)
			/*
					for i := 0; i < res.Len(); i++ {
						ret := res.At(i)
						n := ret.Name()
						if n == "" {
							n = "gopy_" + strconv.Itoa(i)
						}
						g.impl.Printf("%[1]s c_%[2]s;\n", ctypeName(ret.Type()), n)
				    }
			*/
		}
	}

	g.impl.Printf("\n")

	if recv != nil {
		recv.genRecvImpl(g.impl)
	}

	if len(args) > 0 {
		g.impl.Printf("if (!PyArg_ParseTuple(args, ")
		format := []string{}
		pyaddrs := []string{}
		for _, arg := range args {
			pyfmt, addr := arg.getArgParse()
			format = append(format, pyfmt)
			pyaddrs = append(pyaddrs, addr)
		}
		g.impl.Printf("%q, %s)) {\n", strings.Join(format, ""), strings.Join(pyaddrs, ", "))
		g.impl.Indent()
		g.impl.Printf("return NULL;\n")
		g.impl.Outdent()
		g.impl.Printf("}\n\n")
	}

	if len(args) > 0 {
		for _, arg := range args {
			arg.genFuncPreamble(g.impl)
		}
		g.impl.Printf("\n")
	}

	if len(res) > 0 {
		g.impl.Printf("c_gopy_ret = ")
	}

	g.impl.Printf("GoPy_%[1]s(%[2]s);\n", id, strings.Join(funcArgs, ", "))

	g.impl.Printf("\n")

	if len(res) <= 0 {
		g.impl.Printf("Py_INCREF(Py_None);\nreturn Py_None;\n")
		return
	}

	format := []string{}
	funcArgs = []string{}
	switch len(res) {
	case 1:
		ret := res[0]
		pyfmt, _ := ret.getArgParse()
		format = append(format, pyfmt)
		funcArgs = append(funcArgs, "c_gopy_ret")
	default:
		for _, ret := range res {
			pyfmt, _ := ret.getArgParse()
			format = append(format, pyfmt)
			funcArgs = append(funcArgs, ret.getFuncArg())
		}
	}

	g.impl.Printf("return Py_BuildValue(%q, %s);\n",
		strings.Join(format, ""),
		strings.Join(funcArgs, ", "),
	)
	//g.impl.Printf("return NULL;\n")
}

func (g *cpyGen) genStruct(cpy Struct) {
	pkgname := cpy.GoObj().Pkg().Name()

	//fmt.Printf("obj: %#v\ntyp: %#v\n", obj, typ)
	g.decl.Printf("/* --- decls for struct %s.%v --- */\n", pkgname, cpy.GoName())
	g.decl.Printf("typedef void* GoPy_%s;\n\n", cpy.ID())
	g.decl.Printf("/* type for struct %s.%v\n", pkgname, cpy.GoName())
	g.decl.Printf(" */\ntypedef struct {\n")
	g.decl.Indent()
	g.decl.Printf("PyObject_HEAD\n")
	g.decl.Printf("GoPy_%[1]s cgopy; /* unsafe.Pointer to %[1]s */\n", cpy.ID())
	g.decl.Outdent()
	g.decl.Printf("} _gopy_%s;\n", cpy.ID())
	g.decl.Printf("\n\n")

	g.impl.Printf("/* --- impl for %s.%v */\n\n", pkgname, cpy.GoName())

	g.genStructNew(cpy)
	g.genStructDealloc(cpy)
	g.genStructInit(cpy)
	g.genStructMembers(cpy)
	g.genStructMethods(cpy)

	g.impl.Printf("static PyTypeObject _gopy_%sType = {\n", cpy.ID())
	g.impl.Indent()
	g.impl.Printf("PyObject_HEAD_INIT(NULL)\n")
	g.impl.Printf("0,\t/*ob_size*/\n")
	g.impl.Printf("\"%s.%s\",\t/*tp_name*/\n", pkgname, cpy.GoName())
	g.impl.Printf("sizeof(_gopy_%s),\t/*tp_basicsize*/\n", cpy.ID())
	g.impl.Printf("0,\t/*tp_itemsize*/\n")
	g.impl.Printf("(destructor)_gopy_%s_dealloc,\t/*tp_dealloc*/\n", cpy.ID())
	g.impl.Printf("0,\t/*tp_print*/\n")
	g.impl.Printf("0,\t/*tp_getattr*/\n")
	g.impl.Printf("0,\t/*tp_setattr*/\n")
	g.impl.Printf("0,\t/*tp_compare*/\n")
	g.impl.Printf("0,\t/*tp_repr*/\n")
	g.impl.Printf("0,\t/*tp_as_number*/\n")
	g.impl.Printf("0,\t/*tp_as_sequence*/\n")
	g.impl.Printf("0,\t/*tp_as_mapping*/\n")
	g.impl.Printf("0,\t/*tp_hash */\n")
	g.impl.Printf("0,\t/*tp_call*/\n")
	g.impl.Printf("0,\t/*tp_str*/\n")
	g.impl.Printf("0,\t/*tp_getattro*/\n")
	g.impl.Printf("0,\t/*tp_setattro*/\n")
	g.impl.Printf("0,\t/*tp_as_buffer*/\n")
	g.impl.Printf("Py_TPFLAGS_DEFAULT,\t/*tp_flags*/\n")
	g.impl.Printf("%q,\t/* tp_doc */\n", cpy.Doc())
	g.impl.Printf("0,\t/* tp_traverse */\n")
	g.impl.Printf("0,\t/* tp_clear */\n")
	g.impl.Printf("0,\t/* tp_richcompare */\n")
	g.impl.Printf("0,\t/* tp_weaklistoffset */\n")
	g.impl.Printf("0,\t/* tp_iter */\n")
	g.impl.Printf("0,\t/* tp_iternext */\n")
	g.impl.Printf("_gopy_%s_methods,             /* tp_methods */\n", cpy.ID())
	g.impl.Printf("0,\t/* tp_members */\n")
	g.impl.Printf("_gopy_%s_getsets,\t/* tp_getset */\n", cpy.ID())
	g.impl.Printf("0,\t/* tp_base */\n")
	g.impl.Printf("0,\t/* tp_dict */\n")
	g.impl.Printf("0,\t/* tp_descr_get */\n")
	g.impl.Printf("0,\t/* tp_descr_set */\n")
	g.impl.Printf("0,\t/* tp_dictoffset */\n")
	g.impl.Printf("(initproc)_gopy_%s_init,      /* tp_init */\n", cpy.ID())
	g.impl.Printf("0,                         /* tp_alloc */\n")
	g.impl.Printf("_gopy_%s_new,\t/* tp_new */\n", cpy.ID())
	g.impl.Outdent()
	g.impl.Printf("};\n\n")

}

func (g *cpyGen) genStructDealloc(cpy Struct) {
	pkgname := cpy.GoObj().Pkg().Name()

	g.decl.Printf("/* tp_dealloc for %s.%v */\n", pkgname, cpy.GoName())
	g.decl.Printf("static void\n_gopy_%[1]s_dealloc(_gopy_%[1]s *self);\n",
		cpy.ID(),
	)

	g.impl.Printf("/* tp_dealloc for %s.%v */\n", pkgname, cpy.GoName())
	g.impl.Printf("static void\n_gopy_%[1]s_dealloc(_gopy_%[1]s *self) {\n",
		cpy.ID(),
	)
	g.impl.Indent()
	g.impl.Printf("self->ob_type->tp_free((PyObject*)self);\n")
	g.impl.Outdent()
	g.impl.Printf("}\n\n")
}

func (g *cpyGen) genStructNew(cpy Struct) {
	pkgname := cpy.GoObj().Pkg().Name()

	g.decl.Printf("/* tp_new for %s.%v */\n", pkgname, cpy.GoName())
	g.decl.Printf(
		"static PyObject*\n_gopy_%s_new(PyTypeObject *type, PyObject *args, PyObject *kwds);\n",
		cpy.ID(),
	)

	g.impl.Printf("/* tp_new */\n")
	g.impl.Printf(
		"static PyObject*\n_gopy_%s_new(PyTypeObject *type, PyObject *args, PyObject *kwds) {\n",
		cpy.ID(),
	)
	g.impl.Indent()
	g.impl.Printf("_gopy_%s *self;\n", cpy.ID())
	g.impl.Printf("self = (_gopy_%s *)type->tp_alloc(type, 0);\n", cpy.ID())
	g.impl.Printf("self->cgopy = GoPy_%s_new();\n", cpy.ID())
	g.impl.Printf("return (PyObject*)self;\n")
	g.impl.Outdent()
	g.impl.Printf("}\n\n")
}

func (g *cpyGen) genStructInit(cpy Struct) {
	pkgname := cpy.GoObj().Pkg().Name()

	g.decl.Printf("/* tp_init for %s.%v */\n", pkgname, cpy.GoName())
	g.decl.Printf(
		"static int\n_gopy_%[1]s_init(_gopy_%[1]s *self, PyObject *args, PyObject *kwds);\n",
		cpy.ID(),
	)

	g.impl.Printf("/* tp_init */\n")
	g.impl.Printf(
		"static int\n_gopy_%[1]s_init(_gopy_%[1]s *self, PyObject *args, PyObject *kwds) {\n",
		cpy.ID(),
	)
	g.impl.Indent()
	g.impl.Printf("return 0;\n")
	g.impl.Outdent()
	g.impl.Printf("}\n\n")
}

func (g *cpyGen) genStructMembers(cpy Struct) {
	pkgname := cpy.GoObj().Pkg().Name()
	typ := cpy.Struct()

	g.decl.Printf("/* tp_getset for %s.%v */\n", pkgname, cpy.GoName())
	for i := 0; i < typ.NumFields(); i++ {
		f := typ.Field(i)
		if !f.Exported() {
			continue
		}
		ft := f.Type()
		g.decl.Printf("static PyObject*\n")
		g.decl.Printf(
			"_gopy_%[1]s_getter_%[2]d(_gopy_%[1]s *self, void *closure); /* %[3]s */\n",
			cpy.ID(),
			i+1,
			f.Name(),
		)

		g.impl.Printf("static PyObject*\n")
		g.impl.Printf(
			"_gopy_%[1]s_getter_%[2]d(_gopy_%[1]s *self, void *closure) /* %[3]s */ {\n",
			cpy.ID(),
			i+1,
			f.Name(),
		)
		g.impl.Indent()
		ftname := cgoTypeName(ft)
		if needWrapType(ft) {
			ftname = fmt.Sprintf("GoPy_%[1]s_field_%d", cpy.GoName(), i+1)
			g.impl.Printf(
				"%[1]s ret = GoPy_%[2]s_getter_%[3]d(self->cgopy);\n",
				ftname,
				cpy.ID(),
				i+1,
			)
		} else {
			g.impl.Printf(
				"%[1]s ret = GoPy_%[2]s_getter_%[3]d(self->cgopy);\n",
				ftname,
				cpy.ID(),
				i+1,
			)
		}
		g.impl.Printf("Py_RETURN_NONE;\n") // FIXME(sbinet)
		g.impl.Outdent()
		g.impl.Printf("}\n\n")

		g.decl.Printf("static int\n")
		g.decl.Printf(
			"_gopy_%[1]s_setter_%[2]d(_gopy_%[1]s *self, PyObject *value, void *closure);\n",
			cpy.ID(),
			i+1,
		)

		g.impl.Printf("static int\n")
		g.impl.Printf(
			"_gopy_%[1]s_setter_%[2]d(_gopy_%[1]s *self, PyObject *value, void *closure) {\n",
			cpy.ID(),
			i+1,
		)
		g.impl.Indent()
		g.impl.Printf("return 0;\n") // FIXME(sbinet)
		g.impl.Outdent()
		g.impl.Printf("}\n\n")
	}

	g.impl.Printf("/* tp_getset for %s.%v */\n", pkgname, cpy.GoName())
	g.impl.Printf("static PyGetSetDef _gopy_%s_getsets[] = {\n", cpy.ID())
	g.impl.Indent()
	for i := 0; i < typ.NumFields(); i++ {
		f := typ.Field(i)
		if !f.Exported() {
			continue
		}
		doc := "doc for " + f.Name() // FIXME(sbinet) retrieve doc for fields
		g.impl.Printf("{%q, ", f.Name())
		g.impl.Printf("(getter)_gopy_%[1]s_getter_%[2]d, ", cpy.ID(), i+1)
		g.impl.Printf("(setter)_gopy_%[1]s_setter_%[2]d, ", cpy.ID(), i+1)
		g.impl.Printf("%q, NULL},\n", doc)
	}
	g.impl.Printf("{NULL} /* Sentinel */\n")
	g.impl.Outdent()
	g.impl.Printf("};\n\n")
	/*
		static PyGetSetDef Noddy_getseters[] = {
		    {"first",
		     (getter)Noddy_getfirst, (setter)Noddy_setfirst,
		     "first name",
		     NULL},
		    {"last",
		     (getter)Noddy_getlast, (setter)Noddy_setlast,
		     "last name",
		     NULL},
		    {NULL}
		};
	*/
	/*
				static PyObject *
				Noddy_getfirst(Noddy *self, void *closure)
				{
				    Py_INCREF(self->first);
				    return self->first;
				}

				static int
		Noddy_setfirst(Noddy *self, PyObject *value, void *closure)
		{
		  if (value == NULL) {
		    PyErr_SetString(PyExc_TypeError, "Cannot delete the first attribute");
		    return -1;
		  }

		  if (! PyString_Check(value)) {
		    PyErr_SetString(PyExc_TypeError,
		                    "The first attribute value must be a string");
		    return -1;
		  }

		  Py_DECREF(self->first);
		  Py_INCREF(value);
		  self->first = value;

		  return 0;
		}
	*/

}

func (g *cpyGen) genStructMethods(cpy Struct) {

	pkgname := cpy.GoObj().Pkg().Name()

	g.decl.Printf("/* methods for %s.%s */\n\n", pkgname, cpy.GoName())
	for _, m := range cpy.meths {
		g.genMethod(cpy, m)
	}

	g.impl.Printf("static PyMethodDef _gopy_%s_methods[] = {\n", cpy.ID())
	g.impl.Indent()
	for _, m := range cpy.meths {
		margs := "METH_VARARGS"
		if m.Return() == nil {
			margs = "METH_NOARGS"
		}
		g.impl.Printf(
			"{%[1]q, (PyCFunction)gopy_%[2]s, %[3]s, %[4]q},\n",
			m.GoName(),
			m.ID(),
			margs,
			m.Doc(),
		)
		/*
			{"name", (PyCFunction)Noddy_name, METH_NOARGS,
			     "Return the name, combining the first and last name"
			    },
		*/
	}
	g.impl.Printf("{NULL} /* sentinel */\n")
	g.impl.Outdent()
	g.impl.Printf("};\n\n")
}

func (g *cpyGen) genMethod(cpy Struct, fct Func) {
	pkgname := g.pkg.pkg.Name()
	g.decl.Printf("/* wrapper of %[1]s.%[2]s */\n",
		pkgname,
		cpy.GoName()+"."+fct.GoName(),
	)
	g.decl.Printf("static PyObject*\n")
	g.decl.Printf("gopy_%s(PyObject *self, PyObject *args);\n", fct.ID())

	g.impl.Printf("/* wrapper of %[1]s.%[2]s */\n",
		pkgname,
		cpy.GoName()+"."+fct.GoName(),
	)
	g.impl.Printf("static PyObject*\n")
	g.impl.Printf("gopy_%s(PyObject *self, PyObject *args) {\n", fct.ID())
	g.impl.Indent()
	g.genMethodBody(cpy, fct)
	g.impl.Outdent()
	g.impl.Printf("}\n\n")
}

func (g *cpyGen) genMethodBody(cpy Struct, fct Func) {
	g.genFuncBody(fct.ID(), fct.Signature())
}

func (g *cpyGen) genPreamble() {
	n := g.pkg.pkg.Name()
	g.decl.Printf(cPreamble, n, g.pkg.pkg.Path(), filepath.Base(n))
}

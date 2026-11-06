package locate

import "testing"

func TestParseDeclsReadsBytesWithoutAFile(t *testing.T) {
	goSrc := []byte("package p\n\ntype S struct{}\n\nfunc (s *S) Run() {}\n\nfunc Free() {}\n")
	got := ParseDecls("x.go", goSrc)
	names := map[string]Decl{}
	for _, d := range got {
		names[d.Name] = d
	}
	if names["Run"].Container != "S" || names["Run"].Kind != KindMethod || names["Free"].EndLine == 0 {
		t.Fatalf("go decls = %+v", got)
	}
	py := ParseDecls("x.py", []byte("class A:\n    def run(self):\n        pass\n"))
	if len(py) < 2 || py[0].Name != "A" {
		t.Fatalf("scanned decls = %+v", py)
	}
	if ParseDecls("x.go", []byte("package p\x00\x00")) != nil {
		t.Fatal("binary bytes have no declarations")
	}
}

package diff

import "testing"

const sample = `diff --git a/foo.go b/foo.go
index 111..222 100644
--- a/foo.go
+++ b/foo.go
@@ -1,4 +1,6 @@
 package foo

+// added comment
+var X = 1
 func A() {}
-func B() {}
+func B() int { return 0 }
diff --git a/new.go b/new.go
new file mode 100644
--- /dev/null
+++ b/new.go
@@ -0,0 +1,2 @@
+package new
+var Y = 2
`

func TestParse(t *testing.T) {
	d := Parse(sample)
	if d.ChangedFiles() != 2 {
		t.Fatalf("expected 2 files, got %d", d.ChangedFiles())
	}
	foo := d.Files["foo.go"]
	if foo == nil {
		t.Fatal("foo.go missing")
	}
	// New-side lines: 1 package, 2 blank, 3 added comment, 4 added var, 5 func A, 6 func B(int)
	if !foo.AddedLines[3] || !foo.AddedLines[4] || !foo.AddedLines[6] {
		t.Fatalf("expected added lines 3,4,6; got %v", foo.AddedLines)
	}
	if foo.AddedLines[1] || foo.AddedLines[5] {
		t.Fatalf("context lines should not be added: %v", foo.AddedLines)
	}
	if foo.Additions != 3 || foo.Deletions != 1 {
		t.Fatalf("counts wrong: +%d -%d", foo.Additions, foo.Deletions)
	}
}

func TestIsAddedLine(t *testing.T) {
	d := Parse(sample)
	if !d.IsAddedLine("new.go", 1) || !d.IsAddedLine("new.go", 2) {
		t.Fatal("new.go lines should be added")
	}
	if d.IsAddedLine("foo.go", 1) {
		t.Fatal("foo.go:1 is context, not added")
	}
	if d.IsAddedLine("absent.go", 1) {
		t.Fatal("absent file should be false")
	}
}

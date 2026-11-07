package assets

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
)

type svgNode struct {
	XMLName  xml.Name
	Attrs    []xml.Attr `xml:",any,attr"`
	Children []svgNode  `xml:",any"`
}

func (n svgNode) attr(name string) (string, bool) {
	for _, a := range n.Attrs {
		if a.Name.Local == name {
			return a.Value, true
		}
	}
	return "", false
}

// num reads a numeric attribute; an absent one is 0.
func (n svgNode) num(t *testing.T, name string) float64 {
	t.Helper()
	v, ok := n.attr(name)
	if !ok {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		t.Fatalf("%s=%q on <%s>: %v", name, v, n.XMLName.Local, err)
	}
	return f
}

func (n svgNode) walk(fn func(svgNode)) {
	fn(n)
	for _, c := range n.Children {
		c.walk(fn)
	}
}

func readMark(t *testing.T) svgNode {
	t.Helper()
	b, err := os.ReadFile("mark.svg")
	if err != nil {
		t.Fatal(err)
	}
	var root svgNode
	if err := xml.Unmarshal(b, &root); err != nil {
		t.Fatalf("mark.svg is not XML: %v", err)
	}
	return root
}

func rects(root svgNode) []svgNode {
	var out []svgNode
	root.walk(func(n svgNode) {
		if n.XMLName.Local == "rect" {
			out = append(out, n)
		}
	})
	return out
}

func tile(t *testing.T) svgNode {
	t.Helper()
	r := rects(readMark(t))
	if len(r) != 1 {
		t.Fatalf("want exactly one <rect> (the tile), got %d", len(r))
	}
	return r[0]
}

func TestMarkSVG_TileFillsTheViewBox(t *testing.T) {
	root := readMark(t)
	vb, _ := root.attr("viewBox")
	var x, y, w, h float64
	if _, err := fmt.Sscanf(vb, "%f %f %f %f", &x, &y, &w, &h); err != nil || x != 0 || y != 0 {
		t.Fatalf("viewBox %q is not `0 0 W H`", vb)
	}
	r := tile(t)
	if r.num(t, "x") != 0 || r.num(t, "y") != 0 || r.num(t, "width") != w || r.num(t, "height") != h {
		t.Fatalf("tile rect must be 0,0 %vx%v to fill the viewBox", w, h)
	}
}

func TestMarkSVG_CornerKeepsTheRasterRadius(t *testing.T) {
	r := tile(t)
	rx := r.num(t, "rx")
	if rx == 0 {
		t.Fatal("tile has square corners")
	}
	if got := rx / r.num(t, "width"); math.Abs(got-0.285) > 0.01 {
		t.Fatalf("rx/width = %.4f, want 0.285 +- 0.01", got)
	}
}

func TestMarkSVG_HasNoRasterOrText(t *testing.T) {
	root := readMark(t)
	root.walk(func(n svgNode) {
		switch n.XMLName.Local {
		case "image", "text", "foreignObject":
			t.Errorf("<%s> in mark.svg", n.XMLName.Local)
		}
	})
}

func TestMarkSVG_TransparentOutsideTheTile(t *testing.T) {
	root := readMark(t)
	for _, name := range []string{"style", "fill"} {
		if _, ok := root.attr(name); ok {
			t.Errorf("root <svg> sets %s", name)
		}
	}
	var first *svgNode
	for i := range root.Children {
		switch root.Children[i].XMLName.Local {
		case "defs", "title", "desc":
			continue
		}
		first = &root.Children[i]
		break
	}
	if first == nil || first.XMLName.Local != "rect" || first.num(t, "rx") <= 0 {
		t.Fatalf("first painted element must be the rounded tile <rect>, got %+v", first)
	}
	if v, _ := first.attr("fill"); strings.TrimSpace(v) == "" {
		t.Error("tile rect has no fill")
	}
}

func readMailMark(t *testing.T) image.Image {
	t.Helper()
	b, err := os.ReadFile("mail-mark.png")
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("mail-mark.png: %v", err)
	}
	return img
}

func alphaAt(img image.Image, x, y int) uint32 {
	_, _, _, a := img.At(x, y).RGBA()
	return a >> 8
}

func TestMailMark_IsTheMasterWithoutMargin(t *testing.T) {
	img := readMailMark(t)
	if b := img.Bounds(); b.Dx() != 144 || b.Dy() != 144 {
		t.Fatalf("size = %v, want 144x144", b)
	}
	for _, p := range [][2]int{{0, 0}, {143, 0}, {0, 143}, {143, 143}} {
		if a := alphaAt(img, p[0], p[1]); a != 0 {
			t.Errorf("alpha at %v = %d, want 0", p, a)
		}
	}
}

func TestMailMark_KeepsTheTransparentInset(t *testing.T) {
	img := readMailMark(t)
	for _, p := range [][2]int{{1, 72}, {142, 72}, {72, 1}, {72, 142}, {4, 72}, {72, 139}} {
		if a := alphaAt(img, p[0], p[1]); a != 0 {
			t.Errorf("inset pixel %v alpha = %d, want 0", p, a)
		}
	}
	for _, p := range [][2]int{{12, 72}, {131, 72}, {72, 12}, {72, 131}} {
		r, g, b, a := img.At(p[0], p[1]).RGBA()
		if a>>8 != 255 {
			t.Errorf("tile pixel %v alpha = %d, want 255", p, a>>8)
		}
		if mean := (r>>8 + g>>8 + b>>8) / 3; mean >= 128 {
			t.Errorf("tile pixel %v mean RGB = %d, want < 128 (dark tile)", p, mean)
		}
	}
}

func TestMailMark_SharpAtThreeX(t *testing.T) {
	if w := readMailMark(t).Bounds().Dx(); w < 108 {
		t.Fatalf("width = %d, want >= 108 (3x the 36px draw)", w)
	}
}

func TestMailMark_EmbedIsTheFile(t *testing.T) {
	want, err := os.ReadFile("mail-mark.png")
	if err != nil {
		t.Fatal(err)
	}
	if len(Mark) == 0 || !bytes.Equal(Mark, want) {
		t.Fatalf("Mark is %d bytes, mail-mark.png is %d", len(Mark), len(want))
	}
}
